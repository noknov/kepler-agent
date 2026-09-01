package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/infra/redisclient"
	"github.com/noknov/kepler-agent/packages/providers"
	"github.com/noknov/kepler-agent/packages/safety"
)

type Gateway struct {
	Store          Store
	PublicBaseURL  string
	SlackClientID  string
	SlackSecret    string
	Access         safety.AccessPolicy
	Allowlist      []string
	WorkerUpstream string
	workerProxy    http.Handler
}

func NewGateway(cfg config.Config, redis *redisclient.Client) (*Gateway, error) {
	g := &Gateway{
		Store:          Store{Redis: redis},
		PublicBaseURL:  strings.TrimRight(cfg.Connections.PublicBaseURL, "/"),
		SlackClientID:  cfg.Connections.SlackClientID,
		SlackSecret:    cfg.Connections.SlackClientSecret,
		Access:         safety.NewAccessPolicy(cfg.Security.AllowedUsers, nil),
		Allowlist:      cfg.Security.AllowedUsers,
		WorkerUpstream: cfg.HTTP.WorkerUpstreamURL,
	}
	if g.WorkerUpstream != "" {
		target, err := url.Parse(g.WorkerUpstream)
		if err != nil {
			return nil, fmt.Errorf("WORKER_UPSTREAM_URL: %w", err)
		}
		g.workerProxy = NewSingleHostProxy(target)
	}
	return g, nil
}

func (g *Gateway) Register(mux *http.ServeMux) {
	mux.HandleFunc("/cli/device", g.handleDevice)
	mux.HandleFunc("/cli/login", g.handleLogin)
	mux.HandleFunc("/cli/oauth/callback", g.handleCallback)
	mux.HandleFunc("/cli/bootstrap", g.withSession(g.proxyWorker))
	mux.Handle("/v1/", g.withSession(g.proxyWorker))
}

type deviceStart struct {
	DeviceCode string `json:"device_code"`
	LoginURL   string `json:"login_url"`
	ExpiresIn  int    `json:"expires_in"`
}

func (g *Gateway) handleDevice(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		g.startDevice(w, r)
	case http.MethodGet:
		g.pollDevice(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (g *Gateway) startDevice(w http.ResponseWriter, r *http.Request) {
	if g.PublicBaseURL == "" || g.SlackClientID == "" || g.SlackSecret == "" {
		http.Error(w, "Slack CLI login is not configured on this gateway", http.StatusServiceUnavailable)
		return
	}
	state, err := randomToken(16)
	if err != nil {
		http.Error(w, "could not start login", http.StatusInternalServerError)
		return
	}
	device, err := g.Store.StartDevice(r.Context(), state)
	if err != nil {
		http.Error(w, "could not start login", http.StatusInternalServerError)
		return
	}
	login, err := url.Parse(g.PublicBaseURL + "/cli/login")
	if err != nil {
		http.Error(w, "invalid public base URL", http.StatusInternalServerError)
		return
	}
	query := login.Query()
	query.Set("device", device)
	login.RawQuery = query.Encode()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(deviceStart{DeviceCode: device, LoginURL: login.String(), ExpiresIn: int(oauthTTL.Seconds())})
}

func (g *Gateway) pollDevice(w http.ResponseWriter, r *http.Request) {
	device := strings.TrimSpace(r.URL.Query().Get("device"))
	if device == "" {
		http.Error(w, "device is required", http.StatusBadRequest)
		return
	}
	record, err := g.Store.ConsumeDevice(r.Context(), device)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch record.Status {
	case "pending":
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "pending"})
	case "error":
		http.Error(w, record.Error, http.StatusForbidden)
	case "complete":
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "complete", "token": record.Token, "user_id": record.UserID})
	default:
		http.Error(w, "unknown login status", http.StatusInternalServerError)
	}
}

func (g *Gateway) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if g.PublicBaseURL == "" || g.SlackClientID == "" || g.SlackSecret == "" {
		http.Error(w, "Slack CLI login is not configured on this gateway", http.StatusServiceUnavailable)
		return
	}
	device := strings.TrimSpace(r.URL.Query().Get("device"))
	if device == "" {
		http.Error(w, "start login from the CLI: kepler-agent login --api-url "+g.PublicBaseURL, http.StatusBadRequest)
		return
	}
	record, err := g.Store.GetDevice(r.Context(), device)
	if err != nil || record.Status != "pending" || record.State == "" {
		http.Error(w, "login request expired or already used", http.StatusBadRequest)
		return
	}
	values := url.Values{
		"client_id":    {g.SlackClientID},
		"user_scope":   {"identity.basic"},
		"redirect_uri": {g.PublicBaseURL + "/cli/oauth/callback"},
		"state":        {record.State},
	}
	http.Redirect(w, r, "https://slack.com/oauth/v2/authorize?"+values.Encode(), http.StatusFound)
}

