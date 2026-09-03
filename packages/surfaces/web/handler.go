package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

type Handler struct {
	Auth          *AuthService
	Conversations *ConversationService
	Store         Store
	Brand         Brand
	assets        http.Handler
	index         []byte
	staticDir     string
}

type Brand struct {
	Name string `json:"name"`
}

func NewHandler(auth *AuthService, conversations *ConversationService, store Store, staticDir string) (*Handler, error) {
	h := &Handler{Auth: auth, Conversations: conversations, Store: store}
	if dir := strings.TrimSpace(staticDir); dir != "" {
		indexPath := filepath.Join(dir, "index.html")
		if st, err := os.Stat(indexPath); err == nil && !st.IsDir() {
			h.staticDir = dir
			h.assets = http.StripPrefix("/assets/", http.FileServer(http.Dir(dir)))
			slog.Info("web static assets loaded from disk", "dir", dir)
			return h, nil
		}
		slog.Warn("web static dir is invalid, using embedded assets", "dir", dir)
	}
	assets, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, err
	}
	h.assets = http.StripPrefix("/assets/", http.FileServer(http.FS(assets)))
	h.index = index
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.securityHeaders(w, r)
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	if path == "/api/auth/providers" {
		providers := make([]map[string]string, 0, len(h.Auth.Providers))
		for name := range h.Auth.Providers {
			providers = append(providers, map[string]string{"id": name, "label": "Continue with Slack"})
		}
		writeJSON(w, http.StatusOK, map[string]any{"providers": providers})
		return
	}
	if path == "/api/brand" {
		writeJSON(w, http.StatusOK, map[string]any{"brand": h.Brand})
		return
	}
	if strings.HasPrefix(path, "/auth/") {
		h.handleAuth(w, r, strings.Split(strings.TrimPrefix(path, "/auth/"), "/"))
		return
	}
	if path == "/api/session" {
		h.Auth.Require(http.HandlerFunc(h.handleSession)).ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(path, "/api/") {
		h.Auth.Require(http.HandlerFunc(h.handleAPI)).ServeHTTP(w, r)
		return
	}
	if path == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if h.staticDir != "" {
			content, err := os.ReadFile(filepath.Join(h.staticDir, "index.html"))
			if err != nil {
				http.Error(w, "index not found", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(content)
			return
		}
		_, _ = w.Write(h.index)
		return
	}
	if strings.HasPrefix(path, "/assets/") {
		h.assets.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

func (h *Handler) handleAuth(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "start":
		h.Auth.HandleStart(w, r, parts[0])
	case "callback":
		h.Auth.HandleCallback(w, r, parts[0])
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	auth, _ := AuthFromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"user": auth.Session.Identity, "csrfToken": auth.CSRF, "expiresAt": auth.Session.ExpiresAt})
}

func (h *Handler) handleAPI(w http.ResponseWriter, r *http.Request) {
	auth, _ := AuthFromContext(r.Context())
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	if path == "logout" {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
			return
		}
		h.Auth.HandleLogout(w, r)
		return
	}
	parts := strings.Split(path, "/")
	if parts[0] != "conversations" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 {
		h.handleConversationCollection(w, r, auth.Session.Identity)
		return
	}
	conversationID := parts[1]
	if !isWebSession(conversationID) {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 {
		h.handleConversation(w, r, auth.Session.Identity, conversationID)
		return
	}
	switch parts[2] {
	case "messages":
		h.handleMessages(w, r, auth.Session.Identity, conversationID)
	case "events":
		h.handleEvents(w, r, auth.Session.Identity, conversationID)
	case "turns":
		h.handleTurns(w, r, auth.Session.Identity, conversationID, parts[3:])
	case "approvals":
		h.handleApprovals(w, r, auth.Session.Identity, conversationID)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleConversationCollection(w http.ResponseWriter, r *http.Request, owner Identity) {
	switch r.Method {
	case http.MethodGet:
		archived := r.URL.Query().Get("archived") == "true"
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 100 {
			limit = 50
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		conversations, err := h.Conversations.List(r.Context(), owner, archived, limit+1, offset)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "storage_error", "Conversations could not be loaded")
			return
		}
		if conversations == nil {
			conversations = []Conversation{}
		}
		hasMore := len(conversations) > limit
		if hasMore {
			conversations = conversations[:limit]
		}
		writeJSON(w, http.StatusOK, map[string]any{"conversations": conversations, "hasMore": hasMore})
	case http.MethodPost:
		conversation, err := h.Conversations.Create(r.Context(), owner)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "storage_error", "Conversation could not be created")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"conversation": conversation})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
}

