package connections

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCredentialUpdatesAreConditional(t *testing.T) {
	factories := map[string]func(*testing.T) (Store, CredentialStore){
		"file": func(t *testing.T) (Store, CredentialStore) {
			s, err := NewFileStore(t.TempDir()+"/connections.json", "test")
			if err != nil {
				t.Fatal(err)
			}
			return s, s
		},
		"postgres": func(t *testing.T) (Store, CredentialStore) {
			dsn := os.Getenv("POSTGRES_TEST_DSN")
			if dsn == "" {
				t.Skip("POSTGRES_TEST_DSN is not set")
			}
			pool, err := pgxpool.New(t.Context(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			s := PGStore{Pool: pool, SecretKey: "test"}
			return s, s
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			for _, change := range []string{"delete", "reauthorize", "metadata", "recreate", "refresh"} {
				t.Run(change, func(t *testing.T) {
					store, versioned := factory(t)
					ctx := t.Context()
					user := fmt.Sprintf("revision-%d", time.Now().UnixNano())
					t.Cleanup(func() { store.Delete(context.Background(), user, ProviderGCP) })
					if err := store.UpsertToken(ctx, user, ProviderGCP, "old", nil, "old-account"); err != nil {
						t.Fatal(err)
					}
					snapshot, _, err := versioned.LoadCredential(ctx, user, ProviderGCP, DefaultInstanceID)
					if err != nil {
						t.Fatal(err)
					}
					switch change {
					case "delete":
						err = store.Delete(ctx, user, ProviderGCP)
					case "recreate":
						if err = store.Delete(ctx, user, ProviderGCP); err == nil {
							err = store.UpsertToken(ctx, user, ProviderGCP, "old", nil, "old-account")
						}
					case "metadata":
						err = store.(InstanceStore).UpsertTokenInstance(ctx, user, ProviderGCP, DefaultInstanceID, "new label", "new", nil, "new-account", map[string]string{"target": "new"})
					case "refresh":
						newer := snapshot
						newer.Account = "new-account"
						err = versioned.UpdateCredential(ctx, newer, "new")
					default:
						err = store.UpsertToken(ctx, user, ProviderGCP, "new", nil, "new-account")
					}
					if err != nil {
						t.Fatal(err)
					}
					snapshot.Account = "stale-account"
					if err := versioned.UpdateCredential(ctx, snapshot, "stale-refresh"); !errors.Is(err, ErrConnectionChanged) {
						t.Fatalf("stale update=%v", err)
					}
					current, raw, err := versioned.LoadCredential(ctx, user, ProviderGCP, DefaultInstanceID)
					if change == "delete" {
						if !errors.Is(err, ErrNotConnected) {
							t.Fatalf("deleted connection resurrected: %+v %v", current, err)
						}
						return
					}
					if err != nil || raw == "stale-refresh" || current.Account == "stale-account" {
						t.Fatalf("new authorization overwritten: %+v token=%q err=%v", current, raw, err)
					}
					if change == "metadata" && (current.Label != "new label" || current.Metadata["target"] != "new") {
						t.Fatalf("target overwritten: %+v", current)
					}
				})
			}
		})
	}
}

func TestRefreshCannotResurrectOrOverwriteConnection(t *testing.T) {
	for _, provider := range []string{ProviderClickStack, ProviderGCP, ProviderNotion} {
		t.Run(provider, func(t *testing.T) {
			for _, deleted := range []bool{false, true} {
				t.Run(fmt.Sprint(deleted), func(t *testing.T) {
					ctx := t.Context()
					path := t.TempDir() + "/connections.json"
					store, _ := NewFileStore(path, "test")
					peer, _ := NewFileStore(path, "test")
					raw, _ := encodeClickStackTokenBundle(clickStackTokenBundle{Access: "old", Refresh: "refresh", ClientID: "client", ExpiresAt: time.Now().Add(-time.Hour)})
					if err := store.UpsertToken(ctx, "user", provider, raw, nil, "old-account"); err != nil {
						t.Fatal(err)
					}
					entered, release := make(chan struct{}), make(chan struct{})
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/token" {
							w.WriteHeader(404)
							return
						}
						close(entered)
						<-release
						fmt.Fprint(w, `{"access_token":"fresh","refresh_token":"rotated","expires_in":3600}`)
					}))
					defer server.Close()
					service := &Service{Store: store, Config: Config{PublicBaseURL: "http://localhost:8765", SecretKey: "test", ClickStack: ClickStackOAuthConfig{ServiceID: "service"}, GCP: GCPOAuthConfig{ClientID: "client", ClientSecret: "secret"}, Notion: NotionOAuthConfig{MCPURL: "https://example.test/mcp"}}}
					state := service.mutableState()
					state.clickstackOAuth = newClickStackOAuth(service.Config.ClickStack)
					state.clickstackOAuth.tokenURL = server.URL + "/token"
					state.gcpOAuth = newGCPOAuth(service.Config.GCP)
					state.gcpOAuth.tokenURL = server.URL + "/token"
					state.gcpOAuth.httpClient = &http.Client{Transport: credentialTransport(func(req *http.Request) (*http.Response, error) {
						if req.URL.Path != "/token" {
							return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
						}
						return server.Client().Transport.RoundTrip(req)
					})}
					state.notionOAuth = newNotionOAuth(service.Config.Notion)
					state.notionOAuth.tokenURL = server.URL + "/token"
					done := make(chan error, 1)
					go func() {
						var err error
						switch provider {
						case ProviderClickStack:
							_, err = service.ClickStackAccessToken(ctx, "user")
						case ProviderGCP:
							_, err = service.GCPAccessToken(ctx, "user")
						case ProviderNotion:
							_, err = service.NotionAccessToken(ctx, "user")
						}
						done <- err
					}()
					select {
					case <-entered:
					case err := <-done:
						t.Fatalf("refresh returned before token request: %v", err)
					case <-time.After(3 * time.Second):
						t.Fatal("refresh did not start")
					}
					var err error
					if deleted {
						err = peer.Delete(ctx, "user", provider)
					} else {
						err = peer.UpsertToken(ctx, "user", provider, "new-authorization", nil, "new-account")
					}
					close(release)
					if err != nil {
						t.Fatal(err)
					}
					if err := <-done; !errors.Is(err, ErrConnectionChanged) {
						t.Fatalf("refresh conflict=%v", err)
					}
					current, err := store.RawToken(ctx, "user", provider)
					if deleted {
						if !errors.Is(err, ErrNotConnected) {
							t.Fatalf("resurrected token=%q err=%v", current, err)
						}
					} else if err != nil || current != "new-authorization" {
						t.Fatalf("new authorization lost: token=%q err=%v", current, err)
					}
				})
			}
		})
	}
}

type credentialTransport func(*http.Request) (*http.Response, error)

func (f credentialTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFileStoreTransactionsSerializeAcrossInstances(t *testing.T) {
	path := t.TempDir() + "/connections.json"
	errors := make(chan error, 20)
	for i := range 20 {
		go func(i int) {
			store, err := NewFileStore(path, "test")
			if err == nil {
				err = store.UpsertToken(t.Context(), fmt.Sprint(i), ProviderGCP, "token", nil, "account")
			}
			errors <- err
		}(i)
	}
	for range 20 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	store, _ := NewFileStore(path, "test")
	for i := range 20 {
		if _, err := store.Get(t.Context(), fmt.Sprint(i), ProviderGCP); err != nil {
			t.Fatalf("lost concurrent connection %d: %v", i, err)
		}
	}
}
