package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	notionOAuthIssuer         = "https://mcp.notion.com"
	notionDefaultMCPURL       = "https://mcp.notion.com/mcp"
	notionOAuthScope          = "default"
	notionOAuthClientCacheTTL = 24 * time.Hour
)

type NotionOAuthConfig struct {
	MCPURL string
}

func (c NotionOAuthConfig) Configured() bool {
	return true
}

func (c NotionOAuthConfig) Enabled() bool {
	return c.Configured()
}

type notionOAuthClient struct {
	ClientID     string
	RedirectURI  string
	RegisteredAt time.Time
}

type notionOAuth struct {
	cfg          NotionOAuthConfig
	httpClient   *http.Client
	registerURL  string
	authorizeURL string
	tokenURL     string
	mu           sync.Mutex
	client       *notionOAuthClient
}

func newNotionOAuth(cfg NotionOAuthConfig) *notionOAuth {
	return &notionOAuth{
		cfg:          cfg,
		httpClient:   http.DefaultClient,
		registerURL:  notionOAuthIssuer + "/register",
		authorizeURL: notionOAuthIssuer + "/authorize",
		tokenURL:     notionOAuthIssuer + "/token",
	}
}

func (o *notionOAuth) resourceURL() string {
	return notionOAuthIssuer
}

func (o *notionOAuth) mcpURL() string {
	if url := strings.TrimSpace(o.cfg.MCPURL); url != "" {
		return strings.TrimRight(url, "/")
	}
	return strings.TrimRight(notionDefaultMCPURL, "/")
}

func (o *notionOAuth) ensureClient(ctx context.Context, redirectURI string) (string, error) {
	redirectURI = strings.TrimSpace(redirectURI)
	if redirectURI == "" {
		return "", fmt.Errorf("notion oauth redirect uri is required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.client != nil && o.client.RedirectURI == redirectURI && time.Since(o.client.RegisteredAt) < notionOAuthClientCacheTTL {
		return o.client.ClientID, nil
	}
	clientID, err := o.registerClient(ctx, redirectURI)
	if err != nil {
		return "", err
	}
	o.client = &notionOAuthClient{ClientID: clientID, RedirectURI: redirectURI, RegisteredAt: time.Now().UTC()}
	return clientID, nil
}

func (o *notionOAuth) registerClient(ctx context.Context, redirectURI string) (string, error) {
	payload := map[string]any{
		"client_name":                "slack-copilot-agent",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.registerURL, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("notion oauth register: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if strings.TrimSpace(parsed.ClientID) == "" {
		return "", fmt.Errorf("notion oauth register returned an empty client_id")
	}
	return parsed.ClientID, nil
}

func (o *notionOAuth) buildAuthorizeURL(clientID, redirectURI, state, codeVerifier string) (string, error) {
	challenge, err := pkceChallenge(codeVerifier)
	if err != nil {
		return "", err
	}
	values := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"scope":                 {notionOAuthScope},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {o.resourceURL()},
	}
	return o.authorizeURL + "?" + values.Encode(), nil
}

type notionTokenResponse struct {
	AccessToken  string
	RefreshToken string
	Scopes       []string
	ExpiresIn    int
	UserID       string
	WorkspaceID  string
	EmailDomain  string
}

func (o *notionOAuth) exchange(ctx context.Context, code, codeVerifier, redirectURI, clientID string) (notionTokenResponse, error) {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {codeVerifier},
		"resource":      {o.resourceURL()},
	}
	return o.requestToken(ctx, values)
}

func (o *notionOAuth) refresh(ctx context.Context, refreshToken, redirectURI, clientID string) (notionTokenResponse, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return notionTokenResponse{}, fmt.Errorf("notion oauth refresh token is missing")
	}
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"resource":      {o.resourceURL()},
	}
	if redirectURI = strings.TrimSpace(redirectURI); redirectURI != "" {
		values.Set("redirect_uri", redirectURI)
	}
	return o.requestToken(ctx, values)
}

func (o *notionOAuth) requestToken(ctx context.Context, values url.Values) (notionTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return notionTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return notionTokenResponse{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return notionTokenResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return notionTokenResponse{}, fmt.Errorf("notion oauth token: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
		ExpiresIn    int    `json:"expires_in"`
		UserID       string `json:"user_id"`
		WorkspaceID  string `json:"workspace_id"`
		EmailDomain  string `json:"email_domain"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return notionTokenResponse{}, err
	}
	if payload.AccessToken == "" {
		if payload.Error != "" {
			return notionTokenResponse{}, fmt.Errorf("notion oauth failed: %s", payload.Error)
		}
		return notionTokenResponse{}, fmt.Errorf("notion oauth returned an empty access token")
	}
	var scopes []string
	if payload.Scope != "" {
		scopes = strings.Fields(payload.Scope)
	}
	refresh := strings.TrimSpace(payload.RefreshToken)
	if refresh == "" {
		refresh = strings.TrimSpace(values.Get("refresh_token"))
	}
	return notionTokenResponse{
		AccessToken:  payload.AccessToken,
		RefreshToken: refresh,
		Scopes:       scopes,
		ExpiresIn:    payload.ExpiresIn,
		UserID:       strings.TrimSpace(payload.UserID),
		WorkspaceID:  strings.TrimSpace(payload.WorkspaceID),
		EmailDomain:  strings.TrimSpace(payload.EmailDomain),
	}, nil
}

func (o *notionOAuth) accountLabel(response notionTokenResponse) string {
	if response.EmailDomain != "" {
		return response.EmailDomain
	}
	if response.WorkspaceID != "" {
		return response.WorkspaceID
	}
	return response.UserID
}