func (g *Gateway) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if msg := strings.TrimSpace(r.URL.Query().Get("error")); msg != "" {
		pending, _ := g.Store.TakeOAuth(r.Context(), state)
		if pending.DeviceCode != "" {
			_ = g.Store.FailDevice(r.Context(), pending.DeviceCode, "slack oauth: "+msg)
		}
		http.Error(w, "slack oauth: "+msg, http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if state == "" || code == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	pending, err := g.Store.TakeOAuth(r.Context(), state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID, err := exchangeSlackIdentity(r.Context(), g.SlackClientID, g.SlackSecret, code, g.PublicBaseURL+"/cli/oauth/callback")
	if err != nil {
		_ = g.Store.FailDevice(r.Context(), pending.DeviceCode, err.Error())
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if len(g.Allowlist) == 0 || !g.Access.AllowsUser(userID) {
		_ = g.Store.FailDevice(r.Context(), pending.DeviceCode, "this Slack user is not on the Kepler allowlist")
		http.Error(w, "this Slack user is not on the Kepler allowlist", http.StatusForbidden)
		return
	}
	token, err := g.Store.Issue(r.Context(), userID)
	if err != nil {
		_ = g.Store.FailDevice(r.Context(), pending.DeviceCode, "could not issue session")
		http.Error(w, "could not issue session", http.StatusInternalServerError)
		return
	}
	if err := g.Store.CompleteDevice(r.Context(), pending.DeviceCode, token, userID); err != nil {
		http.Error(w, "could not complete login", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<!doctype html><html><body><h1>Kepler CLI logged in</h1><p>You can close this tab and return to the terminal.</p></body></html>")
}

func (g *Gateway) withSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		session, err := g.Store.Lookup(r.Context(), token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Header.Set("X-Kepler-User", session.UserID)
		r.Header.Del("Authorization")
		r.Header.Del("X-Api-Key")
		next(w, r)
	}
}

func (g *Gateway) proxyWorker(w http.ResponseWriter, r *http.Request) {
	if g.workerProxy == nil {
		http.Error(w, "WORKER_UPSTREAM_URL is not configured", http.StatusServiceUnavailable)
		return
	}
	g.workerProxy.ServeHTTP(w, r)
}

type Bootstrap struct {
	Provider        string `json:"provider"`
	Protocol        string `json:"protocol"`
	Model           string `json:"model"`
	AnthropicFlavor string `json:"anthropic_flavor,omitempty"`
	Thinking        string `json:"thinking,omitempty"`
}

func RegisterWorker(mux *http.ServeMux, cfg config.Config) error {
	mux.HandleFunc("/cli/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Bootstrap{
			Provider:        cfg.LLM.Provider,
			Protocol:        "kepler",
			Model:           cfg.LLM.Model,
			AnthropicFlavor: cfg.LLM.AnthropicFlavor,
			Thinking:        cfg.LLM.Thinking,
		})
	})
	hosted, err := providers.New(providers.Config{
		Provider:        cfg.LLM.Provider,
		Protocol:        cfg.LLM.Protocol,
		BaseURL:         cfg.LLM.BaseURL,
		APIKey:          cfg.LLM.APIKey,
		Timeout:         cfg.LLM.Timeout,
		AnthropicFlavor: cfg.LLM.AnthropicFlavor,
		ResponsesModels: cfg.LLM.ResponsesModels,
	})
	if err != nil {
		return err
	}
	mux.HandleFunc("POST "+providers.KeplerGeneratePath, HandleHostedGenerate(hosted, cfg.LLM.Temperature))
	proxy, err := NewLLMUpstreamProxy(cfg.LLM)
	if err != nil {
		return err
	}
	mux.Handle("/v1/", proxy)
	return nil
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

func exchangeSlackIdentity(ctx context.Context, clientID, clientSecret, code, redirectURI string) (string, error) {
	values := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/oauth.v2.access", strings.NewReader(values.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var payload struct {
		OK         bool   `json:"ok"`
		Error      string `json:"error,omitempty"`
		AuthedUser struct {
			ID string `json:"id"`
		} `json:"authed_user"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if !payload.OK || payload.AuthedUser.ID == "" {
		if payload.Error != "" {
			return "", fmt.Errorf("slack oauth failed: %s", payload.Error)
		}
		return "", fmt.Errorf("slack oauth did not return a user id")
	}
	return payload.AuthedUser.ID, nil
}
