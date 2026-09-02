package appserver

import (
	"context"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

type approvalRespondParams struct {
	SessionID  string `json:"sessionId"`
	TurnID     string `json:"turnId"`
	ToolCallID string `json:"toolCallId"`
	Scope      string `json:"scope"`
}

type pendingApproval struct {
	answer chan local.ApprovalScope
}

// WireApprover returns a ScopedApprover that blocks on approval/respond RPC.
func (s *Server) WireApprover(project, approvalsPath string) *local.ScopedApprover {
	approver := &local.ScopedApprover{Project: project, Path: approvalsPath}
	approver.Prompt = func(ctx context.Context, request tool.PolicyRequest, _ tool.Decision) (local.ApprovalScope, error) {
		return s.waitApproval(ctx, request.Call.Scope.TurnID, request.Call.ID)
	}
	return approver
}

func (s *Server) waitApproval(ctx context.Context, turnID, toolCallID string) (local.ApprovalScope, error) {
	if turnID == "" || toolCallID == "" {
		return local.ApprovalDeny, fmt.Errorf("turn and tool call are required for approval")
	}
	key := approvalKey(turnID, toolCallID)
	ch := make(chan local.ApprovalScope, 1)
	s.pendingMu.Lock()
	if s.pending == nil {
		s.pending = make(map[string]*pendingApproval)
	}
	s.pending[key] = &pendingApproval{answer: ch}
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, key)
		s.pendingMu.Unlock()
	}()
	select {
	case scope := <-ch:
		return scope, nil
	case <-ctx.Done():
		return local.ApprovalDeny, ctx.Err()
	}
}

func (s *Server) respondApproval(params approvalRespondParams) error {
	scope, err := parseApprovalScope(params.Scope)
	if err != nil {
		return err
	}
	key := approvalKey(params.TurnID, params.ToolCallID)
	s.pendingMu.Lock()
	pending := s.pending[key]
	s.pendingMu.Unlock()
	if pending == nil {
		return fmt.Errorf("no pending approval for turn %q tool %q", params.TurnID, params.ToolCallID)
	}
	select {
	case pending.answer <- scope:
		return nil
	default:
		return fmt.Errorf("approval for turn %q tool %q was already answered", params.TurnID, params.ToolCallID)
	}
}

func parseApprovalScope(value string) (local.ApprovalScope, error) {
	switch local.ApprovalScope(value) {
	case local.ApprovalDeny, local.ApprovalOnce, local.ApprovalSession, local.ApprovalProject:
		return local.ApprovalScope(value), nil
	case "":
		return local.ApprovalDeny, nil
	default:
		return "", fmt.Errorf("unsupported approval scope %q", value)
	}
}

func approvalKey(turnID, toolCallID string) string {
	return turnID + ":" + toolCallID
}
