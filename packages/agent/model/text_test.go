package model

import (
	"fmt"
	"strings"
	"testing"
)

func TestTextIncludesOnlyPublicAnswerBlocks(t *testing.T) {
	message := Message{Role: RoleAssistant, Content: []Content{
		{Type: ContentText, Text: "你好"},
		{Type: ContentText, Text: "public progress", Phase: "commentary"},
		{Type: ContentReasoning, Text: "private reasoning"},
		{Type: ContentText, Text: " world🙂", Phase: "final_answer"},
	}}
	if got := message.Text(); got != "你好 world🙂" {
		t.Fatalf("answer changed: %q", got)
	}
	if message.Content[1].Text != "public progress" {
		t.Fatal("source message was mutated")
	}
	for _, block := range message.Content {
		single := Message{Content: []Content{block}}
		want := ""
		if block.Type == ContentText && block.Phase != "commentary" {
			want = block.Text
		}
		if single.Text() != want {
			t.Fatal("single-block projection changed")
		}
	}
}

func BenchmarkMessageText(b *testing.B) {
	for _, blocks := range []int{1, 8, 128} {
		b.Run(fmt.Sprintf("blocks_%d", blocks), func(b *testing.B) {
			message := Message{Content: make([]Content, blocks)}
			text := strings.Repeat("source ", 64)
			for i := range message.Content {
				message.Content[i] = Content{Type: ContentText, Text: text}
			}
			want := len(text) * blocks
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got := len(message.Text()); got != want {
					b.Fatalf("answer length=%d want=%d", got, want)
				}
			}
		})
	}
}
