package localtools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

var memoryFileMu sync.Mutex

// MemoryRecord stores an atomic cross-session fact or user preference keyed by
// semantic_key, aligned with docs/long-term-memory.md.
type MemoryRecord struct {
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
			`{"action":{"type":"string","enum":["list","search","upsert","forget"]},"semantic_key":{"type":"string"},"kind":{"type":"string","enum":["preference","project","procedure","fact"]},"content":{"type":"string"},"query":{"type":"string"}}`,
			"action",
		),
		Effects:  []tool.Effect{tool.EffectRead},
		Tags:     []string{tool.CategoryWorkspace, "memory"},
		Parallel: true,
	}
}

func (t MemoryTool) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
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
	memoryFileMu.Lock()
	defer memoryFileMu.Unlock()

	records, err := loadMemoryRecords(path)
	if err != nil {
		return tool.Result{}, err
	}
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
				SemanticKey:     key,
				Kind:            kind,
				Content:         content,
				SourceSessionID: call.Scope.SessionID,
				SourceTurnID:    call.Scope.TurnID,
				UpdatedAt:       now,
				Version:         1,
			})
		}
		if err := saveMemoryRecords(path, records); err != nil {
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
		if err := saveMemoryRecords(path, filtered); err != nil {
			return tool.Result{}, err
		}
		return tool.TextResult(fmt.Sprintf("Forgot memory %q.", key)), nil
	default:
		return tool.Result{}, fmt.Errorf("unsupported memory action %q", args.Action)
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
