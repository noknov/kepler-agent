package chatgpt

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/coreos/go-oidc/v3/oidc"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func session(t *testing.T, expiry time.Time) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.json")
	if err := save(p, Session{ClientID: "oaiapp_test", HostID: "urn:uuid:host", Subject: "operator", AccessToken: "old-access", RefreshToken: "old-refresh", Scope: planScope + " resource.invoke", ExpiresAt: expiry}); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestConcurrentRefreshPersistsRotatingToken(t *testing.T) {
	p := session(t, time.Now().Add(-time.Minute))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "oaiapp_test" || r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("resource") != Resource || r.Form.Get("scope") != "" {
			t.Error("incorrect refresh grant")
		}
		fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr := Transport{Path: p, tokenURL: server.URL}
			token, err := tr.token(context.Background())
			if err != nil || token != "new-access" {
				t.Errorf("refresh: %v", err)
			}
		}()
	}
	wg.Wait()
	if requests.Load() != 1 {
		t.Fatalf("refreshes = %d", requests.Load())
	}
	s, err := Load(p)
	if err != nil || s.RefreshToken != "new-refresh" {
		t.Fatalf("rotation not saved: %v", err)
	}
}
func TestFailedRefreshKeepsSessionAndRedactsBody(t *testing.T) {
	p := session(t, time.Now().Add(-time.Minute))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400); fmt.Fprint(w, "secret-token") }))
	defer server.Close()
	_, err := (&Transport{Path: p, tokenURL: server.URL}).token(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unsafe error: %v", err)
	}
	s, err := Load(p)
	if err != nil || s.RefreshToken != "old-refresh" {
		t.Fatal("failed refresh replaced session")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTransportRestrictsCredentialDestination(t *testing.T) {
	p := session(t, time.Now().Add(time.Hour))
	var calls int
	tr := &Transport{Path: p, Base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer old-access" {
			t.Error("missing operator token")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	for _, u := range []string{"https://evil.example/v1/responses", "https://api.openai.com/v1/chat/completions", "http://api.openai.com/v1/responses"} {
		req, _ := http.NewRequest("POST", u, nil)
		if _, err := tr.RoundTrip(req); err == nil {
			t.Errorf("accepted %s", u)
		}
	}
	req, _ := http.NewRequest("POST", Resource+"/responses", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || req.Header.Get("Authorization") != "" {
		t.Fatal("transport leaked credential or modified caller request")
	}
}
func TestIdentityVerification(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig", "n": enc(key.N.Bytes()), "e": enc(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	defer server.Close()
	verifier := oidc.NewVerifier(Issuer, oidc.NewRemoteKeySet(context.Background(), server.URL), &oidc.Config{ClientID: "oaiapp_test"})
	for _, tc := range []struct {
		name, aud, nonce, sub string
		expired, badsig       bool
		valid                 bool
	}{
		{name: "valid", aud: "oaiapp_test", nonce: "nonce", sub: "operator", valid: true},
		{name: "wrong audience", aud: "other", nonce: "nonce", sub: "operator"},
		{name: "wrong nonce", aud: "oaiapp_test", nonce: "other", sub: "operator"},
		{name: "wrong account", aud: "oaiapp_test", nonce: "nonce", sub: "other"},
		{name: "expired", aud: "oaiapp_test", nonce: "nonce", sub: "operator", expired: true},
		{name: "bad signature", aud: "oaiapp_test", nonce: "nonce", sub: "operator", badsig: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expiry := time.Now().Add(time.Hour).Unix()
			if tc.expired {
				expiry = time.Now().Add(-time.Hour).Unix()
			}
			payload, _ := json.Marshal(map[string]any{"iss": Issuer, "aud": tc.aud, "nonce": tc.nonce, "sub": tc.sub, "exp": expiry, "iat": time.Now().Unix(), "email": "operator@example.test"})
			unsigned := enc([]byte(`{"alg":"RS256","kid":"test"}`)) + "." + enc(payload)
			hash := sha256.Sum256([]byte(unsigned))
			sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
			if err != nil {
				t.Fatal(err)
			}
			if tc.badsig {
				sig[0] ^= 1
			}
			_, _, err = validateIdentity(context.Background(), verifier, unsigned+"."+enc(sig), "nonce", "operator")
			if (err == nil) != tc.valid {
				t.Fatalf("identity verification = %v", err)
			}
		})
	}
}
