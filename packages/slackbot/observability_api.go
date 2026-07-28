package slackbot

import "net/http"

// ObservabilityAPI owns the operational HTTP route registration. It keeps the
// composition root focused on dependency wiring rather than endpoint topology.
type ObservabilityAPI struct{ server *Server }

func newObservabilityAPI(server *Server) *ObservabilityAPI { return &ObservabilityAPI{server: server} }

func (a *ObservabilityAPI) Register(mux *http.ServeMux) {
	s := a.server
	mux.Handle("/metrics", s.observabilityHandler(s.metrics))
	mux.HandleFunc("/health/dashboard", s.handleHealthDashboard)
	mux.HandleFunc("/health/tools", s.handleToolHealth)
	mux.HandleFunc("/runs", s.handleRuns)
	mux.HandleFunc("/runs/", s.handleRun)
}
