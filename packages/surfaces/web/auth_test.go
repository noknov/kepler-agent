package web

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/safety"
)

type memoryWebStore struct {
	states        map[string]AuthState
	sessions      map[string]BrowserSession
	conversations map[string]Conversation
}

func newMemoryWebStore() *memoryWebStore {
	return &memoryWebStore{states: map[string]AuthState{}, sessions: map[string]BrowserSession{}, conversations: map[string]Conversation{}}
}

func hashKey(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }

func (s *memoryWebStore) CreateAuthState(_ context.Context, hash []byte, state AuthState) error {
	s.states[hashKey(hash)] = state
	return nil
}
func (s *memoryWebStore) ConsumeAuthState(_ context.Context, hash []byte, now time.Time) (AuthState, error) {
	key := hashKey(hash)
	state, ok := s.states[key]
	if !ok || !state.ExpiresAt.After(now) {
		return AuthState{}, ErrNotFound
	}
	delete(s.states, key)
	return state, nil
}
func (s *memoryWebStore) CreateSession(_ context.Context, hash []byte, session BrowserSession) error {
	s.sessions[hashKey(hash)] = session
	return nil
}
func (s *memoryWebStore) GetSession(_ context.Context, hash []byte, now time.Time) (BrowserSession, error) {
	session, ok := s.sessions[hashKey(hash)]
	if !ok || !session.ExpiresAt.After(now) {
		return BrowserSession{}, ErrNotFound
	}
	return session, nil
}
func (s *memoryWebStore) DeleteSession(_ context.Context, hash []byte) error {
	delete(s.sessions, hashKey(hash))
	return nil
}
func (s *memoryWebStore) CreateConversation(_ context.Context, _ Identity, conversation Conversation) error {
	s.conversations[conversation.ID] = conversation
	return nil
}
func (s *memoryWebStore) GetConversation(_ context.Context, _ Identity, id string) (Conversation, error) {
	conversation, ok := s.conversations[id]
	if !ok {
		return Conversation{}, ErrNotFound
	}
	return conversation, nil
}
func (s *memoryWebStore) ListConversations(_ context.Context, _ Identity, _ bool, limit, offset int) ([]Conversation, error) {
	result := make([]Conversation, 0, len(s.conversations))
	for _, conversation := range s.conversations {
		result = append(result, conversation)
	}
	if offset >= len(result) {
		return nil, nil
	}
	end := offset + limit
	if end > len(result) {
		end = len(result)
	}
	return result[offset:end], nil
}
func (s *memoryWebStore) RenameConversation(_ context.Context, _ Identity, id, title string) error {
	conversation, ok := s.conversations[id]
	if !ok {
		return ErrNotFound
	}
	conversation.Title = title
	s.conversations[id] = conversation
	return nil
}
func (s *memoryWebStore) ArchiveConversation(_ context.Context, _ Identity, id string, archived bool) error {
	conversation, ok := s.conversations[id]
	if !ok {
		return ErrNotFound
	}
	if archived {
		now := time.Now().UTC()
		conversation.ArchivedAt = &now
	} else {
		conversation.ArchivedAt = nil
	}
	s.conversations[id] = conversation
	return nil
}
func (s *memoryWebStore) TouchConversation(_ context.Context, _ Identity, id, title string) error {
	conversation, ok := s.conversations[id]
	if !ok {
		return ErrNotFound
	}
	if conversation.Title == "New conversation" {
		conversation.Title = title
	}
	s.conversations[id] = conversation
	return nil
}

type fakeIdentityProvider struct{ identity Identity }

func (p fakeIdentityProvider) Name() string { return "slack" }
func (p fakeIdentityProvider) AuthorizationURL(_ context.Context, state, nonce, challenge string) (string, error) {
	return "https://slack.example/authorize?state=" + url.QueryEscape(state) + "&nonce=" + url.QueryEscape(nonce) + "&challenge=" + url.QueryEscape(challenge), nil
}
func (p fakeIdentityProvider) Exchange(_ context.Context, code, verifier, nonce string) (Identity, error) {
	if code == "" || verifier == "" || nonce == "" {
		return Identity{}, errors.New("missing exchange input")
	}
	return p.identity, nil
}

