package web

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/wati/oncall-agent/internal/conversation"
)

//go:embed static
var staticFiles embed.FS

// Server handles all web UI routes: authentication, SSE chat, and static files.
type Server struct {
	hub    *WebHub
	conv   *conversation.Service
	auth   *authHandlers
	models ModelSettings
}

type ModelSettings struct {
	DefaultModel string
	Models       []string
	Get          func(userID string) string
	Set          func(userID, model string) bool
}

// New creates a Server. bot is used only to send OTP DMs; the full Slack client
// satisfies BotMessenger via its PostMessage method.
func New(bot BotMessenger, conv *conversation.Service, hub *WebHub, allowedUsers []string, models ModelSettings) *Server {
	sessions := newSessionStore()
	auth := newAuthHandlers(bot, sessions, allowedUsers)
	return &Server{hub: hub, conv: conv, auth: auth, models: models}
}

// RegisterRoutes adds all web routes to mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	sub, _ := fs.Sub(staticFiles, "static")
	static := http.FileServer(http.FS(sub))

	mux.HandleFunc("/auth/send-code", s.auth.handleSendCode)
	mux.HandleFunc("/auth/verify-code", s.auth.handleVerifyCode)
	mux.HandleFunc("/auth/signout", s.auth.handleSignOut)
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/api/models", s.handleModels)
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.Handle("/", static)
}
