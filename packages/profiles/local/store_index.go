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
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"golang.org/x/sys/unix"
)

const maxCachedSessionIndexes = 64

type eventLocation struct {
	turnID   string
	offset   int64
	length   int
	sequence uint64
}

// Only offsets and IDs are cached, never message bodies. The JSONL file is
// authoritative; an evicted index is rebuilt on the next access.
type sessionIndex struct {
	mu sync.Mutex // pairs with the OS lock to synchronize Go memory access
	indexData
}

type indexData struct {
	coverage     uint64
	currentInput uint64
	taskState    uint64
	info         os.FileInfo
	offset       int64
	sequence     uint64
	entries      []eventLocation
	byID         map[string]int
}

type cachedIndex struct {
	index *sessionIndex
	used  uint64
}

func (s *JSONLStore) sessionIndex(id string) *sessionIndex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.indexes == nil {
		s.indexes = make(map[string]cachedIndex)
	}
	s.access++
	entry, ok := s.indexes[id]
	if !ok {
		if len(s.indexes) >= maxCachedSessionIndexes {
			var oldest string
			age := s.access
			for key, candidate := range s.indexes {
				if candidate.used < age {
					oldest, age = key, candidate.used
				}
			}
			delete(s.indexes, oldest)
		}
		entry.index = &sessionIndex{}
	}
	entry.used = s.access
	s.indexes[id] = entry
	return entry.index
}

func (index *sessionIndex) add(event transcript.Event, offset int64, length int) {
	id, sequence := event.ID, event.Sequence
	if event.Type == transcript.TaskStateUpdated {
		index.taskState = sequence
	}
	if coverage := transcript.CheckpointCoverage(event); coverage > 0 {
		index.coverage = coverage
	}
	if event.Type == transcript.UserInput || event.Type == transcript.SteeringInput {
		index.currentInput = sequence
	}
	if index.byID == nil {
		index.byID = make(map[string]int)
	}
	if id != "" {
		if _, exists := index.byID[id]; !exists {
			index.byID[id] = len(index.entries)
		}
	}
	index.entries = append(index.entries, eventLocation{turnID: event.TurnID, offset: offset, length: length, sequence: sequence})
	index.offset, index.sequence = offset+int64(length), sequence
}

func (index *sessionIndex) refresh(ctx context.Context, file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if index.info == nil || !os.SameFile(index.info, info) || info.Size() < index.offset ||
		(info.Size() == index.offset && !info.ModTime().Equal(index.info.ModTime())) {
		index.indexData = indexData{}
	}
	if index.info != nil && info.Size() == index.offset {
		return nil
	}
	if _, err := file.Seek(index.offset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if len(line) == 0 {
			break
		}
		complete := line[len(line)-1] == '\n'
		trimmed := bytes.TrimSpace(line)
		var header struct {
			ID       string               `json:"id"`
			TurnID   string               `json:"turn_id"`
			Type     transcript.EventType `json:"type"`
			Metadata json.RawMessage      `json:"metadata"`
			Message  *json.RawMessage     `json:"message"`
			Sequence uint64               `json:"sequence"`
		}
		if len(trimmed) > 0 {
			if err := json.Unmarshal(trimmed, &header); err != nil {
				if errors.Is(readErr, io.EOF) && !complete {
					if err := file.Truncate(index.offset); err != nil {
						return err
					}
					if err := file.Sync(); err != nil {
						return err
					}
					break
				}
				return fmt.Errorf("decode transcript: %w", err)
			}
			if header.Sequence <= index.sequence {
				return fmt.Errorf("transcript sequence %d follows %d", header.Sequence, index.sequence)
			}
		}
		// A valid final JSON value without a newline must be separated from
		// the next append. Keep it; only a malformed, incomplete tail is lost.
		if !complete {
			if _, err := file.WriteAt([]byte{'\n'}, index.offset+int64(len(line))); err != nil {
				return err
			}
			if err := file.Sync(); err != nil {
				return err
			}
			line = append(line, '\n')
		}
		if len(trimmed) > 0 {
			event := transcript.Event{ID: header.ID, Sequence: header.Sequence, TurnID: header.TurnID, Type: header.Type, Metadata: header.Metadata}
			if header.Message != nil {
				event.Message = &model.Message{}
			}
			index.add(event, index.offset, len(line))
		} else {
			index.offset += int64(len(line))
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	index.info, err = file.Stat()
	return err
}

func readEvent(file *os.File, location eventLocation) (transcript.Event, error) {
	data := make([]byte, location.length)
	if _, err := file.ReadAt(data, location.offset); err != nil {
		return transcript.Event{}, err
	}
	return transcript.DecodeEvent(data)
}

func lockSessionFile(ctx context.Context, directory string) (func(), error) {
	return lockLocalFile(ctx, filepath.Join(directory, ".events.lock"))
}

func lockLocalFile(ctx context.Context, path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			_ = file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
