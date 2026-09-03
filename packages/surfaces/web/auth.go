package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	authStateTTL = 10 * time.Minute
	maxAuthAge   = 31 * 24 * time.Hour
)

type AuthService struct {
	Store         Store
	Providers     map[string]IdentityProvider
	AllowedUsers  map[string]bool
	PublicBaseURL *url.URL
	SessionSecret []byte
	SessionTTL    time.Duration
	Clock         func() time.Time
}

type AuthContext struct {
	Session BrowserSession
	Token   string
	CSRF    string
}

type authContextKey struct{}

func NewAuthService(store Store, publicBaseURL, secret string, ttl time.Duration, allowedUsers []string, providers ...IdentityProvider) (*AuthService, error) {
	base, err := url.Parse(publicBaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("WEB_PUBLIC_BASE_URL must be an absolute origin URL")
	}
	if base.Scheme != "https" && !isLoopbackHost(base.Hostname()) {
		return nil, fmt.Errorf("WEB_PUBLIC_BASE_URL must use HTTPS outside loopback development")
	}
	if len(secret) < 32 {
		return nil, fmt.Errorf("web session secret must contain at least 32 characters")
	}
	if ttl <= 0 || ttl > maxAuthAge {
		return nil, fmt.Errorf("web session TTL must be between one second and 31 days")
	}
	service := &AuthService{
		Store: store, Providers: make(map[string]IdentityProvider), AllowedUsers: make(map[string]bool),
		PublicBaseURL: base, SessionSecret: []byte(secret), SessionTTL: ttl, Clock: time.Now,
	}
	for _, user := range allowedUsers {
		if user = strings.TrimSpace(user); user != "" {
			service.AllowedUsers[user] = true
		}
	}
	for _, provider := range providers {
		if provider != nil && provider.Name() != "" {
			service.Providers[provider.Name()] = provider
		}
	}
	return service, nil
}

func (s *AuthService) HandleStart(w http.ResponseWriter, r *http.Request, providerName string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	provider := s.Providers[providerName]
	if provider == nil {
		http.NotFound(w, r)
		return
	}
	state, err := randomToken(32)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "auth_unavailable", "Sign in is temporarily unavailable")
		return
	}
	nonce, err := randomToken(24)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "auth_unavailable", "Sign in is temporarily unavailable")
		return
	}
	verifier, err := randomToken(48)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "auth_unavailable", "Sign in is temporarily unavailable")
		return
	}
	returnTo := safeReturnTo(r.URL.Query().Get("return_to"))
	if err := s.Store.CreateAuthState(r.Context(), HashOpaque(state), AuthState{Provider: providerName, Nonce: nonce, CodeVerifier: verifier, ReturnTo: returnTo, ExpiresAt: s.now().Add(authStateTTL)}); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "auth_unavailable", "Sign in is temporarily unavailable")
		return
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	authorizeURL, err := provider.AuthorizationURL(r.Context(), state, nonce, base64.RawURLEncoding.EncodeToString(challengeBytes[:]))
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "provider_unavailable", "Slack sign in is temporarily unavailable")
		return
	}
	s.setAuthStateCookie(w, state, s.now().Add(authStateTTL))
	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

func (s *AuthService) HandleCallback(w http.ResponseWriter, r *http.Request, providerName string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	provider := s.Providers[providerName]
	if provider == nil {
		http.NotFound(w, r)
		return
	}
	if r.FormValue("error") != "" {
		s.redirectAuthFailure(w, r, "access_denied")
		return
	}
	stateRaw := strings.TrimSpace(r.FormValue("state"))
	code := strings.TrimSpace(r.FormValue("code"))
	if stateRaw == "" || code == "" {
		s.redirectAuthFailure(w, r, "invalid_response")
		return
	}
	stateCookie, err := r.Cookie(s.authStateCookieName())
	if err != nil || len(stateCookie.Value) != len(stateRaw) || subtle.ConstantTimeCompare([]byte(stateCookie.Value), []byte(stateRaw)) != 1 {
		s.redirectAuthFailure(w, r, "invalid_response")
		return
	}
	s.clearAuthStateCookie(w)
	state, err := s.Store.ConsumeAuthState(r.Context(), HashOpaque(stateRaw), s.now())
	if err != nil || state.Provider != providerName {
		s.redirectAuthFailure(w, r, "expired_state")
		return
	}
	identity, err := provider.Exchange(r.Context(), code, state.CodeVerifier, state.Nonce)
	if err != nil {
		s.redirectAuthFailure(w, r, "provider_error")
		return
	}
	if !s.AllowedUsers[identity.SubjectID] {
		s.redirectAuthFailure(w, r, "not_allowed")
		return
	}
	token, err := randomToken(48)
	if err != nil {
		s.redirectAuthFailure(w, r, "session_error")
		return
	}
	now := s.now()
	session := BrowserSession{Identity: identity, CreatedAt: now, ExpiresAt: now.Add(s.SessionTTL)}
	if err := s.Store.CreateSession(r.Context(), HashOpaque(token), session); err != nil {
		s.redirectAuthFailure(w, r, "session_error")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: token, Path: "/", HttpOnly: true, Secure: s.secureCookies(),
		SameSite: http.SameSiteLaxMode, MaxAge: int(s.SessionTTL.Seconds()), Expires: session.ExpiresAt,
	})
	http.Redirect(w, r, safeReturnTo(state.ReturnTo), http.StatusSeeOther)
}

