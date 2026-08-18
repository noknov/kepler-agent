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
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type Config struct {
	PublicBaseURL string
	SecretKey     string
	Slack         SlackOAuthConfig
	GitHub        GitHubOAuthConfig
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

func (c Config) OAuthEnabled() bool {
	return c.SlackEnabled() || c.GitHubEnabled()
}

func (s Service) ProviderOAuthEnabled(provider string) bool {
	switch provider {
	case ProviderSlack:
		return s.Config.SlackEnabled()
	case ProviderGitHub:
		return s.Config.GitHubEnabled()
	default:
		return false
	}
}

type Service struct {
	Store  Store
	Config Config
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
	if err := s.Store.CreateOAuthState(context.Background(), userID, provider, state, time.Now().UTC().Add(15*time.Minute)); err != nil {
		return "", err
	}
	return strings.TrimRight(s.Config.PublicBaseURL, "/") + "/oauth/" + url.PathEscape(provider) + "/start?state=" + url.QueryEscape(state), nil
}

func (s Service) HandleStart(w http.ResponseWriter, r *http.Request, provider, state string) {
	if state == "" {
		http.Error(w, "state is required", http.StatusBadRequest)
		return
	}
	_, storedProvider, err := s.Store.PeekOAuthState(r.Context(), state)
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
	userID, storedProvider, err := s.Store.ConsumeOAuthState(r.Context(), state)
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
	default:
		http.Error(w, "unsupported provider", http.StatusNotFound)
		return
	}
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

func ToolResult(err error) (tool.Result, error) {
	var required *RequiredError
	if !AsRequired(err, &required) {
		if err == nil {
			return tool.Result{}, nil
		}
		return tool.Result{}, err
	}
	text := fmt.Sprintf("%s is not connected.", required.Title)
	if required.AuthURL != "" {
		text += fmt.Sprintf("\nConnect here: %s", required.AuthURL)
	}
	return tool.Result{
		Content:      []model.Content{{Type: model.ContentText, Text: text}},
		IsError:      true,
		ErrorCode:    "connection_required",
		Metadata:     map[string]any{"provider": required.Provider, "auth_url": required.AuthURL},
	}, nil
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
