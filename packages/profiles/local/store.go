// Package local contains local CLI implementations for agent contracts.
package local

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type JSONLStore struct {
	Root      string
	mu        sync.Mutex
	sequences map[string]uint64
}

type SessionInfo struct {
	ID       string
	Modified time.Time
}

func (s *JSONLStore) ListSessions() ([]SessionInfo, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, err
	}
	var sessions []SessionInfo
	for _, entry := range entries {
		if !entry.IsDir() || !safeID.MatchString(entry.Name()) {
			continue
		}
		info, err := os.Stat(filepath.Join(s.Root, entry.Name(), "events.jsonl"))
		if err != nil {
			continue
		}
		sessions = append(sessions, SessionInfo{ID: entry.Name(), Modified: info.ModTime()})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Modified.After(sessions[j].Modified) })
	return sessions, nil
}

func NewJSONLStore(root string) (*JSONLStore, error) {
	if root == "" {
		return nil, errors.New("session root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	return &JSONLStore{Root: abs, sequences: make(map[string]uint64)}, nil
}

func (s *JSONLStore) Append(_ context.Context, event transcript.Event) (transcript.Event, error) {
	if !safeID.MatchString(event.SessionID) {
		return transcript.Event{}, fmt.Errorf("invalid session id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sequences[event.SessionID]; !ok {
		events, err := s.loadUnlocked(event.SessionID, 0)
		if err != nil {
			return transcript.Event{}, err
		}
		if len(events) > 0 {
			s.sequences[event.SessionID] = events[len(events)-1].Sequence
		}
	}
	s.sequences[event.SessionID]++
	event.Sequence = s.sequences[event.SessionID]
	directory := filepath.Join(s.Root, event.SessionID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return transcript.Event{}, err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return transcript.Event{}, err
	}
	file, err := os.OpenFile(filepath.Join(directory, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return transcript.Event{}, err
	}
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	return event, err
}

func (s *JSONLStore) Load(_ context.Context, sessionID string, afterSequence uint64) ([]transcript.Event, error) {
	if !safeID.MatchString(sessionID) {
		return nil, fmt.Errorf("invalid session id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked(sessionID, afterSequence)
}

func (s *JSONLStore) loadUnlocked(sessionID string, afterSequence uint64) ([]transcript.Event, error) {
	file, err := os.Open(filepath.Join(s.Root, sessionID, "events.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64<<10)
	var events []transcript.Event
	for {
		line, readErr := reader.ReadBytes('\n')
		complete := len(line) > 0 && line[len(line)-1] == '\n'
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var event transcript.Event
			if unmarshalErr := json.Unmarshal(line, &event); unmarshalErr != nil {
				if errors.Is(readErr, io.EOF) && !complete {
					break
				}
				return nil, fmt.Errorf("decode transcript: %w", unmarshalErr)
			}
			if event.Sequence > afterSequence {
				events = append(events, event)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return events, nil
}
