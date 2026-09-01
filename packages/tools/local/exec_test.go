package localtools

import (
	"reflect"
	"testing"
)

func TestExecArgv(t *testing.T) {
	got, err := execArgv("ls -la | wc -l", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/bash", "-lc", "ls -la | wc -l"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	got, err = execArgv("", []string{"git", "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"git", "status"}) {
		t.Fatalf("got %v", got)
	}
	if _, err := execArgv("ls", []string{"git"}); err == nil {
		t.Fatal("expected error when both command and argv are set")
	}
	if _, err := execArgv("", nil); err == nil {
		t.Fatal("expected error when neither command nor argv is set")
	}
}
