package connections

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/safety"
)

const connectURLTTL = 15 * time.Minute

// ConnectionContext identifies where a connection flow was initiated. The
// values are opaque to this package and are carried through the signed URL and
// durable OAuth state so each surface can restore its own experience without
// guessing from the request or provider.
type ConnectionContext struct {
	Origin        string
	ReturnContext string
}

// ConnectURL returns a signed gateway URL that starts OAuth on click. The gateway
// creates oauth state and PKCE material when the user opens the link.
func (s Service) ConnectURL(userID, provider string) (string, error) {
	return s.ConnectURLForInstance(userID, provider, DefaultInstanceID, "", nil)
}

// ConnectURLForInstance creates a signed, short-lived connection action. The
// optional metadata travels through OAuth state so a provider can bind the
// resulting credential to the selected server/account rather than replacing a
// previous connection of that provider.
func (s Service) ConnectURLForInstance(userID, provider, instanceID, label string, metadata map[string]string) (string, error) {
	return s.ConnectURLForInstanceWithContext(userID, provider, instanceID, label, metadata, ConnectionContext{})
}

// ConnectURLForInstanceWithContext is the context-aware form used by
// interactive surfaces. Context is included in the signature and persisted in
// OAuth state only after the signed link is opened.
func (s Service) ConnectURLForInstanceWithContext(userID, provider, instanceID, label string, metadata map[string]string, connectionContext ConnectionContext) (string, error) {
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
	instanceID = connectionInstanceID(instanceID)
	metadata = cloneMetadata(metadata)
	if err := validateConnectionMetadata(provider, metadata); err != nil {
		return "", err
	}
	sig, err := s.signConnectURLForInstanceWithContext(userID, provider, instanceID, label, metadata, connectionContext, exp)
	if err != nil {
		return "", err
	}
	values := url.Values{
		"user_id":     {userID},
		"exp":         {strconv.FormatInt(exp, 10)},
		"sig":         {sig},
		"instance_id": {instanceID},
	}
	if strings.TrimSpace(label) != "" {
		values.Set("label", label)
	}
	if encoded, err := encodeConnectionMetadata(metadata); err != nil {
		return "", err
	} else if encoded != "" {
		values.Set("metadata", encoded)
	}
	if strings.TrimSpace(connectionContext.Origin) != "" {
		values.Set("origin", connectionContext.Origin)
	}
	if strings.TrimSpace(connectionContext.ReturnContext) != "" {
		values.Set("return_context", connectionContext.ReturnContext)
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
	instanceID := connectionInstanceID(r.URL.Query().Get("instance_id"))
	label := strings.TrimSpace(r.URL.Query().Get("label"))
	connectionContext := ConnectionContext{
		Origin:        strings.TrimSpace(r.URL.Query().Get("origin")),
		ReturnContext: strings.TrimSpace(r.URL.Query().Get("return_context")),
	}
	metadata, err := decodeConnectionMetadata(r.URL.Query().Get("metadata"))
	if err != nil {
		http.Error(w, "connection metadata is invalid", http.StatusBadRequest)
		return
	}
	if err := validateConnectionMetadata(provider, metadata); err != nil {
		http.Error(w, "connection metadata is invalid: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.verifyConnectURLForInstanceWithContext(userID, provider, instanceID, label, metadata, connectionContext, r.URL.Query().Get("exp"), r.URL.Query().Get("sig")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	state, meta, err := s.createOAuthState(r.Context(), userID, provider, OAuthStateMeta{InstanceID: instanceID, Label: label, Metadata: metadata, Origin: connectionContext.Origin, ReturnContext: connectionContext.ReturnContext})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.redirectOAuthAuthorize(w, r, provider, state, meta)
}

func validateConnectionMetadata(provider string, metadata map[string]string) error {
	if provider != ProviderClickStack && provider != ProviderNotion {
		return nil
	}
	if raw := strings.TrimSpace(metadata["mcp_url"]); raw != "" {
		if err := safety.ValidatePublicHTTPURL(raw); err != nil {
			return fmt.Errorf("clickstack mcp_url: %w", err)
		}
	}
	return nil
}

func (s Service) createOAuthState(ctx context.Context, userID, provider string, bases ...OAuthStateMeta) (string, OAuthStateMeta, error) {
	state, err := randomState()
	if err != nil {
		return "", OAuthStateMeta{}, err
	}
	var base OAuthStateMeta
	if len(bases) > 0 {
		base = bases[0]
	}
	meta := base
	meta.InstanceID = connectionInstanceID(meta.InstanceID)
	meta.Origin = strings.TrimSpace(meta.Origin)
	meta.ReturnContext = strings.TrimSpace(meta.ReturnContext)
	meta.Metadata = cloneMetadata(meta.Metadata)
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
	return s.signConnectURLForInstance(userID, provider, DefaultInstanceID, "", nil, exp)
}

func (s Service) signConnectURLForInstance(userID, provider, instanceID, label string, metadata map[string]string, exp int64) (string, error) {
	return s.signConnectURLForInstanceWithContext(userID, provider, instanceID, label, metadata, ConnectionContext{}, exp)
}

func (s Service) signConnectURLForInstanceWithContext(userID, provider, instanceID, label string, metadata map[string]string, connectionContext ConnectionContext, exp int64) (string, error) {
	key := strings.TrimSpace(s.Config.SecretKey)
	if key == "" {
		return "", fmt.Errorf("connections encryption key is not configured")
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(userID))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(provider))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(connectionInstanceID(instanceID)))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(strings.TrimSpace(label)))
	_, _ = mac.Write([]byte{'\n'})
	encoded, err := encodeConnectionMetadata(metadata)
	if err != nil {
		return "", err
	}
	_, _ = mac.Write([]byte(encoded))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(strings.TrimSpace(connectionContext.Origin)))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(strings.TrimSpace(connectionContext.ReturnContext)))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (s Service) signConnectURLForInstanceLegacy(userID, provider, instanceID, label string, metadata map[string]string, exp int64) (string, error) {
	key := strings.TrimSpace(s.Config.SecretKey)
	if key == "" {
		return "", fmt.Errorf("connections encryption key is not configured")
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(userID))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(provider))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(connectionInstanceID(instanceID)))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(strings.TrimSpace(label)))
	_, _ = mac.Write([]byte{'\n'})
	encoded, err := encodeConnectionMetadata(metadata)
	if err != nil {
		return "", err
	}
	_, _ = mac.Write([]byte(encoded))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (s Service) verifyConnectURL(userID, provider, expRaw, sig string) error {
	return s.verifyConnectURLForInstance(userID, provider, DefaultInstanceID, "", nil, expRaw, sig)
}

