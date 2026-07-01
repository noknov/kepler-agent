package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"
)

const sessionCookieName = "oncall_web_sid"
const sessionTTL = 12 * time.Hour
const otpTTL = 5 * time.Minute

// slackUser holds identity information for an authenticated web session.
type slackUser struct {
	ID string
}

// BotMessenger is the minimal interface needed to send a Slack DM.
// *slack.Client already satisfies this via its PostMessage method.
type BotMessenger interface {
	PostMessage(ctx context.Context, channel, threadTS, text string) (string, error)
}

// sessionStore is a non-persistent in-memory session map.
// All sessions disappear on server restart; cookies are session-only
// (no MaxAge/Expires), so closing the browser also clears them.
type sessionStore struct {
	mu      sync.Mutex
	entries map[string]sessionEntry
}

type sessionEntry struct {
	User      slackUser
	ExpiresAt time.Time
}

func newSessionStore() *sessionStore {
	s := &sessionStore{entries: make(map[string]sessionEntry)}
	go s.pruneLoop()
	return s
}

func (s *sessionStore) create(user slackUser) string {
	id := randomToken(32)
	s.mu.Lock()
	s.entries[id] = sessionEntry{User: user, ExpiresAt: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	return id
}

func (s *sessionStore) get(id string) (slackUser, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok || time.Now().After(e.ExpiresAt) {
		delete(s.entries, id)
		return slackUser{}, false
	}
	return e.User, true
}

func (s *sessionStore) delete(id string) {
	s.mu.Lock()
	delete(s.entries, id)
	s.mu.Unlock()
}

func (s *sessionStore) pruneLoop() {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		now := time.Now()
		for id, e := range s.entries {
			if now.After(e.ExpiresAt) {
				delete(s.entries, id)
			}
		}
		s.mu.Unlock()
	}
}

// otpStore holds short-lived one-time passwords keyed by Slack user ID.
type otpStore struct {
	mu      sync.Mutex
	pending map[string]otpEntry
}

type otpEntry struct {
	Code      string
	ExpiresAt time.Time
}

func newOTPStore() *otpStore {
	return &otpStore{pending: make(map[string]otpEntry)}
}

func (s *otpStore) set(userID, code string) {
	s.mu.Lock()
	s.pending[userID] = otpEntry{Code: code, ExpiresAt: time.Now().Add(otpTTL)}
	s.mu.Unlock()
}

func (s *otpStore) verify(userID, code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.pending[userID]
	if !ok || time.Now().After(e.ExpiresAt) {
		delete(s.pending, userID)
		return false
	}
	if e.Code != code {
		return false
	}
	delete(s.pending, userID)
	return true
}

// rateLimiter is a simple per-key token bucket for abuse prevention.
type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]rateLimitEntry
	max     int
	window  time.Duration
}

type rateLimitEntry struct {
	count     int
	windowEnd time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{entries: make(map[string]rateLimitEntry), max: max, window: window}
}

// allow returns true if the key is within the rate limit.
func (r *rateLimiter) allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	e := r.entries[key]
	if now.After(e.windowEnd) {
		e = rateLimitEntry{count: 0, windowEnd: now.Add(r.window)}
	}
	if e.count >= r.max {
		return false
	}
	e.count++
	r.entries[key] = e
	return true
}

// authHandlers manages the OTP-based login flow.
type authHandlers struct {
	bot          BotMessenger
	sessions     *sessionStore
	otps         *otpStore
	allowedUsers map[string]struct{}
	sendLimit    *rateLimiter // max sends per user ID per window
	verifyLimit  *rateLimiter // max verify attempts per user ID per window
}

func newAuthHandlers(bot BotMessenger, sessions *sessionStore, allowedUsers []string) *authHandlers {
	allowed := make(map[string]struct{}, len(allowedUsers))
	for _, u := range allowedUsers {
		if u != "" {
			allowed[u] = struct{}{}
		}
	}
	return &authHandlers{
		bot:          bot,
		sessions:     sessions,
		otps:         newOTPStore(),
		allowedUsers: allowed,
		sendLimit:    newRateLimiter(1, time.Minute),
		verifyLimit:  newRateLimiter(1, time.Minute),
	}
}

// handleSendCode accepts POST user_id=U... and sends a DM with a 6-digit OTP.
func (h *authHandlers) handleSendCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID := r.FormValue("user_id")
	if userID == "" {
		writeAuthJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id required"})
		return
	}
	// Always respond "sent" — never reveal whether a user ID is on the allowlist.
	if len(h.allowedUsers) > 0 {
		if _, ok := h.allowedUsers[userID]; !ok {
			writeAuthJSON(w, http.StatusOK, map[string]string{"status": "sent"})
			return
		}
	}
	// Rate-limit sends to prevent DM spamming allowlisted users.
	if !h.sendLimit.allow(userID) {
		writeAuthJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests, please wait a few minutes"})
		return
	}
	code := randomOTP()
	h.otps.set(userID, code)
	msg := fmt.Sprintf("Your 斗包 web login code: *%s*\n\nThis code expires in 5 minutes.", code)
	if _, err := h.bot.PostMessage(r.Context(), userID, "", msg); err != nil {
		writeAuthJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to send DM, check that the bot is in your workspace"})
		return
	}
	writeAuthJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// handleVerifyCode accepts POST user_id=U...&code=123456 and creates a session on success.
func (h *authHandlers) handleVerifyCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID := r.FormValue("user_id")
	code := r.FormValue("code")
	if userID == "" || code == "" {
		writeAuthJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and code required"})
		return
	}
	// Rate-limit verify attempts to prevent OTP brute force.
	if !h.verifyLimit.allow(userID) {
		writeAuthJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts, please request a new code"})
		return
	}
	if !h.otps.verify(userID, code) {
		writeAuthJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired code"})
		return
	}
	sessionID := h.sessions.create(slackUser{ID: userID})
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPSRequest(r),
		SameSite: http.SameSiteLaxMode,
		// No MaxAge/Expires → session cookie; gone when browser closes.
	})
	writeAuthJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleSignOut clears the session cookie.
func (h *authHandlers) handleSignOut(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		h.sessions.delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPSRequest(r),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// currentUser returns the authenticated user for this request, if any.
func (h *authHandlers) currentUser(r *http.Request) (slackUser, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return slackUser{}, false
	}
	return h.sessions.get(c.Value)
}

func writeAuthJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func isHTTPSRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func randomOTP() string {
	const digits = "0123456789"
	code := make([]byte, 6)
	for i := range code {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		code[i] = digits[n.Int64()]
	}
	return string(code)
}
