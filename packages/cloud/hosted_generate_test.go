package cloud

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/providers"
)

type stubHostedModel struct {
	temperature *float64
}

func (s *stubHostedModel) Generate(_ context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	s.temperature = request.Temperature
	if sink != nil {
		if err := sink(model.StreamEvent{Type: model.StreamTextDelta, Text: "hi "}); err != nil {
			return model.Response{}, err
		}
	}
	return model.Response{
		Message:      model.TextMessage(model.RoleAssistant, "hi "+request.Model),
		FinishReason: model.FinishStop,
	}, nil
}

func TestHostedGenerateRoundTrip(t *testing.T) {
	stub := &stubHostedModel{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+providers.KeplerGeneratePath, HandleHostedGenerate(stub, nil))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := providers.New(providers.Config{
		Provider: "kepler", Protocol: "kepler", BaseURL: server.URL, Timeout: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []string
	response, err := client.Generate(context.Background(), model.Request{
		Model:    "gpt-5.6-luna",
		Messages: []model.Message{model.TextMessage(model.RoleUser, "hello")},
	}, func(event model.StreamEvent) error {
		if event.Type == model.StreamTextDelta {
			deltas = append(deltas, event.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Message.Text() != "hi gpt-5.6-luna" {
		t.Fatalf("text = %q", response.Message.Text())
	}
	if len(deltas) != 1 || deltas[0] != "hi " {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestHostedGenerateWritesNDJSON(t *testing.T) {
	handler := HandleHostedGenerate(&stubHostedModel{}, nil)
	req := httptest.NewRequest(http.MethodPost, providers.KeplerGeneratePath, bytes.NewReader([]byte(`{"model":"m","messages":[],"temperature":0}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	scanner := bufio.NewScanner(rec.Body)
	var kinds []string
	for scanner.Scan() {
		var line providers.HostedStreamLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, line.Kind)
	}
	if len(kinds) < 2 || kinds[0] != "event" || kinds[len(kinds)-1] != "result" {
		t.Fatalf("kinds = %#v", kinds)
	}
}

func TestHostedGenerateUsesOperatorTemperature(t *testing.T) {
	stub := &stubHostedModel{}
	handler := HandleHostedGenerate(stub, nil)
	req := httptest.NewRequest(http.MethodPost, providers.KeplerGeneratePath, bytes.NewReader([]byte(`{"model":"gpt-5.6-luna","messages":[],"temperature":0}`)))
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if stub.temperature != nil {
		t.Fatalf("temperature = %v, want hosted/Slack value (unset)", *stub.temperature)
	}
}
