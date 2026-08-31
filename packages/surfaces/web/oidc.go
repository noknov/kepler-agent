package web

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const slackDiscoveryURL = "https://slack.com/.well-known/openid-configuration"

type IdentityProvider interface {
	Name() string
	AuthorizationURL(context.Context, string, string, string) (string, error)
	Exchange(context.Context, string, string, string) (Identity, error)
}

type OIDCProvider struct {
	ProviderName string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	DiscoveryURL string
	HTTPClient   *http.Client
	Clock        func() time.Time

	mu        sync.Mutex
	discovery oidcDiscovery
	keys      map[string]*rsa.PublicKey
	loadedAt  time.Time
}

type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

func NewSlackOIDC(clientID, clientSecret, redirectURL string) *OIDCProvider {
	return &OIDCProvider{
		ProviderName: "slack", ClientID: clientID, ClientSecret: clientSecret,
		RedirectURL: redirectURL, DiscoveryURL: slackDiscoveryURL,
		HTTPClient: &http.Client{Timeout: 15 * time.Second}, Clock: time.Now,
	}
}

func (p *OIDCProvider) Name() string { return p.ProviderName }

func (p *OIDCProvider) AuthorizationURL(ctx context.Context, state, nonce, challenge string) (string, error) {
	discovery, _, err := p.load(ctx, false)
	if err != nil {
		return "", err
	}
	values := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.ClientID},
		"redirect_uri":          {p.RedirectURL},
		"scope":                 {"openid profile email"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return discovery.AuthorizationEndpoint + "?" + values.Encode(), nil
}

func (p *OIDCProvider) Exchange(ctx context.Context, code, verifier, nonce string) (Identity, error) {
	discovery, _, err := p.load(ctx, false)
	if err != nil {
		return Identity{}, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {p.ClientID},
		"client_secret": {p.ClientSecret},
		"code":          {code},
		"redirect_uri":  {p.RedirectURL},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.client().Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("exchange identity code: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Identity{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Identity{}, fmt.Errorf("identity provider rejected token exchange")
	}
	var token struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &token); err != nil || token.IDToken == "" || (!token.OK && token.Error != "") {
		return Identity{}, fmt.Errorf("identity provider returned an invalid token response")
	}
	claims, err := p.verifyIDToken(ctx, token.IDToken, discovery, nonce)
	if err != nil {
		return Identity{}, err
	}
	identity := Identity{
		Provider: p.ProviderName, TenantID: claims.TeamID, SubjectID: claims.UserID,
		Email: claims.Email, DisplayName: claims.Name, AvatarURL: claims.Picture,
	}
	if identity.SubjectID == "" || identity.TenantID == "" {
		return Identity{}, fmt.Errorf("identity token is missing Slack user or workspace")
	}
	if identity.DisplayName == "" {
		identity.DisplayName = identity.Email
	}
	if identity.DisplayName == "" {
		identity.DisplayName = identity.SubjectID
	}
	return identity, nil
}

