package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	gcpOAuthScope      = "https://www.googleapis.com/auth/logging.read https://www.googleapis.com/auth/cloud-platform.read-only"
	gcpAuthorizeURL    = "https://accounts.google.com/o/oauth2/v2/auth"
	gcpTokenURL        = "https://oauth2.googleapis.com/token"
	gcpUserInfoURL     = "https://www.googleapis.com/oauth2/v2/userinfo"
	gcpOAuthAccessType = "offline"
)

type GCPOAuthConfig struct {
	ClientID     string
	ClientSecret string
}

func (c GCPOAuthConfig) Enabled() bool {
	return strings.TrimSpace(c.ClientID) != "" && strings.TrimSpace(c.ClientSecret) != ""
}

type gcpOAuth struct {
	cfg        GCPOAuthConfig
	httpClient *http.Client
	tokenURL   string
}

type gcpTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func newGCPOAuth(cfg GCPOAuthConfig) *gcpOAuth {
	return &gcpOAuth{cfg: cfg, httpClient: http.DefaultClient, tokenURL: gcpTokenURL}
}

func (o *gcpOAuth) buildAuthorizeURL(redirectURI, state string) string {
	values := url.Values{
		"client_id":     {strings.TrimSpace(o.cfg.ClientID)},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {gcpOAuthScope},
		"state":         {state},
		"access_type":   {gcpOAuthAccessType},
		"prompt":        {"consent"},
	}
	return gcpAuthorizeURL + "?" + values.Encode()
}

func (o *gcpOAuth) exchange(ctx context.Context, code, redirectURI string) (gcpTokenResponse, error) {
	values := url.Values{
		"client_id":     {strings.TrimSpace(o.cfg.ClientID)},
		"client_secret": {strings.TrimSpace(o.cfg.ClientSecret)},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}
	return o.requestToken(ctx, values)
}

func (o *gcpOAuth) refresh(ctx context.Context, refreshToken string) (gcpTokenResponse, error) {
	values := url.Values{
		"client_id":     {strings.TrimSpace(o.cfg.ClientID)},
		"client_secret": {strings.TrimSpace(o.cfg.ClientSecret)},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	return o.requestToken(ctx, values)
}

func (o *gcpOAuth) requestToken(ctx context.Context, values url.Values) (gcpTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return gcpTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return gcpTokenResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return gcpTokenResponse{}, err
	}
	var payload gcpTokenResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return gcpTokenResponse{}, err
	}
	if payload.AccessToken == "" {
		if payload.Error != "" {
			msg := payload.Error
			if payload.ErrorDesc != "" {
				msg += ": " + payload.ErrorDesc
			}
			return gcpTokenResponse{}, fmt.Errorf("gcp oauth failed: %s", msg)
		}
		return gcpTokenResponse{}, fmt.Errorf("gcp oauth returned an empty access token")
	}
	if payload.Scope != "" {
		payload.Scope = strings.TrimSpace(payload.Scope)
	}
	return payload, nil
}

func (o *gcpOAuth) accountLabel(ctx context.Context, accessToken string) string {
	if strings.TrimSpace(accessToken) == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gcpUserInfoURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var payload struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Email)
}

func gcpExpiresAt(access string, expiresIn int) time.Time {
	if expiresIn > 0 {
		return time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)
	}
	if exp, ok := accessTokenExpiresAt(access); ok {
		return exp
	}
	return time.Time{}
}

func gcpScopes(response gcpTokenResponse) []string {
	if strings.TrimSpace(response.Scope) == "" {
		return strings.Fields(gcpOAuthScope)
	}
	return strings.Fields(response.Scope)
}
