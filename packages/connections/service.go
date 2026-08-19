package connections

import (
	"bytes"
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
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
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
	return strings.TrimSpace(c.PublicBaseURL) != "" && c.ClickStack.Configured()
}

func (c ClickStackOAuthConfig) Configured() bool {
	return strings.TrimSpace(c.ServiceID) != "" || customClickStackURL(c.MCPURL)
}

func customClickStackURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw != "" && raw != clickstackDefaultMCPURL
}

func (c Config) GCPEnabled() bool {
	return strings.TrimSpace(c.PublicBaseURL) != "" && c.GCP.Enabled()
}

func (c Config) NotionEnabled() bool {
	return strings.TrimSpace(c.PublicBaseURL) != "" && c.Notion.Enabled()
}

func (c Config) OAuthEnabled() bool {
	return c.SlackEnabled() || c.GitHubEnabled() || c.ClickStackEnabled() || c.GCPEnabled() || c.NotionEnabled()
}

// OAuthCompletedHandler runs after a successful OAuth callback.
type OAuthCompletedHandler func(ctx context.Context, userID, provider string) error

type Service struct {
	Store             Store
	Config            Config
	Continuations     ContinuationStore
	OnOAuthCompleted  OAuthCompletedHandler
	clickstackOAuth   *clickstackOAuth
	gcpOAuth          *gcpOAuth
	clickstackRefresh sync.Map
	gcpRefresh        sync.Map
}

func (s *Service) clickstack() *clickstackOAuth {
	if s.clickstackOAuth == nil {
		s.clickstackOAuth = newClickStackOAuth(s.Config.ClickStack)
	}
	return s.clickstackOAuth
}

func (s *Service) gcp() *gcpOAuth {
	if s.gcpOAuth == nil {
		s.gcpOAuth = newGCPOAuth(s.Config.GCP)
	}
	return s.gcpOAuth
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

func (s Service) StartURL(userID, provider string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", fmt.Errorf("user id is required")
	}
	if s.Config.PublicBaseURL == "" {
		return "", fmt.Errorf("connections public base URL is not configured")
	}
	state, err := randomState()
	if err != nil {
		return "", err
	}
	meta := OAuthStateMeta{}
	if provider == ProviderClickStack {
		verifier, err := newPKCEVerifier()
		if err != nil {
			return "", err
		}
		meta.CodeVerifier = verifier
	}
	if err := s.Store.CreateOAuthState(context.Background(), userID, provider, state, time.Now().UTC().Add(15*time.Minute), meta); err != nil {
		return "", err
	}
	return strings.TrimRight(s.Config.PublicBaseURL, "/") + "/oauth/" + url.PathEscape(provider) + "/start?state=" + url.QueryEscape(state), nil
}

func (s Service) HandleStart(w http.ResponseWriter, r *http.Request, provider, state string) {
	if state == "" {
		http.Error(w, "state is required", http.StatusBadRequest)
		return
	}
	_, storedProvider, meta, err := s.Store.PeekOAuthState(r.Context(), state)
	if err != nil || storedProvider != provider {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}
	switch provider {
	case ProviderSlack:
		if !s.Config.SlackEnabled() {
			http.Error(w, "slack oauth is not configured", http.StatusServiceUnavailable)
			return
		}
		values := url.Values{
			"client_id":    {s.Config.Slack.ClientID},
			"user_scope":   {strings.Join(pluginScopes(provider), ",")},
			"redirect_uri": {s.callbackURL(provider)},
			"state":        {state},
		}
		http.Redirect(w, r, "https://slack.com/oauth/v2/authorize?"+values.Encode(), http.StatusFound)
	case ProviderGitHub:
		if !s.Config.GitHubEnabled() {
			http.Error(w, "github oauth is not configured", http.StatusServiceUnavailable)
			return
		}
		values := url.Values{
			"client_id":    {s.Config.GitHub.ClientID},
			"scope":        {strings.Join(pluginScopes(provider), " ")},
			"redirect_uri": {s.callbackURL(provider)},
			"state":        {state},
		}
		http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+values.Encode(), http.StatusFound)
	case ProviderClickStack:
		if !s.Config.ClickStackEnabled() {
			http.Error(w, "clickstack oauth is not configured", http.StatusServiceUnavailable)
			return
		}
		if meta.CodeVerifier == "" {
			http.Error(w, "invalid oauth state", http.StatusBadRequest)
			return
		}
		redirectURI := s.callbackURL(provider)
		clientID, err := s.clickstack().ensureClient(r.Context(), redirectURI)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		authorizeURL, err := s.clickstack().buildAuthorizeURL(clientID, redirectURI, state, meta.CodeVerifier)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, authorizeURL, http.StatusFound)
	case ProviderGCP:
		if !s.Config.GCPEnabled() {
			http.Error(w, "gcp oauth is not configured", http.StatusServiceUnavailable)
			return
		}
		authorizeURL := s.gcp().buildAuthorizeURL(s.callbackURL(provider), state)
		http.Redirect(w, r, authorizeURL, http.StatusFound)
	case ProviderNotion:
		if !s.Config.NotionEnabled() {
			http.Error(w, "notion oauth is not configured", http.StatusServiceUnavailable)
			return
		}
		values := url.Values{
			"client_id":     {s.Config.Notion.ClientID},
			"response_type": {"code"},
			"owner":         {"user"},
			"redirect_uri":  {s.callbackURL(provider)},
			"state":         {state},
		}
		http.Redirect(w, r, "https://api.notion.com/v1/oauth/authorize?"+values.Encode(), http.StatusFound)
	default:
		http.Error(w, "unsupported provider", http.StatusNotFound)
	}
}

