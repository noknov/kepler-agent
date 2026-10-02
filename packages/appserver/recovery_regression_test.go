package appserver

import (
	"sync"
	"testing"
)

func TestCancelAndCompleteRace(t *testing.T) {
	s := &Server{active: map[string]*activeTurn{}}
	for i := 0; i < 1000; i++ {
		s.active["audit"] = &activeTurn{cancel: func() {}, phase: turnRunning}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); s.cancel("audit") }()
		go func() { defer wg.Done(); s.transition("audit", turnCompleting) }()
		wg.Wait()
	}
}
