package frontmatter

import (
	"reflect"
	"testing"
)

func TestDecodeYAMLFrontMatter(t *testing.T) {
	type metadata struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Tags        []string `yaml:"tags"`
	}
	input := "\ufeff---\r\nname: example\r\ndescription: |\r\n  first line\r\n  second line\r\ntags: [one, two]\r\n---\r\nbody\r\n"
	var got metadata
	body, found, err := Decode(input, &got)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("front matter was not found")
	}
	if body != "body\n" {
		t.Fatalf("body = %q", body)
	}
	want := metadata{Name: "example", Description: "first line\nsecond line\n", Tags: []string{"one", "two"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
}

func TestDecodeLeavesPlainContentUntouched(t *testing.T) {
	const input = "ordinary content\r\n"
	body, found, err := Decode(input, nil)
	if err != nil || found || body != input {
		t.Fatalf("Decode() = (%q, %v, %v)", body, found, err)
	}
}

func TestDecodeRejectsUnterminatedFrontMatter(t *testing.T) {
	if _, found, err := Decode("---\nname: example\n", nil); err == nil || found {
		t.Fatalf("Decode() found=%v err=%v, want not found with error", found, err)
	}
}