func (s Service) verifyConnectURLForInstance(userID, provider, instanceID, label string, metadata map[string]string, expRaw, sig string) error {
	return s.verifyConnectURLForInstanceWithContext(userID, provider, instanceID, label, metadata, ConnectionContext{}, expRaw, sig)
}

func (s Service) verifyConnectURLForInstanceWithContext(userID, provider, instanceID, label string, metadata map[string]string, connectionContext ConnectionContext, expRaw, sig string) error {
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
	expected, err := s.signConnectURLForInstanceWithContext(userID, provider, instanceID, label, metadata, connectionContext, exp)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		// Accept links issued by the previous deployment while their short TTL
		// is still active. New links always bind the explicit context above.
		if connectionContext == (ConnectionContext{}) {
			legacy, legacyErr := s.signConnectURLForInstanceLegacy(userID, provider, instanceID, label, metadata, exp)
			if legacyErr == nil && hmac.Equal([]byte(legacy), []byte(sig)) {
				return nil
			}
		}
		return fmt.Errorf("connect link is invalid")
	}
	return nil
}

func encodeConnectionMetadata(metadata map[string]string) (string, error) {
	if len(metadata) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeConnectionMetadata(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var metadata map[string]string
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return nil, err
	}
	return cloneMetadata(metadata), nil
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
		if err := validateConnectionMetadata(provider, meta.Metadata); err != nil {
			http.Error(w, "connection metadata is invalid: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !s.Config.ClickStackEnabled() && strings.TrimSpace(meta.Metadata["mcp_url"]) == "" {
			http.Error(w, "clickstack oauth is not configured", http.StatusServiceUnavailable)
			return
		}
		if meta.CodeVerifier == "" {
			http.Error(w, "oauth state is missing pkce verifier", http.StatusBadRequest)
			return
		}
		redirectURI := s.callbackURL(provider)
		oauth := s.clickstackForMeta(meta)
		clientID, err := oauth.ensureClient(r.Context(), redirectURI)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		authorizeURL, err := oauth.buildAuthorizeURL(clientID, redirectURI, state, meta.CodeVerifier)
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
