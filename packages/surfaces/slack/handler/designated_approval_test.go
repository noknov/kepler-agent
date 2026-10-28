package slackhandler

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/session"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	gateway "github.com/noknov/kepler-agent/packages/surfaces/slack/gateway"
)

func TestOperatorApprovalInThreadPreservesRequesterScope(t *testing.T) {
	ctx := context.Background()
	inputs := &dedupeInputStore{}
	origin := session.ID("C-shared", "thread")
	h := &Handler{Inputs: inputs, Approvals: approvalAuthorityFunc(func(_ context.Context, id string, r agentruntime.ApprovalResolution) (tool.Scope, error) {
		if id != origin || r.UserID != "owner" {
			return tool.Scope{}, fmt.Errorf("not authorized")
		}
		return tool.Scope{UserID: "requester", Values: map[string]string{"team_id": "team", "channel": "C-shared", "thread_ts": "thread"}}, nil
	})}
	value, _ := json.Marshal(map[string]string{"session_id": origin, "turn_id": "turn", "tool_call_id": "call"})
	// Unauthorized clicks must not consume the stable queue ID or disable controls.
	for _, actor := range []string{"requester", "other"} {
		h.resolveApproval(ctx, gateway.Interaction{TeamID: "team", UserID: actor, Channel: "C-shared", ThreadTS: "thread", MessageTS: "card"}, "agent_approval_approve", string(value))
	}
	if len(inputs.items) != 0 {
		t.Fatal("unauthorized click consumed approval")
	}
	h.resolveApproval(ctx, gateway.Interaction{TeamID: "foreign", UserID: "owner", Channel: "C-shared", ThreadTS: "thread", MessageTS: "card"}, "agent_approval_approve", string(value))
	if len(inputs.items) != 0 {
		t.Fatal("cross-workspace control accepted")
	}
	h.resolveApproval(ctx, gateway.Interaction{TeamID: "team", UserID: "owner", Channel: "D-owner", ThreadTS: "thread", MessageTS: "card"}, "agent_approval_approve", string(value))
	if len(inputs.items) != 0 {
		t.Fatal("control admitted from a different conversation")
	}
	interaction := gateway.Interaction{TeamID: "team", UserID: "owner", Channel: "C-shared", ThreadTS: "thread", MessageTS: "card"}
	h.resolveApproval(ctx, interaction, "agent_approval_approve", string(value))
	h.resolveApproval(ctx, interaction, "agent_approval_approve", string(value))
	if len(inputs.items) != 1 || inputs.items[0].SessionID != origin {
		t.Fatalf("queue=%+v", inputs.items)
	}
	var req conversation.Request
	if err := json.Unmarshal(inputs.items[0].Payload, &req); err != nil {
		t.Fatal(err)
	}
	if req.UserID != "requester" || req.Channel != "C-shared" || req.ThreadTS != "thread" || req.Approval.ResolvedBy != "owner" {
		t.Fatalf("lost principal/destination ownership: %+v approval=%+v", req, req.Approval)
	}
}
