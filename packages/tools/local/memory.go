package localtools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

// MemoryRecord stores an atomic cross-session fact or user preference keyed by
// semantic_key, aligned with docs/long-term-memory.md.
type MemoryRecord struct {
	Namespace       string    `json:"namespace,omitempty"`
	SemanticKey     string    `json:"semantic_key"`
	Kind            string    `json:"kind"`
	Content         string    `json:"content"`
	SourceSessionID string    `json:"source_session_id,omitempty"`
	SourceTurnID    string    `json:"source_turn_id,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
	Version         int       `json:"version"`
}

type MemoryTool struct {
	Workspace local.Workspace
	StorePath string
}

func (MemoryTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{
		Name:        "memory",
		Description: "Manage durable cross-session user preferences, project facts, and reusable procedures. Keep facts atomic and never store credentials or secrets.",
		InputSchema: schema(
			`{"action":{"type":"string","enum":["list","search","upsert","forget"]},"namespace":{"type":"string","enum":["project","user"],"description":"Defaults to this project. Use user only for preferences shared across projects."},"semantic_key":{"type":"string"},"kind":{"type":"string","enum":["preference","project","procedure","fact"]},"content":{"type":"string"},"query":{"type":"string"}}`,
			"action",
		),
		Effects:  []tool.Effect{tool.EffectExternalWrite},
		Tags:     []string{tool.CategoryWorkspace, "memory"},
		Parallel: false,
	}
}

func (t MemoryTool) DescriptorForCall(call tool.Call) (tool.Descriptor, error) {
	var args struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Descriptor{}, err
	}
	d := t.Descriptor()
	if args.Action == "list" || args.Action == "search" {
		d.Effects = []tool.Effect{tool.EffectRead}
		d.Parallel = true
	}
	return d, nil
}

func (t MemoryTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Namespace   string `json:"namespace"`
		Action      string `json:"action"`
		SemanticKey string `json:"semantic_key"`
		Kind        string `json:"kind"`
		Content     string `json:"content"`
		Query       string `json:"query"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	path, err := t.resolvePath()
	if err != nil {
		return tool.Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return tool.Result{}, err
	}
	unlock, err := lockMemoryFile(ctx, path+".lock")
	if err != nil {
		return tool.Result{}, err
	}
	defer unlock()
	namespace := "user"
	if args.Namespace != "user" {
		if args.Namespace != "" && args.Namespace != "project" {
			return tool.Result{}, fmt.Errorf("invalid memory namespace")
		}
		namespace = fmt.Sprintf("project:%x", sha256.Sum256([]byte(filepath.Clean(t.Workspace.Root))))
	}

	records, err := loadMemoryRecords(path)
	if err != nil {
		return tool.Result{}, err
	}
	var others []MemoryRecord
	var selected []MemoryRecord
	for _, record := range records {
		if record.Namespace == namespace || (record.Namespace == "" && namespace == "user") {
			selected = append(selected, record)
		} else {
			others = append(others, record)
		}
	}
	records = selected
	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "list":
		return tool.TextResult(formatMemoryRecords(records)), nil
	case "search":
		q := strings.ToLower(strings.TrimSpace(args.Query))
		if q == "" {
			return tool.TextResult(formatMemoryRecords(records)), nil
		}
		var matched []MemoryRecord
		for _, r := range records {
			corpus := strings.ToLower(r.SemanticKey + " " + r.Kind + " " + r.Content)
			if strings.Contains(corpus, q) {
				matched = append(matched, r)
			}
		}
		return tool.TextResult(formatMemoryRecords(matched)), nil
	case "upsert":
		key := strings.TrimSpace(args.SemanticKey)
		content := strings.TrimSpace(args.Content)
		if key == "" || content == "" {
			return tool.Result{}, fmt.Errorf("semantic_key and content are required for upsert")
		}
		kind := strings.TrimSpace(args.Kind)
		if kind == "" {
			kind = "fact"
		}
		if kind != "fact" && kind != "preference" && kind != "project" && kind != "procedure" {
			return tool.Result{}, fmt.Errorf("invalid memory kind")
		}
		if len(content) > 8000 || len(key) > 200 {
			return tool.Result{}, fmt.Errorf("memory exceeds size limit")
		}
		now := time.Now().UTC()
		updated := false
		for i := range records {
			if records[i].SemanticKey == key {
				records[i].Kind = kind
				records[i].Content = content
				records[i].SourceSessionID = call.Scope.SessionID
				records[i].SourceTurnID = call.Scope.TurnID
				records[i].UpdatedAt = now
				records[i].Version++
				updated = true
				break
			}
		}
		if !updated {
			records = append(records, MemoryRecord{
				Namespace:       namespace,
				SemanticKey:     key,
				Kind:            kind,
				Content:         content,
				SourceSessionID: call.Scope.SessionID,
				SourceTurnID:    call.Scope.TurnID,
				UpdatedAt:       now,
				Version:         1,
			})
		}
		if err := saveMemoryRecords(path, append(others, records...)); err != nil {
			return tool.Result{}, err
		}
		return tool.TextResult(fmt.Sprintf("Saved memory %q (%s).", key, kind)), nil
	case "forget":
		key := strings.TrimSpace(args.SemanticKey)
		if key == "" {
			return tool.Result{}, fmt.Errorf("semantic_key is required for forget")
		}
		filtered := make([]MemoryRecord, 0, len(records))
		removed := false
		for _, r := range records {
			if r.SemanticKey == key {
				removed = true
				continue
			}
			filtered = append(filtered, r)
		}
		if !removed {
			return tool.TextResult(fmt.Sprintf("No memory found for key %q.", key)), nil
		}
		if err := saveMemoryRecords(path, append(others, filtered...)); err != nil {
			return tool.Result{}, err
		}
		return tool.TextResult(fmt.Sprintf("Forgot memory %q.", key)), nil
	default:
		return tool.Result{}, fmt.Errorf("unsupported memory action %q", args.Action)
	}
}

func lockMemoryFile(ctx context.Context, path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
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

func (t MemoryTool) resolvePath() (string, error) {
	if strings.TrimSpace(t.StorePath) != "" {
		return t.StorePath, nil
	}
	stateDir, err := local.DefaultStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "memories.json"), nil
}

func loadMemoryRecords(path string) ([]MemoryRecord, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []MemoryRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].SemanticKey < records[j].SemanticKey
	})
	return records, nil
}

func saveMemoryRecords(path string, records []MemoryRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].SemanticKey < records[j].SemanticKey
	})
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, data, 0o600)
}

func formatMemoryRecords(records []MemoryRecord) string {
	if len(records) == 0 {
		return "No stored memories."
	}
	var b strings.Builder
	b.WriteString("Stored memories:\n")
	for _, r := range records {
		fmt.Fprintf(&b, "- [%s] %s: %s (v%d, updated %s)\n", r.Kind, r.SemanticKey, r.Content, r.Version, r.UpdatedAt.Format("2006-01-02"))
	}
	return strings.TrimSpace(b.String())
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".agent-write-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	cleanup := func() { _ = file.Close(); _ = os.Remove(temporary) }
	if err := file.Chmod(mode); err != nil {
		cleanup()
		return err
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