type slackClaims struct {
	Issuer        string `json:"iss"`
	Subject       string `json:"sub"`
	Audience      any    `json:"aud"`
	ExpiresAt     int64  `json:"exp"`
	IssuedAt      int64  `json:"iat"`
	Nonce         string `json:"nonce"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	TeamID        string `json:"https://slack.com/team_id"`
	UserID        string `json:"https://slack.com/user_id"`
}

func (p *OIDCProvider) verifyIDToken(ctx context.Context, raw string, discovery oidcDiscovery, nonce string) (slackClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return slackClaims{}, fmt.Errorf("identity token is malformed")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return slackClaims{}, fmt.Errorf("identity token header is malformed")
	}
	var header struct{ Algorithm, KeyID string }
	var rawHeader map[string]json.RawMessage
	if err := json.Unmarshal(headerBytes, &rawHeader); err != nil {
		return slackClaims{}, fmt.Errorf("identity token header is malformed")
	}
	_ = json.Unmarshal(rawHeader["alg"], &header.Algorithm)
	_ = json.Unmarshal(rawHeader["kid"], &header.KeyID)
	if header.Algorithm != "RS256" || header.KeyID == "" {
		return slackClaims{}, fmt.Errorf("identity token uses an unsupported signature")
	}
	_, keys, err := p.load(ctx, false)
	if err != nil {
		return slackClaims{}, err
	}
	key := keys[header.KeyID]
	if key == nil {
		_, keys, err = p.load(ctx, true)
		if err != nil {
			return slackClaims{}, err
		}
		key = keys[header.KeyID]
	}
	if key == nil {
		return slackClaims{}, fmt.Errorf("identity token signing key is unknown")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return slackClaims{}, fmt.Errorf("identity token signature is malformed")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return slackClaims{}, fmt.Errorf("identity token signature is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return slackClaims{}, fmt.Errorf("identity token claims are malformed")
	}
	var claims slackClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return slackClaims{}, fmt.Errorf("identity token claims are malformed")
	}
	now := p.now().Unix()
	if claims.Issuer != discovery.Issuer || !audienceContains(claims.Audience, p.ClientID) || claims.ExpiresAt <= now || claims.IssuedAt > now+60 || claims.Nonce != nonce {
		return slackClaims{}, fmt.Errorf("identity token claims are invalid")
	}
	return claims, nil
}

func audienceContains(raw any, expected string) bool {
	switch value := raw.(type) {
	case string:
		return value == expected
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok && text == expected {
				return true
			}
		}
	}
	return false
}

func (p *OIDCProvider) load(ctx context.Context, force bool) (oidcDiscovery, map[string]*rsa.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !force && p.discovery.Issuer != "" && p.now().Sub(p.loadedAt) < time.Hour {
		return p.discovery, p.keys, nil
	}
	var discovery oidcDiscovery
	if err := p.getJSON(ctx, p.DiscoveryURL, &discovery); err != nil {
		return oidcDiscovery{}, nil, fmt.Errorf("load identity provider metadata: %w", err)
	}
	if discovery.Issuer == "" || discovery.AuthorizationEndpoint == "" || discovery.TokenEndpoint == "" || discovery.JWKSURI == "" {
		return oidcDiscovery{}, nil, fmt.Errorf("identity provider metadata is incomplete")
	}
	var document struct {
		Keys []struct {
			KeyID string `json:"kid"`
			Type  string `json:"kty"`
			Use   string `json:"use"`
			N     string `json:"n"`
			E     string `json:"e"`
		} `json:"keys"`
	}
	if err := p.getJSON(ctx, discovery.JWKSURI, &document); err != nil {
		return oidcDiscovery{}, nil, fmt.Errorf("load identity provider keys: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, item := range document.Keys {
		if item.KeyID == "" || item.Type != "RSA" || (item.Use != "" && item.Use != "sig") {
			continue
		}
		modulus, modulusErr := base64.RawURLEncoding.DecodeString(item.N)
		exponent, exponentErr := base64.RawURLEncoding.DecodeString(item.E)
		if modulusErr != nil || exponentErr != nil || len(exponent) == 0 || len(exponent) > 4 {
			continue
		}
		padded := make([]byte, 4)
		copy(padded[4-len(exponent):], exponent)
		e := int(binary.BigEndian.Uint32(padded))
		if e < 3 {
			continue
		}
		keys[item.KeyID] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: e}
	}
	if len(keys) == 0 {
		return oidcDiscovery{}, nil, errors.New("identity provider published no usable signing keys")
	}
	p.discovery, p.keys, p.loadedAt = discovery, keys, p.now()
	return discovery, keys, nil
}

func (p *OIDCProvider) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(target)
}

func (p *OIDCProvider) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (p *OIDCProvider) now() time.Time {
	if p.Clock != nil {
		return p.Clock().UTC()
	}
	return time.Now().UTC()
}
