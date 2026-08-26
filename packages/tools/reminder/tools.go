package reminder

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	reminderStore "github.com/noknov/kepler-agent/packages/reminder"
)

type CreateTool struct {
	Store    reminderStore.Store
	OnCreate func(context.Context)
}

func (t CreateTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("reminder-create", "", tool.ObjectSchema([]string{"run_at", "message"}, map[string]any{"run_at": map[string]any{"type": "string", "description": ""}, "message": map[string]any{"type": "string", "description": ""}}), tool.ExternalWrite()...)
}
func (t CreateTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var a struct {
		RunAt   string `json:"run_at"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(call.Arguments, &a); err != nil {
		return tool.Result{}, err
	}
	runAt, err := time.Parse(time.RFC3339, a.RunAt)
	if err != nil {
		return tool.Result{}, fmt.Errorf("run_at must be an RFC3339 timestamp with timezone: %w", err)
	}
	if !runAt.After(time.Now()) {
		return tool.Result{}, fmt.Errorf("run_at must be in the future")
	}
	if strings.TrimSpace(a.Message) == "" {
		return tool.Result{}, fmt.Errorf("message is required")
	}
	if call.Scope.UserID == "" || call.Scope.Values["channel"] == "" {
		return tool.Result{}, fmt.Errorf("reminders require a Slack user and channel")
	}
	id, err := newID()
	if err != nil {
		return tool.Result{}, err
	}
	r, err := t.Store.Create(ctx, reminderStore.Reminder{ID: id, UserID: call.Scope.UserID, Channel: call.Scope.Values["channel"], ThreadTS: call.Scope.Values["thread_ts"], Message: strings.TrimSpace(a.Message), RunAt: runAt.UTC()})
	if err != nil {
		return tool.Result{}, err
	}
	if t.OnCreate != nil {
		t.OnCreate(ctx)
	}
	return tool.TextResult(fmt.Sprintf("提醒已创建：ID %s，将在 %s 提醒“%s”。", r.ID, r.RunAt.Format(time.RFC3339), r.Message)), nil
}

type ListTool struct{ Store reminderStore.Store }

func (ListTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("reminder-list", "", tool.ObjectSchema(nil, map[string]any{}), tool.ReadNetworkParallel()...)
}
func (t ListTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	all, err := t.Store.List(ctx, call.Scope.UserID)
	if err != nil {
		return tool.Result{}, err
	}
	if len(all) == 0 {
		return tool.TextResult("没有待执行的提醒。"), nil
	}
	var lines []string
	for _, r := range all {
		lines = append(lines, fmt.Sprintf("- %s：%s — %s", r.ID, r.RunAt.Format(time.RFC3339), r.Message))
	}
	return tool.TextResult(strings.Join(lines, "\n")), nil
}

type CancelTool struct{ Store reminderStore.Store }

func (CancelTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("reminder-cancel", "", tool.ObjectSchema([]string{"id"}, map[string]any{"id": map[string]any{"type": "string", "description": ""}}), tool.ExternalWrite()...)
}
func (t CancelTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(call.Arguments, &a); err != nil {
		return tool.Result{}, err
	}
	if err := t.Store.Cancel(ctx, a.ID, call.Scope.UserID); err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult("提醒已取消。"), nil
}
func newID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "r-" + hex.EncodeToString(b), nil
}
