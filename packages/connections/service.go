package connections

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
)

type Config struct {
	PublicBaseURL string
	SecretKey     string
	Slack         SlackOAuthConfig
	GitHub        GitHubOAuthConfig
	ClickStack    ClickStackOAuthConfig
	GCP           GCPOAuthConfig
	Notion        NotionOAuthConfig
}

type SlackOAuthConfig struct {
	ClientID     string
	ClientSecret string
}

type GitHubOAuthConfig struct {
	ClientID     string
	ClientSecret string
	APIBaseURL   string
}

func (c Config) SlackEnabled() bool {
	return strings.TrimSpace(c.Slack.ClientID) != "" && strings.TrimSpace(c.Slack.ClientSecret) != ""
}

func (c Config) GitHubEnabled() bool {
	return strings.TrimSpace(c.GitHub.ClientID) != "" && strings.TrimSpace(c.GitHub.ClientSecret) != ""
}

func (c Config) ClickStackEnabled() bool {
	// The shared callback and signing key are the deployment prerequisites.
	// The endpoint itself may be supplied by a connection's declared field, so
	// a user can add a custom ClickStack server without an operator pre-filling
	// one global URL.
	return strings.TrimSpace(c.PublicBaseURL) != "" && strings.TrimSpace(c.SecretKey) != ""
}

func (c ClickStackOAuthConfig) Configured() bool {
	return strings.TrimSpace(c.ServiceID) != "" || customClickStackURL(c.MCPURL)
}

func (c ClickStackOAuthConfig) OAuthMode() bool {
	return strings.TrimSpace(c.ServiceID) != ""
}

func customClickStackURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw != "" && raw != clickstackDefaultMCPURL
}

func (c Config) GCPEnabled() bool {
	return strings.TrimSpace(c.PublicBaseURL) != "" && c.GCP.Enabled()
}

func (c Config) NotionEnabled() bool {
	return strings.TrimSpace(c.PublicBaseURL) != "" && c.Notion.Configured()
}

func (c Config) OAuthEnabled() bool {
	return c.SlackEnabled() || c.GitHubEnabled() || c.ClickStackEnabled() || c.GCPEnabled() || c.NotionEnabled()
}

// ConnectionChangedHandler runs after integration credentials change in storage,
// including a successful OAuth callback or clearing a token the remote rejected.
type ConnectionChangedHandler func(ctx context.Context, userID, provider string) error

type Service struct {
	Store               Store
	Config              Config
	Continuations       ContinuationStore
	OnConnectionChanged ConnectionChangedHandler
	state               *serviceState
}

// ListConnections returns every user-owned connection instance. It is the
// preferred surface API; Store.List remains as a compatibility alias.
func (s Service) ListConnections(ctx context.Context, userID string) ([]Connection, error) {
	if s.Store == nil {
		return nil, ErrNotConnected
	}
	if store, ok := instanceStore(s.Store); ok {
		return store.ListInstances(ctx, userID)
	}
	return s.Store.List(ctx, userID)
}

func (s Service) GetConnection(ctx context.Context, userID, provider, instanceID string) (Connection, error) {
	if s.Store == nil {
		return Connection{}, ErrNotConnected
	}
	if store, ok := instanceStore(s.Store); ok {
		return store.GetInstance(ctx, userID, provider, connectionInstanceID(instanceID))
	}
	if connectionInstanceID(instanceID) != DefaultInstanceID {
		return Connection{}, ErrNotConnected
	}
	return s.Store.Get(ctx, userID, provider)
}

func (s Service) upsertToken(ctx context.Context, userID, provider, instanceID, label, token string, scopes []string, account string, metadata map[string]string) error {
	instanceID = connectionInstanceID(instanceID)
	if store, ok := instanceStore(s.Store); ok {
		return store.UpsertTokenInstance(ctx, userID, provider, instanceID, label, token, scopes, account, metadata)
	}
	if connectionInstanceID(instanceID) != DefaultInstanceID {
		return fmt.Errorf("connection store does not support multiple instances")
	}
	return s.Store.UpsertToken(ctx, userID, provider, token, scopes, account)
}

func (s Service) deleteConnection(ctx context.Context, userID, provider, instanceID string) error {
	instanceID = connectionInstanceID(instanceID)
	if store, ok := instanceStore(s.Store); ok {
		return store.DeleteInstance(ctx, userID, provider, instanceID)
	}
	if connectionInstanceID(instanceID) != DefaultInstanceID {
		return ErrNotConnected
	}
	return s.Store.Delete(ctx, userID, provider)
}

