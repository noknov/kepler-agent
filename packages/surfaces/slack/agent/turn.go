package slackagent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/failure"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	"github.com/noknov/kepler-agent/packages/userprefs"
)

func (s *Service) run(eventCtx context.Context, sessionID string, claimed claimedInput) (runErr error) {
	req := claimed.Request
	runCtx, cancel := context.WithTimeout(eventCtx, s.runTimeout())
	owner := s.owner()
	active := &activeRun{userID: req.UserID, turnID: req.EventID, startedAt: time.Now(), cancel: cancel, steering: &steeringInput{store: s.Inputs, sessionID: sessionID, owner: owner, userID: req.UserID}}
	if existing := s.register(sessionID, active); existing != nil {
		cancel()
		// Another executor owns this session. Preserve the claimed input for
		// the dispatcher; execution must not turn it into a second admission.
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(eventCtx), 5*time.Second)
		defer releaseCancel()
		return s.releaseClaim(releaseCtx, req.ClaimID)
	}

	defer func() {
		cancel()
		s.unregister(sessionID, active)
		if s.Inputs != nil {
			promoteCtx, promoteCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, promoteErr := s.Inputs.PromoteSteering(promoteCtx, sessionID)
			promoteCancel()
			if runErr == nil && promoteErr != nil {
				runErr = promoteErr
			}
		}
		if runErr == nil {
			s.wakeExecution(sessionID)
		}
	}()

	defer s.keepInputClaim(runCtx, sessionID, req.ClaimID, cancel)()
	if s.AuthorizeInput != nil {
		allowed, err := s.AuthorizeInput(runCtx, req)
		if err != nil {
			return err
		}
		if !allowed {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(runCtx), 5*time.Second)
			defer cancel()
			return s.ackClaim(cleanup, req.ClaimID)
		}
	}
	if s.BeforeRun != nil {
		if err := s.BeforeRun(runCtx, req.UserID); err != nil {
			cancel()
			return err
		}
	}

	// The registered run, transcript and Slack delivery share one identifier.
	turnID := req.EventID
	runCtx, reply := beginReplyTrace(runCtx, sessionID, req, s.traceContent)
	defer func() { reply.end(runErr) }()
	delivery, err := s.beginDelivery(runCtx, sessionID, req)
	if err != nil {
		return err
	}
	if delivery != nil && delivery.receipt.Phase == replyDelivered {
		return s.ackClaim(runCtx, req.ClaimID)
	}
	stream := newSlackStream(runCtx, s.Messenger, req)
	stream.reconcileStatus = func(ctx context.Context, send func(context.Context) error) error {
		_, err := s.Agent.Runtime.ReconcileTurn(ctx, sessionID, req.EventID, func(ctx context.Context, _ agentruntime.TerminationReason) error { return send(ctx) })
		return err
	}
	defer stream.closeDeliveryContext()
	stream.delivery = delivery
	if delivery != nil {
		receipt := delivery.receipt
		if receipt.MessageTS != "" {
			stream.messageTS = receipt.MessageTS
			stream.nativeStream = true
			stream.streamDeliveryFailed = true
		}
		if receipt.Phase == replyUncertain {
			stream.streamStartUncertain = true
			stream.streamDeliveryFailed = true
		}
	}

	stream.approvalNotifyUserID = s.ApprovalNotifyUserID
	stream.telemetry = reply
	stream.connections = s.Connections
	stream.redactor = safety.NewStreamRedactor(s.Redactor)
	s.router.set(turnID, stream)
	defer s.router.set(turnID, nil)
	finalSessionStatus := sessionActive
	defer func() {
		// Complete/Fail normally synchronize status after message delivery.
		// Cover early failures and retry a failed terminal-state update here.
		stream.setSessionStatus(finalSessionStatus)
	}()
	if approval := req.Approval; approval != nil {
		if err := s.Agent.Runtime.ResolveApproval(runCtx, sessionID, agentruntime.ApprovalResolution{
			TurnID: approval.TurnID, ToolCallID: approval.ToolCallID, Approved: approval.Approved, UserID: req.UserID,
		}); err != nil {
			return err
		}
		if approval.MessageTS != "" {
			if updater, ok := s.Messenger.(slackconversation.MessageBlocksUpdater); ok {
				if err := updater.UpdateMessageBlocks(runCtx, req.Channel, approval.MessageTS, "Approval resolved", approvalResolutionBlocks(approval.Approved)); err != nil {
					log.Printf("slack approval resolved update failed turn=%s call=%s: %v", approval.TurnID, approval.ToolCallID, err)
				}
			}
		}
	}

	fragments := []prompt.Fragment{
		{ID: "hosted-core", Version: "1", Layer: prompt.LayerCore, Content: s.Prompt.SystemPrompt()},
		{ID: "slack-output-format", Version: "3", Layer: prompt.LayerProduct, Content: slackOutputFormatPrompt},
		{ID: "user-rules", Layer: prompt.LayerUser, Content: userprefs.RulesPrompt(runCtx, s.UserPrefs, req.UserID)},
		{ID: "user-skills", Layer: prompt.LayerSkill, Content: userprefs.SkillsMetadataPrompt(runCtx, s.UserPrefs, req.UserID)},
	}
	// Slack thread history is a first-turn bootstrap only: the runtime lazily
	// loads it when the canonical session is empty and otherwise relies on the
	// append-only transcript. That keeps the model prefix stable for provider
	// prompt caching and avoids re-fetching the thread, re-downloading
	// attachments, and re-attaching historical images on every turn.
	var historyLoader func(context.Context) ([]model.Message, error)
	if s.ThreadLoader != nil {
		historyLoader = func(loadCtx context.Context) ([]model.Message, error) {
			return s.ThreadLoader.Load(loadCtx, req)
		}
	}
	modelName := strings.TrimSpace(s.Model)
	// Only the current message's own attachments enter the turn input. Historical
	// images are read on demand, never re-attached, so a later question does not
	// resend every earlier screenshot.
	input := req.Message()
	if s.Multimodal != nil && model.ContainImages(input) {
		supported, err := s.Multimodal(runCtx, modelName)
		if err != nil {
			return fmt.Errorf("resolve image input capability: %w", err)
		}
		if !supported {
			input = withoutUnsupportedImages(input, slackconversation.IsChineseLocale(req.Locale))
		}
	}
	webSearch := "enabled"
	if s.WebSearchEnabled != nil && !s.WebSearchEnabled(req.UserID) {
		webSearch = "disabled"
	}
	scopeValues := map[string]string{"surface": "slack", "team_id": req.TeamID, "channel": req.Channel, "thread_ts": req.ThreadTS, "message_ts": req.MessageTS, "web_search": webSearch}
	scopeValues["conversation_type"] = req.ConversationType
	scopeValues["session_kind"] = string(req.SessionKind)
	result, err := s.Agent.Run(runCtx, hosted.Request{
		SessionID: sessionID, TurnID: turnID, ResumeFromTurnID: req.ResumeFromTurnID,
		UserID: req.UserID, Workspace: s.Workspace,
		Input: input, HistoryLoader: historyLoader, Model: modelName,
		Steering: active.steering, Prompt: fragments, ScopeValues: scopeValues,
	})
	finalizeCtx, finalizeCancel := context.WithTimeout(context.WithoutCancel(runCtx), 20*time.Second)
	defer finalizeCancel()
	if errors.Is(err, agentruntime.ErrStaleContinuation) {
		reply.delivery("superseded", nil)
		return s.ackClaim(finalizeCtx, req.ClaimID)
	}
	if err != nil {
		if s.AlreadyDelivered != nil {
			delivered, deliveryStateErr := s.AlreadyDelivered(finalizeCtx, turnID)
			if deliveryStateErr != nil {
				return deliveryStateErr
			}
			if delivered {
				reply.delivery("already_delivered", nil)
				stream.setSessionStatus(sessionActive)
				return s.ackClaim(finalizeCtx, req.ClaimID)
			}
		}
		log.Printf("slack agent run failed session=%s turn=%s: %v", sessionID, turnID, err)
		messageTS, deliveryErr := stream.Fail(failure.PublicMessage(err), errors.Is(err, context.Canceled))
		if deliveryErr == nil && messageTS != "" && delivery != nil && delivery.receipt.Phase == replyStreaming {
			deliveryErr = delivery.advance(finalizeCtx, replyDelivered, messageTS)
		}
		deliveryErr = errors.Join(deliveryErr, delivery.persistenceError())
		stream.recordFinalDelivery(messageTS, deliveryErr)
		if deliveryErr != nil {
			return deliveryErr
		}
		return s.persistDelivery(runCtx, turnID, req, messageTS)
	}
	final := s.Redactor.Sanitize(renderAnswer(result.Message))
	finalSessionStatus = sessionStatusForTermination(string(result.Termination))
	stream.sessionMu.Lock()
	stream.terminalSessionStatus = finalSessionStatus
	stream.sessionMu.Unlock()
	if s.AlreadyDelivered != nil {
		delivered, deliveryStateErr := s.AlreadyDelivered(finalizeCtx, turnID)
		if deliveryStateErr != nil {
			return deliveryStateErr
		}
		if delivered {
			reply.delivery("already_delivered", nil)
			stream.setSessionStatus(sessionStatusForTermination(string(result.Termination)))
			return s.ackClaim(finalizeCtx, req.ClaimID)
		}
	}
	messageTS, err := stream.Complete(final)
	if err == nil && messageTS != "" && delivery != nil && delivery.receipt.Phase == replyStreaming {
		err = delivery.advance(finalizeCtx, replyDelivered, messageTS)
	}
	err = errors.Join(err, delivery.persistenceError())
	stream.recordFinalDelivery(messageTS, err)
	if err != nil {
		return err
	}
	return s.persistDelivery(runCtx, turnID, req, messageTS)
}

