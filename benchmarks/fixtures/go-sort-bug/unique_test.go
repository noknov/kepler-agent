package unique

import (
	"reflect"
	"testing"
)

func TestStableUnique(t *testing.T) {
	want := []string{"b", "a", "c"}
	if got := StableUnique([]string{"b", "a", "b", "c", "a"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("StableUnique() = %#v, want %#v", got, want)
	}
}
