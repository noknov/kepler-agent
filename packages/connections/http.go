package connections

import (
	"net/http"
	"strings"
)

func NewHTTPHandler(service Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/oauth/")
		path = strings.Trim(path, "/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		provider, action := parts[0], parts[1]
		switch action {
		case "connect":
			service.HandleConnect(w, r, provider)
		case "start":
			service.HandleStart(w, r, provider, strings.TrimSpace(r.URL.Query().Get("state")))
		case "callback":
			service.HandleCallback(w, r, provider)
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}