// serviceState holds mutable OAuth state behind a pointer so Service remains a
// safe, lightweight dependency value. Surface adapters may carry Service by
// value, but must never copy synchronization primitives themselves.
type serviceState struct {
	mu                sync.Mutex
	clickstackOAuth   *clickstackOAuth
	gcpOAuth          *gcpOAuth
	notionOAuth       *notionOAuth
	clickstackRefresh sync.Map
	gcpRefresh        sync.Map
	notionRefresh     sync.Map
}

func (s *Service) mutableState() *serviceState {
	if s.state == nil {
		s.state = &serviceState{}
	}
	return s.state
}

func (s *Service) clickstack() *clickstackOAuth {
	state := s.mutableState()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.clickstackOAuth == nil {
		state.clickstackOAuth = newClickStackOAuth(s.Config.ClickStack)
	}
	return state.clickstackOAuth
}

func (s *Service) gcp() *gcpOAuth {
	state := s.mutableState()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.gcpOAuth == nil {
		state.gcpOAuth = newGCPOAuth(s.Config.GCP)
	}
	return state.gcpOAuth
}

func (s *Service) notion() *notionOAuth {
	state := s.mutableState()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.notionOAuth == nil {
		state.notionOAuth = newNotionOAuth(s.Config.Notion)
	}
	return state.notionOAuth
}

func (s Service) ProviderOAuthEnabled(provider string) bool {
	switch provider {
	case ProviderSlack:
		return s.Config.SlackEnabled()
	case ProviderGitHub:
		return s.Config.GitHubEnabled()
	case ProviderClickStack:
		return s.Config.ClickStackEnabled()
	case ProviderGCP:
		return s.Config.GCPEnabled()
	case ProviderNotion:
		return s.Config.NotionEnabled()
	default:
		return false
	}
}

func (s Service) HandleStart(w http.ResponseWriter, r *http.Request, provider, state string) {
	if state == "" {
		http.Error(w, "state is required", http.StatusBadRequest)
		return
	}
	_, storedProvider, meta, err := s.Store.PeekOAuthState(r.Context(), state)
	if err != nil {
		http.Error(w, "invalid oauth state: not found or expired; reopen App Home and connect again", http.StatusBadRequest)
		return
	}
	if storedProvider != provider {
		http.Error(w, "invalid oauth state: provider mismatch", http.StatusBadRequest)
		return
	}
	s.redirectOAuthAuthorize(w, r, provider, state, meta)
}

func (s Service) HandleCallback(w http.ResponseWriter, r *http.Request, provider string) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	userID, storedProvider, meta, err := s.Store.ConsumeOAuthState(r.Context(), state)
	if err != nil {
		http.Error(w, "invalid oauth state: not found or expired; reopen App Home and connect again", http.StatusBadRequest)
		return
	}
	if storedProvider != provider {
		http.Error(w, "invalid oauth state: provider mismatch", http.StatusBadRequest)
		return
	}
	switch provider {
	case ProviderSlack:
		token, account, scopes, err := s.exchangeSlack(r.Context(), code)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := s.upsertToken(r.Context(), userID, provider, meta.InstanceID, meta.Label, token, scopes, account, meta.Metadata); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case ProviderGitHub:
		token, account, scopes, err := s.exchangeGitHub(r.Context(), code)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := s.upsertToken(r.Context(), userID, provider, meta.InstanceID, meta.Label, token, scopes, account, meta.Metadata); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case ProviderClickStack:
		redirectURI := s.callbackURL(provider)
		oauth := s.clickstackForMeta(meta)
		clientID, err := oauth.ensureClient(r.Context(), redirectURI)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		response, err := oauth.exchange(r.Context(), code, meta.CodeVerifier, redirectURI, clientID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := s.storeClickStackBundleInstance(r.Context(), userID, meta.InstanceID, meta.Label, meta.Metadata, clientID, redirectURI, response, "", response.Scopes); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case ProviderGCP:
		if !s.Config.GCPEnabled() {
			http.Error(w, "gcp oauth is not configured", http.StatusServiceUnavailable)
			return
		}
		response, err := s.gcp().exchange(r.Context(), code, s.callbackURL(provider))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := s.storeGCPBundleInstance(r.Context(), userID, meta.InstanceID, meta.Label, meta.Metadata, response, "", nil); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case ProviderNotion:
		if !s.Config.NotionEnabled() {
			http.Error(w, "notion oauth is not configured", http.StatusServiceUnavailable)
			return
		}
		redirectURI := s.callbackURL(provider)
		clientID, err := s.notion().ensureClient(r.Context(), redirectURI)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		response, err := s.notion().exchange(r.Context(), code, meta.CodeVerifier, redirectURI, clientID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := s.storeNotionBundleInstance(r.Context(), userID, meta.InstanceID, meta.Label, meta.Metadata, clientID, redirectURI, response, "", response.Scopes); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "unsupported provider", http.StatusNotFound)
		return
	}
	s.notifyOAuthCompleted(r.Context(), userID, provider, meta.InstanceID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, connectionCallbackPage(meta.Origin))
}

