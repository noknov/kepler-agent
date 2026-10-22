package llm

import "testing"

func TestProviderToolImagePayloads(t *testing.T) {
	message := Message{Role: "tool", ToolCallID: "c", ContentParts: []ContentPart{TextPart("image evidence"), ImageURLPart("data:image/png;base64,eA==")}}
	anthropic := anthropicMessages([]Message{message})
	blocks, ok := anthropic[0].Content[0].Content.([]anthropicBlock)
	if !ok || len(blocks) != 2 || blocks[1].Type != "image" {
		t.Fatalf("Anthropic image result lost: %+v", anthropic)
	}
	responses := responsesInput([]Message{message})
	output, ok := responses[0].(map[string]any)["output"].([]map[string]any)
	if !ok || len(output) != 2 || output[1]["type"] != "input_image" {
		t.Fatalf("Responses image result lost: %+v", responses)
	}
}

func TestChatImagesFollowTheCompleteToolBatch(t *testing.T) {
	messages := []Message{{Role: "tool", ToolCallID: "first", ContentParts: []ContentPart{ImageURLPart("data:image/png;base64,eA==")}}, {Role: "tool", ToolCallID: "second", Content: "ok"}, {Role: "assistant", Content: "answer", Phase: "final_answer"}}
	converted := chatMessagesWithToolImages(messages)
	if len(converted) != 4 || converted[0].Role != "tool" || converted[1].Role != "tool" || converted[2].Role != "user" || converted[3].Role != "assistant" {
		t.Fatalf("tool batch pairing broken: %+v", converted)
	}
	if messages[0].ContentParts == nil {
		t.Fatal("canonical wire input was mutated")
	}
	input := responsesInput([]Message{messages[2]})
	if input[0].(map[string]any)["phase"] != "final_answer" {
		t.Fatal("Responses assistant phase was dropped")
	}
}
