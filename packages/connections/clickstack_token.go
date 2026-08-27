package connections

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const clickstackTokenRefreshSkew = 5 * time.Minute

type clickStackTokenBundle struct {
	Access      string
	Refresh     string
	ClientID    string
	RedirectURI string
	ExpiresAt   time.Time
}

func (b clickStackTokenBundle) needsRefresh(now time.Time) bool {
	if strings.TrimSpace(b.Access) == "" {
		return true
	}
	deadline := now.UTC().Add(clickstackTokenRefreshSkew)
	if !b.ExpiresAt.IsZero() {
		return !deadline.Before(b.ExpiresAt)
	}
	if exp, ok := accessTokenExpiresAt(b.Access); ok {
		return !deadline.Before(exp)
	}
	return false
}

func parseClickStackTokenBundle(raw string) (clickStackTokenBundle, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return clickStackTokenBundle{}, fmt.Errorf("clickstack token is empty")
	}
	if raw[0] != '{' {
		return clickStackTokenBundle{Access: raw}, nil
	}
	var payload struct {
		Access      string `json:"access"`
		Refresh     string `json:"refresh"`
		ClientID    string `json:"client_id"`
		RedirectURI string `json:"redirect_uri"`
		ExpiresAt   string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return clickStackTokenBundle{}, err
	}
	bundle := clickStackTokenBundle{
		Access:      strings.TrimSpace(payload.Access),
		Refresh:     strings.TrimSpace(payload.Refresh),
		ClientID:    strings.TrimSpace(payload.ClientID),
		RedirectURI: strings.TrimSpace(payload.RedirectURI),
	}
	if bundle.Access == "" {
		return clickStackTokenBundle{}, fmt.Errorf("clickstack token bundle is missing access token")
	}
	if payload.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, payload.ExpiresAt)
		if err != nil {
			return clickStackTokenBundle{}, fmt.Errorf("clickstack token bundle has invalid expires_at: %w", err)
		}
		bundle.ExpiresAt = parsed.UTC()
	}
	return bundle, nil
}

func encodeClickStackTokenBundle(bundle clickStackTokenBundle) (string, error) {
	if strings.TrimSpace(bundle.Refresh) == "" && bundle.ClientID == "" && bundle.RedirectURI == "" && bundle.ExpiresAt.IsZero() {
		return bundle.Access, nil
	}
	payload := map[string]string{"access": bundle.Access}
	if bundle.Refresh != "" {
		payload["refresh"] = bundle.Refresh
	}
	if bundle.ClientID != "" {
		payload["client_id"] = bundle.ClientID
	}
	if bundle.RedirectURI != "" {
		payload["redirect_uri"] = bundle.RedirectURI
	}
	if !bundle.ExpiresAt.IsZero() {
		payload["expires_at"] = bundle.ExpiresAt.UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func clickStackExpiresAt(access string, expiresIn int) time.Time {
	if expiresIn > 0 {
		return time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)
	}
	if exp, ok := accessTokenExpiresAt(access); ok {
		return exp
	}
	return time.Time{}
}

func accessTokenExpiresAt(access string) (time.Time, bool) {
	parts := strings.Split(strings.TrimSpace(access), ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0).UTC(), true
}

func decodeStoredToken(value string) string {
	bundle, err := parseClickStackTokenBundle(value)
	if err != nil {
		return strings.TrimSpace(value)
	}
	return bundle.Access
}

func encodeStoredToken(access, refresh string) (string, error) {
	return encodeClickStackTokenBundle(clickStackTokenBundle{Access: access, Refresh: refresh})
}
