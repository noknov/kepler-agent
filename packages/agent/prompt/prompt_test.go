package prompt

import "testing"

func TestComposeUsesStableLayerOrder(t *testing.T) {
	composition, err := Compose([]Fragment{
		{ID: "user", Layer: LayerUser, Content: "user"},
		{ID: "core", Layer: LayerCore, Content: "core"},
		{ID: "project-b", Layer: LayerProject, Order: 2, Content: "project-b"},
		{ID: "project-a", Layer: LayerProject, Order: 1, Content: "project-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := composition.Content, "core\n\nproject-a\n\nproject-b\n\nuser"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if composition.Hash == "" {
		t.Fatal("expected composition hash")
	}
}
