package connections

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const connectURLTTL = 15 * time.Minute

// ConnectURL returns a signed gateway URL that starts OAuth on click. The gateway
// creates oauth state and PKCE material when the user opens the link.
func (s Service) ConnectURL(userID, provider string) (string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", fmt.Errorf("user id is required")
	}
	if s.Config.PublicBaseURL == "" {
		return "", fmt.Errorf("connections public base URL is not configured")
	}
	if strings.TrimSpace(s.Config.SecretKey) == "" {
		return "", fmt.Errorf("connections encryption key is not configured")
	}
	exp := time.Now().UTC().Add(connectURLTTL).Unix()
	sig, err := s.signConnectURL(userID, provider, exp)
	if err != nil {
		return "", err
	}
	values := url.Values{
		"user_id": {userID},
		"exp":     {strconv.FormatInt(exp, 10)},
		"sig":     {sig},
	}
	return strings.TrimRight(s.Config.PublicBaseURL, "/") + "/oauth/" + url.PathEscape(provider) + "/connect?" + values.Encode(), nil
}

// StartURL returns a signed connect URL. Legacy /oauth/*/start links are still
// accepted for in-flight authorizations.
func (s Service) StartURL(userID, provider string) (string, error) {
	return s.ConnectURL(userID, provider)
}

func (s Service) HandleConnect(w http.ResponseWriter, r *http.Request, provider string) {
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if err := s.verifyConnectURL(userID, provider, r.URL.Query().Get("exp"), r.URL.Query().Get("sig")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	state, meta, err := s.createOAuthState(r.Context(), userID, provider)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.redirectOAuthAuthorize(w, r, provider, state, meta)
}

func (s Service) createOAuthState(ctx context.Context, userID, provider string) (string, OAuthStateMeta, error) {
	state, err := randomState()
	if err != nil {
		return "", OAuthStateMeta{}, err
	}
	meta := OAuthStateMeta{}
	if provider == ProviderClickStack || provider == ProviderNotion {
		verifier, err := newPKCEVerifier()
		if err != nil {
			return "", OAuthStateMeta{}, err
		}
		meta.CodeVerifier = verifier
	}
	if err := s.Store.CreateOAuthState(ctx, userID, provider, state, time.Now().UTC().Add(connectURLTTL), meta); err != nil {
		return "", OAuthStateMeta{}, err
	}
	return state, meta, nil
}

func (s Service) signConnectURL(userID, provider string, exp int64) (string, error) {
	key := strings.TrimSpace(s.Config.SecretKey)
	if key == "" {
		return "", fmt.Errorf("connections encryption key is not configured")
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(userID))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(provider))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (s Service) verifyConnectURL(userID, provider, expRaw, sig string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("user id is required")
	}
	if strings.TrimSpace(sig) == "" {
		return fmt.Errorf("connect signature is required")
	}
	exp, err := strconv.ParseInt(strings.TrimSpace(expRaw), 10, 64)
	if err != nil || exp <= 0 {
		return fmt.Errorf("connect link is invalid")
	}
	if time.Now().UTC().After(time.Unix(exp, 0)) {
		return fmt.Errorf("connect link expired; reopen App Home and connect again")
	}
	expected, err := s.signConnectURL(userID, provider, exp)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return fmt.Errorf("connect link is invalid")
	}
	return nil
}

func (s Service) redirectOAuthAuthorize(w http.ResponseWriter, r *http.Request, provider, state string, meta OAuthStateMeta) {
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
			http.Error(w, "oauth state is missing pkce verifier", http.StatusBadRequest)
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
		if meta.CodeVerifier == "" {
			http.Error(w, "oauth state is missing pkce verifier", http.StatusBadRequest)
			return
		}
		redirectURI := s.callbackURL(provider)
		clientID, err := s.notion().ensureClient(r.Context(), redirectURI)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		authorizeURL, err := s.notion().buildAuthorizeURL(clientID, redirectURI, state, meta.CodeVerifier)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, authorizeURL, http.StatusFound)
	default:
		http.Error(w, "unsupported provider", http.StatusNotFound)
	}
}