func (h *Handler) handleConversation(w http.ResponseWriter, r *http.Request, owner Identity, id string) {
	switch r.Method {
	case http.MethodGet:
		conversation, err := h.Store.GetConversation(r.Context(), owner, id)
		if err != nil {
			h.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"conversation": conversation})
	case http.MethodPatch:
		var request struct {
			Title    *string `json:"title"`
			Archived *bool   `json:"archived"`
		}
		if err := decodeJSON(r, &request, 8<<10); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if request.Title != nil {
			title := titleFromInput(*request.Title)
			if title == "" {
				writeAPIError(w, http.StatusBadRequest, "invalid_title", "Title is required")
				return
			}
			if err := h.Store.RenameConversation(r.Context(), owner, id, title); err != nil {
				h.writeStoreError(w, err)
				return
			}
		}
		if request.Archived != nil {
			if err := h.Store.ArchiveConversation(r.Context(), owner, id, *request.Archived); err != nil {
				h.writeStoreError(w, err)
				return
			}
		}
		conversation, err := h.Store.GetConversation(r.Context(), owner, id)
		if err != nil {
			h.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"conversation": conversation})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
}

func (h *Handler) handleMessages(w http.ResponseWriter, r *http.Request, owner Identity, id string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	events, err := h.Conversations.Events(r.Context(), owner, id, after)
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (h *Handler) handleTurns(w http.ResponseWriter, r *http.Request, owner Identity, id string, rest []string) {
	if len(rest) == 1 && rest[0] == "stop" {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
			return
		}
		if !h.Conversations.Stop(owner, id) {
			writeAPIError(w, http.StatusConflict, "not_running", "No active turn was found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(rest) != 0 || r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	var request struct {
		RequestID string `json:"requestId"`
		Message   string `json:"message"`
	}
	if err := decodeJSON(r, &request, 256<<10); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	turnID, err := h.Conversations.StartTurn(r.Context(), owner, id, request.RequestID, request.Message)
	if err != nil {
		h.writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"turnId": turnID})
}

func (h *Handler) handleApprovals(w http.ResponseWriter, r *http.Request, owner Identity, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	var request struct {
		RequestID  string `json:"requestId"`
		TurnID     string `json:"turnId"`
		ToolCallID string `json:"toolCallId"`
		Approved   bool   `json:"approved"`
	}
	if err := decodeJSON(r, &request, 16<<10); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	continuationID, err := h.Conversations.ResolveApproval(r.Context(), owner, id, request.TurnID, request.ToolCallID, request.RequestID, request.Approved)
	if err != nil {
		h.writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"turnId": continuationID})
}

func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request, owner Identity, id string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "streaming_unavailable", "Streaming is unavailable")
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	snapshots, channel, cancel := h.Conversations.Hub.Subscribe(id)
	defer cancel()
	replay, err := h.Conversations.Events(r.Context(), owner, id, after)
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	for _, event := range replay {
		if err := writeSSE(w, event); err != nil {
			return
		}
	}
	for _, event := range snapshots {
		if err := writeSSE(w, event); err != nil {
			return
		}
	}
	flusher.Flush()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-channel:
			if !open || writeSSE(w, event) != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(w io.Writer, event ClientEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if event.Sequence > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", event.Sequence); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "event: kepler\ndata: %s\n\n", payload)
	return err
}

func decodeJSON(r *http.Request, target any, limit int64) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("Request body is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("Request body must contain one JSON object")
	}
	return nil
}

func (h *Handler) writeConversationError(w http.ResponseWriter, err error) {
	if IsNotFound(err) {
		writeAPIError(w, http.StatusNotFound, "conversation_not_found", "Conversation was not found")
		return
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "active turn"), strings.Contains(message, "archived"):
		writeAPIError(w, http.StatusConflict, "conversation_busy", message)
	case strings.Contains(message, "required"), strings.Contains(message, "invalid"), strings.Contains(message, "too long"):
		writeAPIError(w, http.StatusBadRequest, "invalid_request", message)
	default:
		writeAPIError(w, http.StatusInternalServerError, "turn_failed", "The turn could not be started")
	}
}

func (h *Handler) writeStoreError(w http.ResponseWriter, err error) {
	if IsNotFound(err) {
		writeAPIError(w, http.StatusNotFound, "conversation_not_found", "Conversation was not found")
		return
	}
	writeAPIError(w, http.StatusInternalServerError, "storage_error", "The conversation could not be loaded")
}

func (h *Handler) securityHeaders(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' https://*.slack-edge.com https://secure.gravatar.com data:; style-src 'self' https://fonts.googleapis.com; style-src-elem 'self' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self' https://slack.com")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	if h.Auth != nil && h.Auth.secureCookies() {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}
