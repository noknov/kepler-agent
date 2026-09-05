package localtools

import (
	"reflect"
	"testing"
)

func TestExecArgv(t *testing.T) {
	got, err := execArgv([]string{"git", "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"git", "status"}) {
		t.Fatalf("got %v", got)
	}
	if _, err := execArgv(nil); err == nil {
		t.Fatal("expected error when argv is absent")
	}
}
