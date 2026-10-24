package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// initializeSession commits imported history and its completion marker together.
// SessionStarted is only an intent. Neither it nor failed-turn telemetry proves
// history was loaded. Session ownership serializes initialization with turns.
func (r *Runtime) initializeSession(ctx context.Context, request TurnRequest, events []transcript.Event) error {
	started := false
	var legacyHistory []model.Message
	for _, event := range events {
		switch event.Type {
		case transcript.SessionInitialized, transcript.TurnStarted, transcript.CompactionCreated:
			// Old runtimes imported history before starting their first turn. Keep
			// existing canonical sessions authoritative without fetching Slack again.
			return nil
		case transcript.SessionStarted:
			started = true
		case transcript.UserInput, transcript.AssistantMessage:
			var source struct {
				Source string `json:"source"`
			}
			if json.Unmarshal(event.Metadata, &source) != nil || source.Source != "imported_history" {
				return nil
			}
			if event.Message != nil {
				legacyHistory = append(legacyHistory, *event.Message)
			}
		}
	}
	if !started {
		if _, err := r.record(ctx, transcript.Event{ID: "session-start:" + request.SessionID, SessionID: request.SessionID, Type: transcript.SessionStarted}); err != nil {
			return err
		}
	}
	history := request.History
	if len(history) == 0 && request.HistoryLoader != nil {
		var err error
		history, err = request.HistoryLoader(ctx)
		if err != nil {
			return fmt.Errorf("load session history: %w", err)
		}
	}
	imported := make([]model.Message, len(history))
	for i := range history {
		imported[i] = history[i].WithoutImages()
		if imported[i].Role != model.RoleUser && imported[i].Role != model.RoleAssistant {
			imported[i].Role = model.RoleUser
		}
	}
	// Recover an interrupted import written by an old runtime only when its
	// durable prefix matches the loaded history exactly. Ambiguity fails closed.
	if len(legacyHistory) > len(imported) {
		return fmt.Errorf("legacy history prefix exceeds loaded history")
	}
	for i := range legacyHistory {
		old, _ := json.Marshal(legacyHistory[i])
		current, _ := json.Marshal(imported[i])
		if string(old) != string(current) {
			return fmt.Errorf("legacy history prefix differs at message %d", i)
		}
	}
	batch := make([]transcript.Event, 0, len(imported)-len(legacyHistory)+1)
	metadata := json.RawMessage(`{"source":"imported_history"}`)
	for i := len(legacyHistory); i < len(imported); i++ {
		kind := transcript.UserInput
		if imported[i].Role == model.RoleAssistant {
			kind = transcript.AssistantMessage
		}
		batch = append(batch, transcript.Event{ID: fmt.Sprintf("history:%s:%d", request.SessionID, i), SessionID: request.SessionID, Type: kind, Message: &imported[i], Metadata: metadata, Timestamp: r.deps.Clock()})
	}
	complete := transcript.Event{ID: "session-initialized:" + request.SessionID, SessionID: request.SessionID, Type: transcript.SessionInitialized, Timestamp: r.deps.Clock()}
	if len(batch) == 0 {
		_, err := r.record(ctx, complete)
		return err
	}
	store, ok := r.deps.Transcript.(transcript.BatchStore)
	if !ok {
		return fmt.Errorf("history import requires an atomic transcript batch store")
	}
	batch = append(batch, complete)
	stored, err := store.AppendBatch(ctx, batch)
	if err != nil {
		return fmt.Errorf("commit session history: %w", err)
	}
	for _, event := range stored {
		r.eventMu.Lock()
		if cached, ok := r.eventCache[request.SessionID]; ok {
			cached.append(event)
		}
		r.eventMu.Unlock()
		r.emit(ctx, event)
	}
	return nil
}
