package cloud

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/providers"
)

type HostedGeneratePolicy struct {
	Model           string
	MaxOutputTokens int
	Temperature     *float64
}

func HandleHostedGenerate(client model.Client, policy HostedGeneratePolicy) http.HandlerFunc {
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
		if policy.Model != "" && request.Model != "" && request.Model != policy.Model {
			http.Error(w, "model is not allowed", http.StatusForbidden)
			return
		}
		if policy.Model != "" {
			request.Model = policy.Model
		}
		if policy.MaxOutputTokens > 0 && (request.MaxOutputTokens <= 0 || request.MaxOutputTokens > policy.MaxOutputTokens) {
			request.MaxOutputTokens = policy.MaxOutputTokens
		}
		request.Temperature = policy.Temperature
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
