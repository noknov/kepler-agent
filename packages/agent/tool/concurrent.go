package tool

import (
	"fmt"
	"strings"
	"sync"
)

// maxConcurrentMap limits fan-out within a single multi-file tool call. The
// runtime limits independent tool calls; this closes the second fan-out layer
// so one tool call with a very large paths array cannot exhaust file handles,
// subprocess slots, or goroutines.
const maxConcurrentMap = 8

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
	workers := n
	if workers > maxConcurrentMap {
		workers = maxConcurrentMap
	}
	var next int
	var nextMu sync.Mutex
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for {
				nextMu.Lock()
				i := next
				next++
				nextMu.Unlock()
				if i >= n {
					return
				}
				parts[i], errs[i] = fn(i)
			}
		}()
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
