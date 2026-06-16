package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/wati/oncall-agent/internal/conversation"
)

// handleMe returns the authenticated user info as JSON, or 401.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := s.auth.currentUser(r)
	if !ok {
		http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"user_id": user.ID,
	})
}

// handleChat accepts POST {"text":"...","conv_id":"..."} and returns an SSE stream.
//
// Each chunk event carries a single item from the agent's AppendStream calls.
// The client accumulates markdown_text chunks for streaming display and
// renders task_update chunks as the status indicator.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := s.auth.currentUser(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var body struct {
		Text   string `json:"text"`
		ConvID string `json:"conv_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		http.Error(w, "missing text", http.StatusBadRequest)
		return
	}

	convID := body.ConvID
	if convID == "" {
		convID = newID()
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Register the hub channel before starting the agent to buffer any early events.
	ch := s.hub.register(convID)
	defer s.hub.deregister(convID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	// Send conv_id first so the client can store it for follow-up turns.
	writeSSEJSON(w, "init", map[string]string{"conv_id": convID})
	flusher.Flush()

	agentCtx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	go func() {
		s.conv.HandleMention(agentCtx, conversation.Request{
			EventID:  newID(),
			UserID:   user.ID,
			Channel:  "web:" + user.ID,
			ThreadTS: convID,
			Text:     body.Text,
		})
		s.hub.send(convID, hubEvent{Kind: kindDone})
	}()

	for {
		select {
		case event := <-ch:
			switch event.Kind {
			case kindChunks:
				for _, chunk := range event.Chunks {
					writeSSEJSON(w, "chunk", chunk)
				}
				flusher.Flush()
			case kindMessage:
				writeSSEJSON(w, "message", map[string]string{"text": event.Text})
				flusher.Flush()
			case kindDone:
				writeSSEJSON(w, "done", map[string]any{})
				flusher.Flush()
				return
			}
		case <-agentCtx.Done():
			writeSSEJSON(w, "done", map[string]any{})
			flusher.Flush()
			return
		}
	}
}

func writeSSEJSON(w http.ResponseWriter, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
