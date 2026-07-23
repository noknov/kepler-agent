package app

import (
	"context"
	"time"
)

func (s *Server) Go(fn func(context.Context)) {
	if fn == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn(s.ctx)
	}()
}

func (s *Server) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	if timeout <= 0 {
		<-done
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (s *Server) waitEvents(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	for s.activeEvents > 0 {
		if timeout <= 0 {
			s.eventCond.Wait()
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		timer := time.AfterFunc(remaining, func() {
			s.eventMu.Lock()
			s.eventCond.Broadcast()
			s.eventMu.Unlock()
		})
		s.eventCond.Wait()
		timer.Stop()
	}
	return true
}

func (s *Server) beginEvent() bool {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.draining.Load() {
		return false
	}
	s.activeEvents++
	return true
}

func (s *Server) endEvent() {
	s.eventMu.Lock()
	if s.activeEvents > 0 {
		s.activeEvents--
	}
	s.eventCond.Broadcast()
	s.eventMu.Unlock()
}
