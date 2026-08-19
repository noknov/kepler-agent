package model

import "testing"

func TestWithImagesDedupesExistingURLs(t *testing.T) {
	input := Message{Role: RoleUser, Content: []Content{
		{Type: ContentText, Text: "再看看"},
		{Type: ContentImage, ImageURL: "data:image/png;base64,abc"},
	}}
	history := []Message{{Content: []Content{
		{Type: ContentImage, ImageURL: "data:image/png;base64,abc"},
		{Type: ContentImage, ImageURL: "data:image/png;base64,def"},
	}}}
	got := input.WithImages(CollectImages(history...))
	if len(got.Content) != 3 || got.Content[2].ImageURL != "data:image/png;base64,def" {
		t.Fatalf("content=%+v", got.Content)
	}
}

func TestWithoutImagesRemovesImageBlocks(t *testing.T) {
	message := Message{Content: []Content{
		{Type: ContentText, Text: "pets"},
		{Type: ContentImage, ImageURL: "data:image/png;base64,abc"},
	}}
	got := message.WithoutImages()
	if len(got.Content) != 1 || got.Content[0].Type != ContentText {
		t.Fatalf("content=%+v", got.Content)
	}
}
