package tool

import (
	"fmt"
	"strings"
	"sync"
)

// MapOrdered runs fn for each index concurrently and returns results in input
// order. Individual failures are recorded as error text for that slot; the
// combined error is non-nil only when every slot failed.
func MapOrdered(n int, fn func(i int) (string, error)) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("nothing to run")
	}
	if n == 1 {
		text, err := fn(0)
		return text, err
	}
	parts := make([]string, n)
	errs := make([]error, n)
	var wait sync.WaitGroup
	wait.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wait.Done()
			parts[i], errs[i] = fn(i)
		}(i)
	}
	wait.Wait()
	ok := 0
	var first error
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			if first == nil {
				first = errs[i]
			}
			parts[i] = "Error: " + errs[i].Error()
			continue
		}
		ok++
	}
	joined := strings.Join(parts, "\n\n")
	if ok == 0 {
		return joined, first
	}
	return joined, nil
}
