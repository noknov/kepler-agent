package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
)

type ApprovalScope string

const (
	ApprovalDeny    ApprovalScope = "deny"
	ApprovalOnce    ApprovalScope = "once"
	ApprovalSession ApprovalScope = "session"
	ApprovalProject ApprovalScope = "project"
)

type ApprovalPrompt func(ctx context.Context, request tool.PolicyRequest, decision tool.Decision) (ApprovalScope, error)

// ScopedApprover supports one-shot, session, and project-scoped grants. Project
// rules are stored under the agent state directory rather than modifying source.
type ScopedApprover struct {
	Project string
	Path    string
	Prompt  ApprovalPrompt
	mu      sync.Mutex
	session map[string]bool
	project map[string]bool
	loaded  bool
}

func (a *ScopedApprover) Approve(ctx context.Context, request tool.PolicyRequest, decision tool.Decision) (bool, error) {
	key := approvalKey(request, decision)
	a.mu.Lock()
	if a.session == nil {
		a.session = make(map[string]bool)
	}
	if err := a.loadLocked(); err != nil {
		a.mu.Unlock()
		return false, err
	}
	approved := a.session[key] || a.project[key]
	a.mu.Unlock()
	if approved {
		return true, nil
	}
	if a.Prompt == nil {
		return false, nil
	}
	scope, err := a.Prompt(ctx, request, decision)
	if err != nil {
		return false, err
	}
	switch scope {
	case ApprovalOnce:
		return true, nil
	case ApprovalSession:
		a.mu.Lock()
		a.session[key] = true
		a.mu.Unlock()
		return true, nil
	case ApprovalProject:
		a.mu.Lock()
		a.project[key] = true
		err = a.saveLocked()
		a.mu.Unlock()
		return err == nil, err
	case ApprovalDeny, "":
		return false, nil
	default:
		return false, fmt.Errorf("unsupported approval scope %q", scope)
	}
}

func (a *ScopedApprover) loadLocked() error {
	if a.loaded {
		return nil
	}
	a.loaded = true
	a.project = make(map[string]bool)
	if a.Path == "" {
		return nil
	}
	data, err := os.ReadFile(a.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var all map[string]map[string]bool
	if err := json.Unmarshal(data, &all); err != nil {
		return fmt.Errorf("decode approval rules: %w", err)
	}
	for key, approved := range all[a.Project] {
		if approved {
			a.project[key] = true
		}
	}
	return nil
}

func (a *ScopedApprover) saveLocked() error {
	if a.Path == "" {
		return fmt.Errorf("project approval storage is not configured")
	}
	all := make(map[string]map[string]bool)
	if data, err := os.ReadFile(a.Path); err == nil {
		_ = json.Unmarshal(data, &all)
	}
	all[a.Project] = a.project
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.Path), 0o700); err != nil {
		return err
	}
	temporary := a.Path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, a.Path)
}

func approvalKey(request tool.PolicyRequest, decision tool.Decision) string {
	data, _ := json.Marshal(struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Rule      string          `json:"rule"`
	}{request.Call.Name, request.Call.Arguments, decision.Rule})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
