// Package local contains local CLI implementations for agent contracts.
package local

import (
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

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type JSONLStore struct {
	Root    string
	mu      sync.Mutex // protects only the bounded index cache, never file I/O
	indexes map[string]cachedIndex
	access  uint64
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
	return &JSONLStore{Root: abs}, nil
}

func (s *JSONLStore) Append(ctx context.Context, event transcript.Event) (transcript.Event, error) {
	file, index, closeSession, err := s.openSession(ctx, event.SessionID)
	if err != nil {
		return transcript.Event{}, err
	}
	defer closeSession()
	if location, ok := index.byID[event.ID]; event.ID != "" && ok {
		stored, err := readEvent(file, index.entries[location])
		if err == nil {
			err = file.Sync()
		}
		return stored, err
	}
	event.Sequence = index.sequence + 1
	data, err := json.Marshal(event)
	if err != nil {
		return transcript.Event{}, err
	}
	data = append(data, '\n')
	if err := ctx.Err(); err != nil {
		return transcript.Event{}, err
	}
	if _, err = file.WriteAt(data, index.offset); err != nil {
		return transcript.Event{}, err
	}
	if err = file.Sync(); err != nil {
		return transcript.Event{}, err
	}
	info, err := file.Stat()
	if err != nil {
		return transcript.Event{}, err
	}
	index.add(event.ID, event.Sequence, index.offset, len(data))
	index.info = info
	return event, nil
}

func (s *JSONLStore) AppendBatch(ctx context.Context, batch []transcript.Event) ([]transcript.Event, error) {
	if len(batch) == 0 {
		return nil, nil
	}
	sessionID := batch[0].SessionID
	for _, event := range batch {
		if event.SessionID != sessionID {
			return nil, fmt.Errorf("batch events must share one session id")
		}
	}
	file, index, closeSession, err := s.openSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	defer closeSession()
	result := make([]transcript.Event, len(batch))
	for i, event := range batch {
		event.Sequence = index.sequence + uint64(i) + 1
		result[i] = event
	}
	// Fork/initialization batches remain atomic. Copy the validated bytes;
	// decoding and re-encoding all prior messages only amplifies the write.
	directory := filepath.Dir(file.Name())
	temporary, err := os.CreateTemp(directory, ".events-*.jsonl")
	if err != nil {
		return nil, err
	}
	defer func() { _ = temporary.Close(); _ = os.Remove(temporary.Name()) }()
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err = io.CopyN(temporary, file, index.offset); err != nil {
		return nil, err
	}
	encoder := json.NewEncoder(temporary)
	for _, event := range result {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := encoder.Encode(event); err != nil {
			return nil, err
		}
	}
	if err := temporary.Sync(); err != nil {
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(temporary.Name(), file.Name()); err != nil {
		return nil, err
	}
	// Make the rename durable, not just the new file's contents.
	dir, err := os.Open(directory)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *JSONLStore) Load(ctx context.Context, sessionID string, afterSequence uint64) ([]transcript.Event, error) {
	file, index, closeSession, err := s.openSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	defer closeSession()
	start := sort.Search(len(index.entries), func(i int) bool { return index.entries[i].sequence > afterSequence })
	var events []transcript.Event
	for _, location := range index.entries[start:] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		event, err := readEvent(file, location)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

// openSession holds the cross-process lock until the caller has committed or
// read its snapshot. Independent sessions never share an I/O mutex.
func (s *JSONLStore) openSession(ctx context.Context, sessionID string) (*os.File, *sessionIndex, func(), error) {
	if !safeID.MatchString(sessionID) {
		return nil, nil, nil, fmt.Errorf("invalid session id")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	directory := filepath.Join(s.Root, sessionID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, nil, nil, err
	}
	unlock, err := lockSessionFile(ctx, directory)
	if err != nil {
		return nil, nil, nil, err
	}
	file, err := os.OpenFile(filepath.Join(directory, "events.jsonl"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		unlock()
		return nil, nil, nil, err
	}
	index := s.sessionIndex(sessionID)
	index.mu.Lock()
	closeSession := func() { index.mu.Unlock(); _ = file.Close(); unlock() }
	if err := index.refresh(ctx, file); err != nil {
		closeSession()
		return nil, nil, nil, err
	}
	return file, index, closeSession, nil
}