func (s *Service) runTimeout() time.Duration {
	if s.RunTimeout > 0 {
		return s.RunTimeout
	}
	return 30 * time.Minute
}

func renderAnswer(message model.Message) string {
	answer := strings.TrimSpace(message.Text())
	seen := make(map[string]bool)
	var sources []string
	for _, citation := range message.Citations() {
		url := strings.TrimSpace(citation.URL)
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		title := strings.TrimSpace(citation.Title)
		if title == "" {
			title = url
		}
		sources = append(sources, "["+title+"]("+url+")")
	}
	if len(sources) == 0 {
		return answer
	}
	return answer + "\n\nSources: " + strings.Join(sources, ", ")
}

func withoutUnsupportedImages(message model.Message, cjk bool) model.Message {
	content := make([]model.Content, 0, len(message.Content)+1)
	removed := 0
	for _, block := range message.Content {
		if block.Type == model.ContentImage {
			removed++
			continue
		}
		content = append(content, block)
	}
	if removed == 0 {
		return message
	}
	note := fmt.Sprintf("[%d image attachment(s) omitted because the selected model does not support images.]", removed)
	if cjk {
		note = fmt.Sprintf("[已省略 %d 个图片附件：当前选择的模型不支持图片。]", removed)
	}
	content = append(content, model.Content{Type: model.ContentText, Text: note})
	message.Content = content
	return message
}

func (s *Service) register(sessionID string, run *activeRun) *activeRun {
	s.mu.Lock()
	if existing := s.active[sessionID]; existing != nil {
		s.mu.Unlock()
		return existing
	}
	s.active[sessionID] = run
	s.mu.Unlock()
	if s.Redis != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Redis.Set(ctx, "agent:active:"+sessionID, s.PodID, inputClaimLease)
	}
	return nil
}

func (s *Service) unregister(sessionID string, run *activeRun) {
	s.mu.Lock()
	owned := s.active[sessionID] == run
	if owned {
		delete(s.active, sessionID)
	}
	s.mu.Unlock()
	if owned && s.Redis != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Redis.DeleteIfValue(ctx, "agent:active:"+sessionID, s.PodID)
	}
}
