package connections

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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
	clickstackOAuthIssuer         = "https://mcp.clickhouse.cloud"
	clickstackDefaultMCPURL       = "https://mcp.clickhouse.cloud/clickstack"
	clickstackOAuthScope          = "clickstack:access openid profile email"
	clickstackOAuthClientCacheTTL = 24 * time.Hour
)

type ClickStackOAuthConfig struct {
	MCPURL    string
	ServiceID string
}

type clickstackOAuthClient struct {
	ClientID     string
	RedirectURI  string
	RegisteredAt time.Time
}

type clickstackOAuth struct {
	cfg        ClickStackOAuthConfig
	httpClient *http.Client
	registerURL string
	authorizeURL string
	tokenURL     string
	mu         sync.Mutex
	client     *clickstackOAuthClient
}

func newClickStackOAuth(cfg ClickStackOAuthConfig) *clickstackOAuth {
	return &clickstackOAuth{
		cfg:          cfg,
		httpClient:   http.DefaultClient,
		registerURL:  clickstackOAuthIssuer + "/register",
		authorizeURL: clickstackOAuthIssuer + "/authorize",
		tokenURL:     clickstackOAuthIssuer + "/token",
	}
}

func (o *clickstackOAuth) resourceURL() string {
	if url := strings.TrimSpace(o.cfg.MCPURL); url != "" {
		return strings.TrimRight(url, "/")
	}
	return clickstackDefaultMCPURL
}

func (o *clickstackOAuth) ensureClient(ctx context.Context, redirectURI string) (string, error) {
	redirectURI = strings.TrimSpace(redirectURI)
	if redirectURI == "" {
		return "", fmt.Errorf("clickstack oauth redirect uri is required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.client != nil && o.client.RedirectURI == redirectURI && time.Since(o.client.RegisteredAt) < clickstackOAuthClientCacheTTL {
		return o.client.ClientID, nil
	}
	clientID, err := o.registerClient(ctx, redirectURI)
	if err != nil {
		return "", err
	}
	o.client = &clickstackOAuthClient{ClientID: clientID, RedirectURI: redirectURI, RegisteredAt: time.Now().UTC()}
	return clientID, nil
}

func (o *clickstackOAuth) registerClient(ctx context.Context, redirectURI string) (string, error) {
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
		return "", fmt.Errorf("clickstack oauth register: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if strings.TrimSpace(parsed.ClientID) == "" {
		return "", fmt.Errorf("clickstack oauth register returned an empty client_id")
	}
	return parsed.ClientID, nil
}

func (o *clickstackOAuth) buildAuthorizeURL(clientID, redirectURI, state, codeVerifier string) (string, error) {
	challenge, err := pkceChallenge(codeVerifier)
	if err != nil {
		return "", err
	}
	values := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"scope":                 {clickstackOAuthScope},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {o.resourceURL()},
	}
	return o.authorizeURL + "?" + values.Encode(), nil
}

type clickstackTokenResponse struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	Scopes       []string
	ExpiresIn    int
}

func (o *clickstackOAuth) exchange(ctx context.Context, code, codeVerifier, redirectURI, clientID string) (clickstackTokenResponse, error) {
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

func (o *clickstackOAuth) refresh(ctx context.Context, refreshToken, redirectURI, clientID string) (clickstackTokenResponse, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return clickstackTokenResponse{}, fmt.Errorf("clickstack oauth refresh token is missing")
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

func (o *clickstackOAuth) requestToken(ctx context.Context, values url.Values) (clickstackTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return clickstackTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return clickstackTokenResponse{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return clickstackTokenResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return clickstackTokenResponse{}, fmt.Errorf("clickstack oauth token: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		Scope        string `json:"scope"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return clickstackTokenResponse{}, err
	}
	if payload.AccessToken == "" {
		if payload.Error != "" {
			return clickstackTokenResponse{}, fmt.Errorf("clickstack oauth failed: %s", payload.Error)
		}
		return clickstackTokenResponse{}, fmt.Errorf("clickstack oauth returned an empty access token")
	}
	var scopes []string
	if payload.Scope != "" {
		scopes = strings.Fields(payload.Scope)
	}
	refresh := strings.TrimSpace(payload.RefreshToken)
	if refresh == "" {
		refresh = strings.TrimSpace(values.Get("refresh_token"))
	}
	return clickstackTokenResponse{
		AccessToken:  payload.AccessToken,
		RefreshToken: refresh,
		IDToken:      payload.IDToken,
		Scopes:       scopes,
		ExpiresIn:    payload.ExpiresIn,
	}, nil
}

func (o *clickstackOAuth) accountLabel(accessToken, idToken string) string {
	return accountLabelFromTokens(accessToken, idToken)
}

func accountLabelFromTokens(accessToken, idToken string) string {
	for _, token := range []string{idToken, accessToken} {
		if label := accountFromJWT(token); label != "" {
			return label
		}
	}
	return ""
}

func accountFromIDToken(idToken string) string {
	return accountFromJWT(idToken)
}

func accountFromJWT(token string) string {
	claims := parseJWTClaims(token)
	if claims == nil {
		return ""
	}
	for _, candidate := range identityClaimCandidates(claims) {
		if label := strings.TrimSpace(candidate); label != "" {
			return label
		}
	}
	return ""
}

func identityClaimCandidates(claims map[string]any) []string {
	direct := []string{
		stringClaim(claims, "email"),
		stringClaim(claims, "preferred_username"),
		stringClaim(claims, "name"),
		stringClaim(claims, "username"),
		stringClaim(claims, "login"),
		stringClaim(claims, "sub"),
	}
	for key, value := range claims {
		lower := strings.ToLower(key)
		if strings.HasSuffix(lower, "/email") || strings.HasSuffix(lower, ":email") {
			if label := stringClaimValue(value); label != "" {
				return append([]string{label}, direct...)
			}
		}
	}
	return direct
}

func parseJWTClaims(token string) map[string]any {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}

func stringClaim(claims map[string]any, key string) string {
	return stringClaimValue(claims[key])
}

func stringClaimValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func newPKCEVerifier() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func pkceChallenge(verifier string) (string, error) {
	if strings.TrimSpace(verifier) == "" {
		return "", fmt.Errorf("pkce verifier is required")
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