func TestAuthFlowCreatesAllowlistedOpaqueSession(t *testing.T) {
	store := newMemoryWebStore()
	identity := Identity{Provider: "slack", TenantID: "T1", SubjectID: "U1", DisplayName: "Ada"}
	auth, err := NewAuthService(store, "https://kepler.example", strings.Repeat("s", 32), time.Hour, []string{"U1"}, fakeIdentityProvider{identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	auth.Clock = func() time.Time { return now }

	startRequest := httptest.NewRequest(http.MethodGet, "https://kepler.example/auth/slack/start?return_to=%2F", nil)
	startResponse := httptest.NewRecorder()
	auth.HandleStart(startResponse, startRequest, "slack")
	if startResponse.Code != http.StatusFound {
		t.Fatalf("start status = %d", startResponse.Code)
	}
	location, _ := url.Parse(startResponse.Header().Get("Location"))
	state := location.Query().Get("state")
	if state == "" || location.Query().Get("challenge") == "" {
		t.Fatalf("authorization URL missing state or PKCE challenge: %s", location)
	}
	var authCookie *http.Cookie
	for _, cookie := range startResponse.Result().Cookies() {
		if cookie.Name == "__Host-kepler_oauth" {
			authCookie = cookie
		}
	}
	if authCookie == nil || !authCookie.HttpOnly || !authCookie.Secure {
		t.Fatalf("OAuth transaction cookie is not hardened: %#v", startResponse.Result().Cookies())
	}

	callback := httptest.NewRequest(http.MethodGet, "https://kepler.example/auth/slack/callback?code=ok&state="+url.QueryEscape(state), nil)
	callback.AddCookie(authCookie)
	callbackResponse := httptest.NewRecorder()
	auth.HandleCallback(callbackResponse, callback, "slack")
	if callbackResponse.Code != http.StatusSeeOther {
		t.Fatalf("callback status = %d location=%s", callbackResponse.Code, callbackResponse.Header().Get("Location"))
	}
	cookies := callbackResponse.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == "__Host-kepler_session" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie is not hardened: %#v", cookies)
	}
	if len(store.sessions) != 1 {
		t.Fatalf("stored sessions = %d", len(store.sessions))
	}
	for key := range store.sessions {
		if strings.Contains(key, sessionCookie.Value) {
			t.Fatal("raw browser token was stored")
		}
	}
}

func TestAuthCallbackRequiresTheStartingBrowser(t *testing.T) {
	store := newMemoryWebStore()
	identity := Identity{Provider: "slack", TenantID: "T1", SubjectID: "U1"}
	auth, err := NewAuthService(store, "https://kepler.example", strings.Repeat("s", 32), time.Hour, []string{"U1"}, fakeIdentityProvider{identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	start := httptest.NewRecorder()
	auth.HandleStart(start, httptest.NewRequest(http.MethodGet, "https://kepler.example/auth/slack/start", nil), "slack")
	location, _ := url.Parse(start.Header().Get("Location"))
	callback := httptest.NewRecorder()
	auth.HandleCallback(callback, httptest.NewRequest(http.MethodGet, "https://kepler.example/auth/slack/callback?code=ok&state="+url.QueryEscape(location.Query().Get("state")), nil), "slack")
	if callback.Code != http.StatusSeeOther || !strings.Contains(callback.Header().Get("Location"), "invalid_response") || len(store.sessions) != 0 {
		t.Fatalf("unbound callback status=%d location=%q sessions=%d", callback.Code, callback.Header().Get("Location"), len(store.sessions))
	}
}

func TestRequireRejectsMutationWithoutOriginAndCSRF(t *testing.T) {
	store := newMemoryWebStore()
	auth, _ := NewAuthService(store, "https://kepler.example", strings.Repeat("k", 32), time.Hour, []string{"U1"})
	token := "opaque-session-token"
	store.sessions[hashKey(HashOpaque(token))] = BrowserSession{Identity: Identity{Provider: "slack", TenantID: "T1", SubjectID: "U1"}, ExpiresAt: time.Now().Add(time.Hour)}
	handler := auth.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	request := httptest.NewRequest(http.MethodPost, "https://kepler.example/api/action", strings.NewReader("{}"))
	request.AddCookie(&http.Cookie{Name: "__Host-kepler_session", Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("request without CSRF status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "https://kepler.example/api/action", strings.NewReader("{}"))
	request.AddCookie(&http.Cookie{Name: "__Host-kepler_session", Value: token})
	request.Header.Set("Origin", "https://kepler.example")
	request.Header.Set("X-CSRF-Token", auth.csrf(token))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("verified mutation status = %d", response.Code)
	}
}

func TestOIDCVerifierChecksSignatureAudienceAndNonce(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	provider := &OIDCProvider{ProviderName: "slack", ClientID: "client-1", Clock: func() time.Time { return now }}
	provider.discovery = oidcDiscovery{Issuer: "https://slack.com"}
	provider.keys = map[string]*rsa.PublicKey{"key-1": &key.PublicKey}
	provider.loadedAt = now
	claims := map[string]any{
		"iss": "https://slack.com", "aud": "client-1", "exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce-1",
		"https://slack.com/team_id": "T1", "https://slack.com/user_id": "U1", "name": "Ada",
	}
	token := signToken(t, key, "key-1", claims)
	verified, err := provider.verifyIDToken(context.Background(), token, provider.discovery, "nonce-1")
	if err != nil || verified.UserID != "U1" || verified.TeamID != "T1" {
		t.Fatalf("verify = %#v, %v", verified, err)
	}
	if _, err := provider.verifyIDToken(context.Background(), token, provider.discovery, "wrong"); err == nil {
		t.Fatal("wrong nonce was accepted")
	}
	claims["aud"] = "another-client"
	if _, err := provider.verifyIDToken(context.Background(), signToken(t, key, "key-1", claims), provider.discovery, "nonce-1"); err == nil {
		t.Fatal("wrong audience was accepted")
	}
}

func TestProjectEventHidesApprovalContinuationAndRedactsSecrets(t *testing.T) {
	hidden := model.TextMessage(model.RoleUser, "Continue")
	hidden.ID = "approval-continuation:turn"
	if _, ok := ProjectEvent(transcript.Event{Type: transcript.UserInput, Message: &hidden}, safety.Redactor{}); ok {
		t.Fatal("approval continuation leaked into the UI")
	}
	message := model.TextMessage(model.RoleAssistant, "openai_api_key=secret-value")
	view, ok := ProjectEvent(transcript.Event{ID: "1", SessionID: "web_1", Type: transcript.AssistantMessage, Message: &message}, safety.Redactor{})
	if !ok || strings.Contains(view.Text, "secret-value") || !strings.Contains(view.Text, "[redacted]") {
		t.Fatalf("projected event was not redacted: %#v", view)
	}
}

func TestProjectEventProjectsPlanUpdate(t *testing.T) {
	view, ok := ProjectEvent(transcript.Event{
		ID: "plan-1", SessionID: "web_1", TurnID: "turn-1", Type: transcript.PlanUpdated,
		Plan: &tool.PlanUpdate{
			Explanation: "Investigating auth",
			Items: []tool.PlanItem{
				{ID: "inspect", Task: "Inspect instagram-service config", Status: "completed"},
				{ID: "trace", Task: "Trace IDP token client", Status: "in_progress"},
			},
		},
	}, safety.Redactor{})
	if !ok || view.Kind != "plan" || view.Plan == nil || len(view.Plan.Items) != 2 {
		t.Fatalf("projected plan event = %#v, ok=%v", view, ok)
	}
	if view.Plan.Items[1].Task != "Trace IDP token client" {
		t.Fatalf("plan items = %#v", view.Plan.Items)
	}
}

func TestCollapseClientEventsKeepsLatestToolStatus(t *testing.T) {
	events := []ClientEvent{
		{ID: "1", Sequence: 1, TurnID: "turn-1", Kind: "tool", Tool: "code-search", ToolCallID: "call-1", Status: "running"},
		{ID: "2", Sequence: 2, TurnID: "turn-1", Kind: "tool", Tool: "code-search", ToolCallID: "call-1", Status: "completed"},
		{ID: "3", Sequence: 3, TurnID: "turn-1", Kind: "message", Role: "assistant", Text: "done"},
	}
	collapsed := CollapseClientEvents(events)
	if len(collapsed) != 2 {
		t.Fatalf("collapsed len = %d, want 2: %#v", len(collapsed), collapsed)
	}
	if collapsed[0].Status != "completed" {
		t.Fatalf("tool status = %q, want completed", collapsed[0].Status)
	}
}

func TestEventHubReplaysCurrentStreamSnapshotOnSubscribe(t *testing.T) {
	hub := NewEventHub(safety.Redactor{})
	hub.Publish(context.Background(), transcript.Event{
		ID: "delta-1", SessionID: "web_1", TurnID: "turn-1", Type: transcript.ModelStreamed,
		Model: &model.StreamEvent{Type: model.StreamTextDelta, Text: "partial response\n"}, Timestamp: time.Now().UTC(),
	})
	snapshots, _, cancel := hub.Subscribe("web_1")
	defer cancel()
	if len(snapshots) != 1 || !snapshots[0].Replace || snapshots[0].Text != "partial response\n" {
		t.Fatalf("stream snapshot = %#v", snapshots)
	}
}

func TestSafeReturnToRejectsExternalRedirects(t *testing.T) {
	for _, value := range []string{"https://evil.example", "//evil.example/path", "/ok\r\nLocation:https://evil.example"} {
		if got := safeReturnTo(value); got != "/" {
			t.Fatalf("safeReturnTo(%q) = %q", value, got)
		}
	}
	if got := safeReturnTo("/conversation?id=1"); got != "/conversation?id=1" {
		t.Fatalf("valid return path = %q", got)
	}
}

func TestHandlerServesBrandedPageAndProtectsChatAPI(t *testing.T) {
	store := newMemoryWebStore()
	auth, err := NewAuthService(store, "https://kepler.example", strings.Repeat("s", 32), time.Hour, []string{"U1"}, fakeIdentityProvider{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(auth, nil, store, "")
	if err != nil {
		t.Fatal(err)
	}
	handler.Brand = Brand{Name: "斗包"}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "https://kepler.example/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Continue with Slack") {
		t.Fatalf("page status/body = %d %q", page.Code, page.Body.String())
	}
	if !strings.Contains(page.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || page.Header().Get("Strict-Transport-Security") == "" {
		t.Fatalf("security headers missing: %#v", page.Header())
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "https://kepler.example/assets/app.js", nil))
	if asset.Code != http.StatusOK || !strings.Contains(asset.Body.String(), "import { $, state }") {
		t.Fatalf("asset response = %d %q", asset.Code, asset.Body.String())
	}

	brand := httptest.NewRecorder()
	handler.ServeHTTP(brand, httptest.NewRequest(http.MethodGet, "https://kepler.example/api/brand", nil))
	if brand.Code != http.StatusOK || !strings.Contains(brand.Body.String(), "斗包") {
		t.Fatalf("brand response = %d %q", brand.Code, brand.Body.String())
	}

	api := httptest.NewRecorder()
	handler.ServeHTTP(api, httptest.NewRequest(http.MethodGet, "https://kepler.example/api/conversations", nil))
	if api.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API status = %d", api.Code)
	}
}

func signToken(t *testing.T, key *rsa.PrivateKey, keyID string, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": keyID, "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}
