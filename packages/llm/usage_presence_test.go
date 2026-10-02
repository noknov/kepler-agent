package llm

import (
	"encoding/json"
	"testing"
)

func TestUsageDistinguishesMissingFromExplicitZero(t *testing.T) {
	for _, payload := range []struct {
		data string
		want bool
	}{
		{`{}`, false}, {`null`, false}, {`{"input_tokens":0,"output_tokens":0,"prompt_tokens":0,"completion_tokens":0}`, true},
	} {
		var responses responsesUsage
		var chat openAIUsage
		var anthropic anthropicUsageDetails
		for _, target := range []any{&responses, &chat, &anthropic} {
			if err := json.Unmarshal([]byte(payload.data), target); err != nil {
				t.Fatal(err)
			}
		}
		if responses.toUsage().Reported != payload.want || chat.toUsage().Reported != payload.want || anthropic.Reported != payload.want {
			t.Fatalf("presence for %s: responses=%v chat=%v anthropic=%v", payload.data, responses.Reported, chat.Reported, anthropic.Reported)
		}
	}
}
