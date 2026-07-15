package reminder

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	reminderStore "github.com/wati/oncall-agent/internal/reminder"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type CreateTool struct{ Store reminderStore.Store }

func (CreateTool) IsWrite() bool { return true }
func (t CreateTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec("reminder-create", "", registry.ObjectSchema([]string{"run_at", "message"}, map[string]any{"run_at": map[string]any{"type": "string", "description": ""}, "message": map[string]any{"type": "string", "description": ""}}))
}
func (t CreateTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var a struct {
		RunAt   string `json:"run_at"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return registry.Result{}, err
	}
	runAt, err := time.Parse(time.RFC3339, a.RunAt)
	if err != nil {
		return registry.Result{}, fmt.Errorf("run_at must be an RFC3339 timestamp with timezone: %w", err)
	}
	if !runAt.After(time.Now()) {
		return registry.Result{}, fmt.Errorf("run_at must be in the future")
	}
	if strings.TrimSpace(a.Message) == "" {
		return registry.Result{}, fmt.Errorf("message is required")
	}
	if rt.UserID == "" || rt.Channel == "" {
		return registry.Result{}, fmt.Errorf("reminders require a Slack user and channel")
	}
	id, err := newID()
	if err != nil {
		return registry.Result{}, err
	}
	r, err := t.Store.Create(ctx, reminderStore.Reminder{ID: id, UserID: rt.UserID, Channel: rt.Channel, ThreadTS: rt.ThreadTS, Message: strings.TrimSpace(a.Message), RunAt: runAt.UTC()})
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: fmt.Sprintf("提醒已创建：ID %s，将在 %s 提醒“%s”。", r.ID, r.RunAt.Format(time.RFC3339), r.Message)}, nil
}

type ListTool struct{ Store reminderStore.Store }

func (ListTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec("reminder-list", "", registry.ObjectSchema(nil, map[string]any{}))
}
func (t ListTool) Execute(ctx context.Context, _ json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	all, err := t.Store.List(ctx, rt.UserID)
	if err != nil {
		return registry.Result{}, err
	}
	if len(all) == 0 {
		return registry.Result{Content: "没有待执行的提醒。"}, nil
	}
	var lines []string
	for _, r := range all {
		lines = append(lines, fmt.Sprintf("- %s：%s — %s", r.ID, r.RunAt.Format(time.RFC3339), r.Message))
	}
	return registry.Result{Content: strings.Join(lines, "\n")}, nil
}

type CancelTool struct{ Store reminderStore.Store }

func (CancelTool) IsWrite() bool { return true }
func (CancelTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec("reminder-cancel", "", registry.ObjectSchema([]string{"id"}, map[string]any{"id": map[string]any{"type": "string", "description": ""}}))
}
func (t CancelTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return registry.Result{}, err
	}
	if err := t.Store.Cancel(ctx, a.ID, rt.UserID); err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: "提醒已取消。"}, nil
}
func newID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "r-" + hex.EncodeToString(b), nil
}
