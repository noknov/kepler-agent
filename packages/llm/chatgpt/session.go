// Package chatgpt manages the operator's official Sign in with ChatGPT session.
package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const Issuer = "https://auth.openai.com"
const Resource = "https://api.openai.com/v1"
const tokenEndpoint = Issuer + "/api/accounts/oauth/token"
const planScope = "chatgpt.tokens.use.direct"

// Session stays on the operator's host. Never expose it through gateway bootstrap.
type Session struct {
	ClientID     string    `json:"client_id"`
	HostID       string    `json:"ext_agent_host_id"`
	Subject      string    `json:"subject"`
	Email        string    `json:"email"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token"`
	Scope        string    `json:"scope"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func DefaultPath() string {
	if p := os.Getenv("CHATGPT_CREDENTIALS_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "kepler-agent", "chatgpt", "session.json")
}

func Load(path string) (Session, error) {
	var s Session
	info, err := os.Stat(path)
	if err != nil {
		return s, fmt.Errorf("ChatGPT session unavailable; run kepler-agent chatgpt login: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return s, errors.New("ChatGPT session must be an owner-only regular file (0600)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(data, &s); err != nil {
		return s, errors.New("invalid ChatGPT session file")
	}
	if s.ClientID == "" || s.ClientID == "dynamic_agent_client" || s.HostID == "" || s.Subject == "" || s.AccessToken == "" || s.RefreshToken == "" || s.ExpiresAt.IsZero() || !hasScope(s.Scope, planScope) || !hasScope(s.Scope, "resource.invoke") {
		return s, errors.New("incomplete ChatGPT plan session; run kepler-agent chatgpt login")
	}
	return s, nil
}

func save(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func random() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func hasScope(scope, want string) bool {
	for _, s := range strings.Fields(scope) {
		if s == want {
			return true
		}
	}
	return false
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func exchange(ctx context.Context, client *http.Client, endpoint string, form url.Values) (tokenResponse, error) {
	var t tokenResponse
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return t, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return t, errors.New("ChatGPT token exchange could not connect")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return t, fmt.Errorf("ChatGPT token exchange failed (HTTP %d); reauthorize if the grant has expired or been revoked", resp.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return t, errors.New("invalid ChatGPT token response")
	}
	if t.AccessToken == "" || t.ExpiresIn <= 0 || !strings.EqualFold(t.TokenType, "Bearer") {
		return t, errors.New("incomplete ChatGPT token response")
	}
	return t, nil
}

// Login starts a loopback callback before printing the official authorization URL.
// The existing registration stays active until the new identity is verified.
func Login(ctx context.Context, path string, out io.Writer) error {
	if path == "" {
		return errors.New("credential file path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	unlock, err := lock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	var old Session
	if _, err := os.Stat(path); err == nil {
		old, err = Load(path)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	hostID := old.HostID
	if hostID == "" {
		hostPath := filepath.Join(filepath.Dir(path), "host.json")
		var host struct {
			ID string `json:"id"`
		}
		data, err := os.ReadFile(hostPath)
		if err == nil {
			if json.Unmarshal(data, &host) != nil || host.ID == "" {
				return errors.New("invalid ChatGPT host file")
			}
			hostID = host.ID
		} else if !os.IsNotExist(err) {
			return err
		} else {
			b := make([]byte, 16)
			if _, err = rand.Read(b); err != nil {
				return err
			}
			b[6] = (b[6] & 15) | 64
			b[8] = (b[8] & 63) | 128
			hostID = fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
			host.ID = hostID
			if err = save(hostPath, host); err != nil {
				return err
			}
		}
	}
	state, err := random()
	if err != nil {
		return err
	}
	nonce, err := random()
	if err != nil {
		return err
	}
	verifier, err := random()
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(verifier))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	callback := "http://" + listener.Addr().String() + "/auth/callback"
	clientID := old.ClientID
	if clientID == "" {
		clientID = "dynamic_agent_client"
	}
	params := url.Values{"client_id": {clientID}, "ext_agent_host_id": {hostID}, "response_type": {"code"}, "redirect_uri": {callback}, "scope": {"openid profile email offline_access resource.invoke " + planScope}, "resource": {Resource}, "state": {state}, "nonce": {nonce}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}}
	if old.ClientID == "" {
		params.Set("agent_name_hint", "kepler-agent")
	}
	// Avoid printing an authorization URL containing an ID token hint.
	type result struct {
		code, id string
		err      error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "Invalid OAuth state", 400)
			return
		}
		res := result{code: q.Get("code"), id: q.Get("client_id")}
		if q.Get("error") != "" {
			res.err = errors.New("ChatGPT authorization declined")
		}
		if old.ClientID != "" {
			if res.id != "" && res.id != old.ClientID {
				res.err = errors.New("ChatGPT registration mismatch")
			}
			res.id = old.ClientID
		}
		if res.err == nil && (res.code == "" || res.id == "" || res.id == "dynamic_agent_client") {
			res.err = errors.New("incomplete ChatGPT authorization callback")
		}
		select {
		case done <- res:
			fmt.Fprintln(w, "Authorization received. Return to your terminal to check the result.")
		default:
			http.Error(w, "Authorization already received", 409)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case done <- result{err: err}:
			default:
			}
		}
	}()
	fmt.Fprintf(out, "Continue with ChatGPT: open this URL in your browser:\n%s/api/accounts/authorize?%s\n", Issuer, params.Encode())
	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if res.err != nil {
		return res.err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	t, err := exchange(ctx, client, tokenEndpoint, url.Values{"grant_type": {"authorization_code"}, "client_id": {res.id}, "code": {res.code}, "code_verifier": {verifier}, "redirect_uri": {callback}, "resource": {Resource}})
	if err != nil {
		return err
	}
	if !hasScope(t.Scope, planScope) || !hasScope(t.Scope, "resource.invoke") || t.RefreshToken == "" {
		return errors.New("ChatGPT plan permission or renewable session was not granted")
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), Issuer)
	if err != nil {
		return errors.New("could not discover OpenAI identity provider")
	}
	subject, email, err := validateIdentity(oidc.ClientContext(ctx, client), provider.Verifier(&oidc.Config{ClientID: res.id}), t.IDToken, nonce, old.Subject)
	if err != nil {
		return errors.New("ChatGPT ID token validation failed")
	}
	session := Session{ClientID: res.id, HostID: hostID, Subject: subject, Email: email, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, IDToken: t.IDToken, Scope: t.Scope, ExpiresAt: time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)}
	if err = save(path, session); err != nil {
		return err
	}
	fmt.Fprintf(out, "ChatGPT plan authorized for %s. Session saved to %s\n", email, path)
	return nil
}

// Transport refreshes the single operator session under a cross-process lock.
// It never sends OAuth credentials to an arbitrary base URL.
type Transport struct {
	Path        string
	Base        http.RoundTripper
	tokenURL    string
	tokenClient *http.Client
}

func (t *Transport) token(ctx context.Context) (string, error) {
	unlock, err := lock(ctx, t.Path+".lock")
	if err != nil {
		return "", err
	}
	defer unlock()
	s, err := Load(t.Path)
	if err != nil {
		return "", err
	}
	if time.Until(s.ExpiresAt) > time.Minute {
		return s.AccessToken, nil
	}
	endpoint := t.tokenURL
	if endpoint == "" {
		endpoint = tokenEndpoint
	}
	client := t.tokenClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	next, err := exchange(ctx, client, endpoint, url.Values{"grant_type": {"refresh_token"}, "client_id": {s.ClientID}, "refresh_token": {s.RefreshToken}, "resource": {Resource}})
	if err != nil {
		return "", err
	}
	if next.Scope != "" {
		if !hasScope(next.Scope, planScope) || !hasScope(next.Scope, "resource.invoke") {
			return "", errors.New("ChatGPT plan permission revoked")
		}
		s.Scope = next.Scope
	}
	s.AccessToken = next.AccessToken
	s.ExpiresAt = time.Now().Add(time.Duration(next.ExpiresIn) * time.Second)
	if next.RefreshToken != "" {
		s.RefreshToken = next.RefreshToken
	}
	if next.IDToken != "" {
		s.IDToken = next.IDToken
	}
	if err = save(t.Path, s); err != nil {
		return "", fmt.Errorf("persist renewed ChatGPT session: %w", err)
	}
	return s.AccessToken, nil
}
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "api.openai.com" || (req.URL.Path != "/v1/responses" && req.URL.Path != "/v1/models") {
		return nil, errors.New("ChatGPT credentials may only be used with official models and Responses endpoints")
	}
	token, err := t.token(req.Context())
	if err != nil {
		return nil, err
	}
	copy := req.Clone(req.Context())
	copy.Header = req.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+token)
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(copy)
}

func Models(ctx context.Context, path string, out io.Writer) error {
	client := &http.Client{Timeout: 30 * time.Second, Transport: &Transport{Path: path}}
	req, err := http.NewRequestWithContext(ctx, "GET", Resource+"/models", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("ChatGPT model catalog failed (HTTP %d)", resp.StatusCode)
	}
	var catalog struct {
		Models []struct {
			Slug       string `json:"slug"`
			Name       string `json:"display_name"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&catalog); err != nil {
		return err
	}
	for _, m := range catalog.Models {
		if m.Visibility == "list" {
			fmt.Fprintf(out, "%s\t%s\n", m.Slug, m.Name)
		}
	}
	return nil
}

func validateIdentity(ctx context.Context, verifier *oidc.IDTokenVerifier, token, nonce, oldSubject string) (string, string, error) {
	id, err := verifier.Verify(ctx, token)
	if err != nil {
		return "", "", errors.New("ChatGPT ID token validation failed")
	}
	if id.Nonce != nonce || id.Subject == "" || (oldSubject != "" && oldSubject != id.Subject) {
		return "", "", errors.New("ChatGPT identity or nonce mismatch")
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err = id.Claims(&claims); err != nil {
		return "", "", errors.New("invalid ChatGPT identity claims")
	}
	return id.Subject, claims.Email, nil
}