func (s Service) HandleCallback(w http.ResponseWriter, r *http.Request, provider string) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	userID, storedProvider, meta, err := s.Store.ConsumeOAuthState(r.Context(), state)
	if err != nil || storedProvider != provider {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}
	switch provider {
	case ProviderSlack:
		token, account, scopes, err := s.exchangeSlack(r.Context(), code)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := s.Store.UpsertToken(r.Context(), userID, provider, token, scopes, account); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case ProviderGitHub:
		token, account, scopes, err := s.exchangeGitHub(r.Context(), code)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := s.Store.UpsertToken(r.Context(), userID, provider, token, scopes, account); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case ProviderClickStack:
		redirectURI := s.callbackURL(provider)
		clientID, err := s.clickstack().ensureClient(r.Context(), redirectURI)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		response, err := s.clickstack().exchange(r.Context(), code, meta.CodeVerifier, redirectURI, clientID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := s.storeClickStackBundle(r.Context(), userID, clientID, redirectURI, response, "", response.Scopes); err != nil {
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
		if err := s.storeGCPBundle(r.Context(), userID, response, "", nil); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case ProviderNotion:
		token, account, err := s.exchangeNotion(r.Context(), code)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := s.Store.UpsertToken(r.Context(), userID, provider, token, nil, account); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "unsupported provider", http.StatusNotFound)
		return
	}
	s.notifyOAuthCompleted(r.Context(), userID, provider)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<!doctype html><html><body><h1>Connected</h1><p>You can return to Slack and continue.</p></body></html>")
}

func (s Service) callbackURL(provider string) string {
	return strings.TrimRight(s.Config.PublicBaseURL, "/") + "/oauth/" + url.PathEscape(provider) + "/callback"
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
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
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

func (s Service) exchangeNotion(ctx context.Context, code string) (token, account string, err error) {
	redirectURI := s.callbackURL(ProviderNotion)
	body := map[string]string{
		"grant_type":   "authorization_code",
		"code":         code,
		"redirect_uri": redirectURI,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.notion.com/v1/oauth/token", bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	req.SetBasicAuth(s.Config.Notion.ClientID, s.Config.Notion.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Notion-Version", "2022-06-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("notion oauth failed: %s", strings.TrimSpace(string(data)))
	}
	var parsed struct {
		AccessToken   string `json:"access_token"`
		WorkspaceName string `json:"workspace_name"`
		Error         string `json:"error"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", "", err
	}
	if parsed.AccessToken == "" {
		if parsed.Error != "" {
			return "", "", fmt.Errorf("notion oauth failed: %s", parsed.Error)
		}
		return "", "", fmt.Errorf("notion oauth returned an empty token")
	}
	return parsed.AccessToken, parsed.WorkspaceName, nil
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
	if _, err := s.Store.Token(context.Background(), userID, provider); err == nil {
		return nil
	}
	authURL, err := s.StartURL(userID, provider)
	if err != nil {
		return &RequiredError{Provider: provider, Title: pluginTitle(provider)}
	}
	return &RequiredError{Provider: provider, Title: pluginTitle(provider), AuthURL: authURL}
}

// Reauthorize returns a connection-required error with a fresh OAuth URL even when
// a token already exists (for example when Notion returns 403 for a page).
func (s Service) Reauthorize(userID, provider string) error {
	authURL, err := s.StartURL(userID, provider)
	if err != nil {
		return &RequiredError{Provider: provider, Title: pluginTitle(provider), Reauthorize: true}
	}
	return &RequiredError{Provider: provider, Title: pluginTitle(provider), AuthURL: authURL, Reauthorize: true}
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
		Metadata:       map[string]any{"provider": required.Provider, "auth_url": required.AuthURL, "reauthorize": required.Reauthorize},
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