func connectionCallbackPage(origin string) string {
	message := "You can return to Slack and continue."
	switch strings.TrimSpace(origin) {
	case "app_home":
		message = "Return to the Slack App Home to see this connection and continue."
	case "chat":
		message = "Return to the Slack conversation; the waiting request will continue automatically."
	}
	return "<!doctype html><html><body><h1>Connected</h1><p>" + message + "</p></body></html>"
}

func (s Service) callbackURL(provider string) string {
	return strings.TrimRight(s.Config.PublicBaseURL, "/") + "/oauth/" + url.PathEscape(provider) + "/callback"
}

func (s Service) clickstackForMeta(meta OAuthStateMeta) *clickstackOAuth {
	metadata := meta.Metadata
	if len(metadata) == 0 {
		return s.clickstack()
	}
	cfg := s.Config.ClickStack
	if value := strings.TrimSpace(metadata["mcp_url"]); value != "" {
		cfg.MCPURL = value
	}
	if value := strings.TrimSpace(metadata["service_id"]); value != "" {
		cfg.ServiceID = value
	}
	return newClickStackOAuth(cfg)
}

func (s Service) exchangeSlack(ctx context.Context, code string) (token, account string, scopes []string, err error) {
	values := url.Values{
		"client_id":     {s.Config.Slack.ClientID},
		"client_secret": {s.Config.Slack.ClientSecret},
		"code":          {code},
		"redirect_uri":  {s.callbackURL(ProviderSlack)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/oauth.v2.access", strings.NewReader(values.Encode()))
	if err != nil {
		return "", "", nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", nil, err
	}
	defer resp.Body.Close()
	var payload struct {
		OK         bool   `json:"ok"`
		Error      string `json:"error,omitempty"`
		AuthedUser struct {
			ID          string `json:"id"`
			AccessToken string `json:"access_token"`
			Scope       string `json:"scope"`
		} `json:"authed_user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", "", nil, err
	}
	if !payload.OK || payload.AuthedUser.AccessToken == "" {
		if payload.Error != "" {
			return "", "", nil, fmt.Errorf("slack oauth failed: %s", payload.Error)
		}
		return "", "", nil, fmt.Errorf("slack oauth returned an empty user token")
	}
	if payload.AuthedUser.Scope != "" {
		scopes = strings.Split(payload.AuthedUser.Scope, ",")
	}
	return payload.AuthedUser.AccessToken, payload.AuthedUser.ID, scopes, nil
}

func (s Service) exchangeGitHub(ctx context.Context, code string) (token, account string, scopes []string, err error) {
	values := url.Values{
		"client_id":     {s.Config.GitHub.ClientID},
		"client_secret": {s.Config.GitHub.ClientSecret},
		"code":          {code},
		"redirect_uri":  {s.callbackURL(ProviderGitHub)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(values.Encode()))
	if err != nil {
		return "", "", nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", nil, err
	}
	defer resp.Body.Close()
	var payload struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", "", nil, err
	}
	if payload.AccessToken == "" {
		if payload.Error != "" {
			return "", "", nil, fmt.Errorf("github oauth failed: %s", payload.Error)
		}
		return "", "", nil, fmt.Errorf("github oauth returned an empty token")
	}
	account, err = s.githubLogin(ctx, payload.AccessToken)
	if err != nil {
		account = ""
	}
	if payload.Scope != "" {
		scopes = strings.Fields(payload.Scope)
	}
	return payload.AccessToken, account, scopes, nil
}

func (s Service) githubLogin(ctx context.Context, token string) (string, error) {
	base := strings.TrimRight(s.Config.GitHub.APIBaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var payload struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	return payload.Login, nil
}

func (s Service) Required(userID, provider string) error {
	return s.RequiredInstance(userID, provider, DefaultInstanceID)
}

// RequiredInstance is the shared connection gate used by tools. It keeps the
// selected instance in the error contract so chat surfaces can offer the
// exact connection action that was requested.
func (s Service) RequiredInstance(userID, provider, instanceID string) error {
	if _, err := s.tokenForInstance(context.Background(), userID, provider, instanceID); err == nil {
		return nil
	}
	label, metadata := s.connectionActionDetails(context.Background(), userID, provider, instanceID)
	authURL, err := s.ConnectURLForInstance(userID, provider, instanceID, label, metadata)
	title := pluginTitle(provider)
	if strings.TrimSpace(label) != "" {
		title += " · " + strings.TrimSpace(label)
	}
	if err != nil {
		return &RequiredError{Provider: provider, InstanceID: connectionInstanceID(instanceID), Title: title, Metadata: metadata}
	}
	return &RequiredError{Provider: provider, InstanceID: connectionInstanceID(instanceID), Title: title, AuthURL: authURL, Metadata: metadata}
}

func (s Service) connectionActionDetails(ctx context.Context, userID, provider, instanceID string) (string, map[string]string) {
	connection, err := s.GetConnection(ctx, userID, provider, instanceID)
	if err != nil {
		return "", nil
	}
	return connection.Label, cloneMetadata(connection.Metadata)
}

func (s Service) tokenForInstance(ctx context.Context, userID, provider, instanceID string) (string, error) {
	instanceID = connectionInstanceID(instanceID)
	if store, ok := instanceStore(s.Store); ok {
		return store.TokenInstance(ctx, userID, provider, instanceID)
	}
	if connectionInstanceID(instanceID) != DefaultInstanceID {
		return "", ErrNotConnected
	}
	return s.Store.Token(ctx, userID, provider)
}

// Reauthorize returns a connection-required error with a fresh OAuth URL even when
// a token already exists.
func (s Service) Reauthorize(userID, provider string) error {
	return s.ReauthorizeInstance(userID, provider, DefaultInstanceID)
}

func (s Service) ReauthorizeInstance(userID, provider, instanceID string) error {
	label, metadata := s.connectionActionDetails(context.Background(), userID, provider, instanceID)
	authURL, err := s.ConnectURLForInstance(userID, provider, instanceID, label, metadata)
	title := pluginTitle(provider)
	if strings.TrimSpace(label) != "" {
		title += " · " + strings.TrimSpace(label)
	}
	if err != nil {
		return &RequiredError{Provider: provider, InstanceID: connectionInstanceID(instanceID), Title: title, Reauthorize: true, Metadata: metadata}
	}
	return &RequiredError{Provider: provider, InstanceID: connectionInstanceID(instanceID), Title: title, AuthURL: authURL, Reauthorize: true, Metadata: metadata}
}

func ToolResult(err error) (tool.Result, error) {
	var required *RequiredError
	if !AsRequired(err, &required) {
		if err == nil {
			return tool.Result{}, nil
		}
		return tool.Result{}, err
	}
	text := connectionRequiredText(required)
	return tool.Result{
		Content:        []model.Content{{Type: model.ContentText, Text: text}},
		IsError:        true,
		ErrorCode:      "connection_required",
		NeedsUserInput: true,
		Metadata: map[string]any{
			"provider": required.Provider, "instance_id": required.InstanceID,
			"auth_url": required.AuthURL, "reauthorize": required.Reauthorize,
			"connection_action": ConnectionActionFor(required),
		},
	}, nil
}

func connectionRequiredText(required *RequiredError) string {
	title := required.Title
	if title == "" {
		title = required.Provider
	}
	if required.Reauthorize {
		text := fmt.Sprintf("%s needs additional access.", title)
		if required.AuthURL != "" {
			text += fmt.Sprintf("\nReconnect here: %s", required.AuthURL)
		}
		text += "\nAfter reconnecting, I will continue automatically in Slack."
		return text
	}
	text := fmt.Sprintf("%s is not connected.", title)
	if required.AuthURL != "" {
		text += fmt.Sprintf("\nConnect here: %s", required.AuthURL)
	}
	text += "\nAfter connecting, I will continue automatically in Slack. You can also reply in this thread to continue."
	return text
}

func AsRequired(err error, target **RequiredError) bool {
	if err == nil {
		return false
	}
	if req, ok := err.(*RequiredError); ok {
		*target = req
		return true
	}
	return false
}

func pluginScopes(provider string) []string {
	for _, item := range Plugins() {
		if item.ID == provider {
			return item.Scopes
		}
	}
	return nil
}

func randomState() (string, error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
