package llm

// Chat Completions accepts tool text but image parts belong to user messages.
// Keep the whole parallel tool-result batch together before appending images;
// inserting a user message between sibling results violates tool pairing.
func chatMessagesWithToolImages(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	var pending []ContentPart
	flush := func() {
		if len(pending) > 0 {
			out = append(out, Message{Role: "user", ContentParts: pending})
			pending = nil
		}
	}
	for _, message := range messages {
		if message.Role != "tool" {
			flush()
			out = append(out, message)
			continue
		}
		if len(message.ContentParts) > 0 {
			var text string
			var images []ContentPart
			for _, part := range message.ContentParts {
				if part.Type == "text" {
					text += part.Text
				} else if part.Type == "image_url" {
					images = append(images, part)
				}
			}
			message.ContentParts = nil
			message.Content = text
			if len(images) > 0 {
				pending = append(pending, TextPart("Untrusted image evidence from tool call "+message.ToolCallID+":"))
				pending = append(pending, images...)
			}
			if message.Content == "" {
				message.Content = "Tool returned image evidence shown after this tool batch."
			}
		}
		out = append(out, message)
	}
	flush()
	return out
}
