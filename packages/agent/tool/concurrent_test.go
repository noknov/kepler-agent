package tool

import (
	"fmt"
	"strings"
	"testing"
)

func TestNormalizePathsDropsBlanksAndDuplicates(t *testing.T) {
	got := NormalizePaths(" a.go ", []string{"b.go", "a.go", " ", "c.go"})
	if strings.Join(got, " ") != "a.go b.go c.go" {
		t.Fatalf("got %v", got)
	}
}

func TestMapOrderedPreservesOrder(t *testing.T) {
	text, err := MapOrdered(3, func(i int) (string, error) {
		return fmt.Sprintf("%d", i), nil
	})
	if err != nil || text != "0\n\n1\n\n2" {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestMapOrderedRunsConcurrently(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_, _ = MapOrdered(2, func(int) (string, error) {
			started <- struct{}{}
			<-release
			return "ok", nil
		})
		close(done)
	}()
	<-started
	<-started
	close(release)
	<-done
}