func (s *AuthService) Authenticate(r *http.Request) (AuthContext, error) {
	cookie, err := r.Cookie(s.cookieName())
	if err != nil || cookie.Value == "" {
		return AuthContext{}, ErrNotFound
	}
	session, err := s.Store.GetSession(r.Context(), HashOpaque(cookie.Value), s.now())
	if err != nil {
		return AuthContext{}, err
	}
	if !s.AllowedUsers[session.Identity.SubjectID] {
		return AuthContext{}, ErrNotFound
	}
	return AuthContext{Session: session, Token: cookie.Value, CSRF: s.csrf(cookie.Value)}, nil
}

func (s *AuthService) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, err := s.Authenticate(r)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				writeAPIError(w, http.StatusServiceUnavailable, "auth_unavailable", "Authentication is temporarily unavailable")
				return
			}
			writeAPIError(w, http.StatusUnauthorized, "authentication_required", "Sign in is required")
			return
		}
		if isMutation(r.Method) && !s.validMutation(r, auth) {
			writeAPIError(w, http.StatusForbidden, "request_rejected", "The request could not be verified")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authContextKey{}, auth)))
	})
}

func AuthFromContext(ctx context.Context) (AuthContext, bool) {
	auth, ok := ctx.Value(authContextKey{}).(AuthContext)
	return auth, ok
}

func (s *AuthService) HandleLogout(w http.ResponseWriter, r *http.Request) {
	auth, ok := AuthFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "authentication_required", "Sign in is required")
		return
	}
	_ = s.Store.DeleteSession(r.Context(), HashOpaque(auth.Token))
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: "", Path: "/", HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	w.WriteHeader(http.StatusNoContent)
}

func (s *AuthService) validMutation(r *http.Request, auth AuthContext) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || origin != s.PublicBaseURL.Scheme+"://"+s.PublicBaseURL.Host {
		return false
	}
	provided := r.Header.Get("X-CSRF-Token")
	return len(provided) == len(auth.CSRF) && subtle.ConstantTimeCompare([]byte(provided), []byte(auth.CSRF)) == 1
}

func (s *AuthService) csrf(token string) string {
	mac := hmac.New(sha256.New, s.SessionSecret)
	_, _ = mac.Write([]byte("csrf\n" + token))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *AuthService) redirectAuthFailure(w http.ResponseWriter, r *http.Request, code string) {
	values := url.Values{"auth_error": {code}}
	http.Redirect(w, r, "/?"+values.Encode(), http.StatusSeeOther)
}

func (s *AuthService) cookieName() string {
	if s.secureCookies() {
		return "__Host-kepler_session"
	}
	return "kepler_session"
}

func (s *AuthService) authStateCookieName() string {
	if s.secureCookies() {
		return "__Host-kepler_oauth"
	}
	return "kepler_oauth"
}

func (s *AuthService) setAuthStateCookie(w http.ResponseWriter, state string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: s.authStateCookieName(), Value: state, Path: "/", HttpOnly: true, Secure: s.secureCookies(),
		SameSite: http.SameSiteLaxMode, MaxAge: int(authStateTTL.Seconds()), Expires: expires,
	})
}

func (s *AuthService) clearAuthStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.authStateCookieName(), Value: "", Path: "/", HttpOnly: true, Secure: s.secureCookies(),
		SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0),
	})
}

func (s *AuthService) secureCookies() bool { return s.PublicBaseURL.Scheme == "https" }

func (s *AuthService) now() time.Time {
	if s.Clock != nil {
		return s.Clock().UTC()
	}
	return time.Now().UTC()
}

func randomToken(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func safeReturnTo(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\r\n") {
		return "/"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return "/"
	}
	return parsed.RequestURI()
}

func isMutation(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
