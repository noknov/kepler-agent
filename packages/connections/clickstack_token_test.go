package connections

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestParseClickStackTokenBundleLegacyAccessOnly(t *testing.T) {
	bundle, err := parseClickStackTokenBundle("plain-access-token")
	if err != nil {
		t.Fatalf("parseClickStackTokenBundle() error = %v", err)
	}
	if bundle.Access != "plain-access-token" || bundle.Refresh != "" {
		t.Fatalf("bundle = %+v", bundle)
	}
}

func TestEncodeDecodeClickStackTokenBundle(t *testing.T) {
	expires := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	original := clickStackTokenBundle{
		Access:      "access-1",
		Refresh:     "refresh-1",
		ClientID:    "client-1",
		RedirectURI: "http://localhost/callback",
		ExpiresAt:   expires,
	}
	raw, err := encodeClickStackTokenBundle(original)
	if err != nil {
		t.Fatalf("encodeClickStackTokenBundle() error = %v", err)
	}
	got, err := parseClickStackTokenBundle(raw)
	if err != nil {
		t.Fatalf("parseClickStackTokenBundle() error = %v", err)
	}
	if got.Access != original.Access || got.Refresh != original.Refresh || got.ClientID != original.ClientID || got.RedirectURI != original.RedirectURI {
		t.Fatalf("bundle = %+v, want %+v", got, original)
	}
	if !got.ExpiresAt.Equal(expires) {
		t.Fatalf("ExpiresAt = %v, want %v", got.ExpiresAt, expires)
	}
	if decodeStoredToken(raw) != "access-1" {
		t.Fatalf("decodeStoredToken() = %q", decodeStoredToken(raw))
	}
}

func TestClickStackTokenBundleNeedsRefresh(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	fresh := clickStackTokenBundle{Access: "token", ExpiresAt: now.Add(30 * time.Minute)}
	if fresh.needsRefresh(now) {
		t.Fatal("expected fresh token not to need refresh")
	}
	stale := clickStackTokenBundle{Access: "token", ExpiresAt: now.Add(2 * time.Minute)}
	if !stale.needsRefresh(now) {
		t.Fatal("expected near-expiry token to need refresh")
	}
}

func TestAccountLabelFromAccessTokenWhenIDTokenMissing(t *testing.T) {
	access := testJWT(t, map[string]any{"sub": "google-oauth2|107375439960855894386"})
	if got := accountLabelFromTokens(access, ""); got != "google-oauth2|107375439960855894386" {
		t.Fatalf("accountLabelFromTokens() = %q", got)
	}
}

func TestAccountLabelPrefersIDTokenEmail(t *testing.T) {
	idToken := testJWT(t, map[string]any{"email": "user@example.com", "sub": "opaque-sub"})
	access := testJWT(t, map[string]any{"sub": "google-oauth2|123"})
	if got := accountLabelFromTokens(access, idToken); got != "user@example.com" {
		t.Fatalf("accountLabelFromTokens() = %q", got)
	}
}

func TestAccountLabelNamespacedEmailClaim(t *testing.T) {
	token := testJWT(t, map[string]any{
		"https://clickhouse.cloud/email": "user@example.com",
		"sub":                            "opaque-sub",
	})
	if got := accountFromJWT(token); got != "user@example.com" {
		t.Fatalf("accountFromJWT() = %q", got)
	}
}

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}

func TestAccessTokenExpiresAtFromJWT(t *testing.T) {
	exp := time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC)
	claims, err := json.Marshal(map[string]any{"exp": exp.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	got, ok := accessTokenExpiresAt(token)
	if !ok || !got.Equal(exp) {
		t.Fatalf("accessTokenExpiresAt() = (%v, %v)", got, ok)
	}
}
