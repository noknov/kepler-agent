package slackconversation

import (
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

func TestViewingReferenceSurvivesDurablePayloadWithoutChangingIdentity(t *testing.T) {
	original := Request{TeamID: "T1", UserID: "U1", Channel: "D1", ThreadTS: "1.0", Text: "summarize this", ViewingContext: &ViewingContext{TeamID: "T1", ChannelID: "C123"}}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var recovered Request
	if err = json.Unmarshal(data, &recovered); err != nil {
		t.Fatal(err)
	}
	message := recovered.Message()
	if recovered.Channel != "D1" || recovered.UserID != "U1" || message.Text() != original.Text || len(message.Content) != 2 || message.Content[1].Type != model.ContentJSON {
		t.Fatalf("lost data or changed identity: %+v %+v", recovered, message)
	}
	recovered.TeamID = "T2"
	if len(recovered.Message().Content) != 1 {
		t.Fatal("reused reference for another workspace")
	}
}
