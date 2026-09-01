package cloud

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/providers"
)

func HandleHostedGenerate(client model.Client, temperature *float64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if client == nil {
			http.Error(w, "hosted model is not configured", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			http.Error(w, "read request", http.StatusBadRequest)
			return
		}
		var request model.Request
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, "invalid generate request", http.StatusBadRequest)
			return
		}
		request.Temperature = temperature
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		writeLine := func(line providers.HostedStreamLine) bool {
			if encodeErr := json.NewEncoder(w).Encode(line); encodeErr != nil {
				return false
			}
			if flusher != nil {
				flusher.Flush()
			}
			return true
		}
		response, err := client.Generate(r.Context(), request, func(event model.StreamEvent) error {
			if !writeLine(providers.HostedStreamLine{Kind: "event", Event: &event}) {
				return io.ErrClosedPipe
			}
			return nil
		})
		if err != nil {
			var modelErr *model.Error
			if errors.As(err, &modelErr) {
				_ = writeLine(providers.HostedStreamLine{Kind: "error", Error: modelErr})
				return
			}
			_ = writeLine(providers.HostedStreamLine{Kind: "error", Error: &model.Error{
				Kind:    model.ErrorUnknown,
				Message: err.Error(),
			}})
			return
		}
		_ = writeLine(providers.HostedStreamLine{Kind: "result", Response: &response})
	}
}
