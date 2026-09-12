package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/sessioninput"
)

const webOutputPrompt = "The response is displayed in a modern web chat that renders GitHub-Flavored Markdown.\n\n" +
	"Formatting rules:\n" +
	"- Use valid Markdown only. Code blocks must use triple backticks on their own lines.\n" +
	"- Never use two-backtick fences, single-backtick fences, or unclosed code blocks.\n" +
	"- Use headings, lists, tables, and links when they improve readability.\n" +
	"- Keep ASCII diagrams inside fenced code blocks.\n" +
	"- Prefer direct answers with short sections only when useful.\n" +
	"- Do not mention the transport or repeat the user's request."

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,96}$`)

type ConversationService struct {
	Agent      hosted.Agent
	Store      Store
	Transcript transcript.Store
	Hub        *EventHub
	Prompt     safety.PromptPolicy
	Redactor   safety.Redactor
	Workspace  string
	Model      string
	Lifecycle  context.Context
	BeforeRun  func(context.Context, string) error
	IsDraining func() bool
	Inputs     sessioninput.Store
	QueueOwner string

	mu      sync.Mutex
	queueMu sync.Mutex
	wg      sync.WaitGroup
	active  map[string]activeWebTurn
}

type activeWebTurn struct {
	identity string
	turnID   string
	cancel   context.CancelFunc
}

type queuedWebTurn struct {
	Owner    Identity                         `json:"owner"`
	Input    model.Message                    `json:"input"`
	Approval *agentruntime.ApprovalResolution `json:"approval,omitempty"`
}

func NewConversationService(agent hosted.Agent, store Store, transcriptStore transcript.Store, hub *EventHub) *ConversationService {
	return &ConversationService{Agent: agent, Store: store, Transcript: transcriptStore, Hub: hub, active: make(map[string]activeWebTurn)}
}

func (s *ConversationService) Create(ctx context.Context, owner Identity) (Conversation, error) {
	random, err := randomToken(18)
	if err != nil {
		return Conversation{}, err
	}
	now := time.Now().UTC()
	conversation := Conversation{ID: "web_" + random, Title: "New conversation", CreatedAt: now, UpdatedAt: now}
	return conversation, s.Store.CreateConversation(ctx, owner, conversation)
}

type sessionMessageChecker interface {
	SessionsWithMessages(ctx context.Context, sessionIDs []string) (map[string]bool, error)
}

func (s *ConversationService) List(ctx context.Context, owner Identity, archived bool, limit, offset int) ([]Conversation, error) {
	conversations, err := s.Store.ListConversations(ctx, owner, archived, limit, offset)
	if err != nil {
		return nil, err
	}
	if len(conversations) == 0 {
		return conversations, nil
	}
	ids := make([]string, len(conversations))
	for i, conversation := range conversations {
		ids[i] = conversation.ID
	}
	var hasMessages map[string]bool
	if checker, ok := s.Transcript.(sessionMessageChecker); ok {
		hasMessages, err = checker.SessionsWithMessages(ctx, ids)
		if err != nil {
			return nil, err
		}
	} else if s.Transcript != nil {
		hasMessages = make(map[string]bool, len(ids))
		for _, id := range ids {
			events, loadErr := s.Transcript.Load(ctx, id, 0)
			if loadErr != nil {
				continue
			}
			for _, event := range events {
				if event.Type == transcript.UserInput || event.Type == transcript.AssistantMessage {
					hasMessages[id] = true
					break
				}
			}
		}
	}
	for i := range conversations {
		conversations[i].HasMessages = hasMessages[conversations[i].ID]
	}
	return conversations, nil
}

func (s *ConversationService) StartTurn(ctx context.Context, owner Identity, conversationID, requestID, input string) (string, error) {
	if s.IsDraining != nil && s.IsDraining() {
		return "", fmt.Errorf("service is draining; retry shortly")
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("message is required")
	}
	if utf8.RuneCountInString(input) > 32000 {
		return "", fmt.Errorf("message is too long")
	}
	if !requestIDPattern.MatchString(requestID) {
		return "", fmt.Errorf("requestId is invalid")
	}
	conversation, err := s.Store.GetConversation(ctx, owner, conversationID)
	if err != nil {
		return "", err
	}
	if conversation.ArchivedAt != nil {
		return "", fmt.Errorf("conversation is archived")
	}
	turnID := deterministicTurnID(conversationID, requestID)
	s.mu.Lock()
	current, exists := s.active[conversationID]
	s.mu.Unlock()
	if exists {
		if current.turnID == turnID && current.identity == owner.Key() {
			return turnID, nil
		}
		return "", fmt.Errorf("conversation already has an active turn")
	}
	if err := s.Store.TouchConversation(ctx, owner, conversationID, titleFromInput(input)); err != nil {
		return "", err
	}
	message := model.TextMessage(model.RoleUser, input)
	if s.Inputs != nil {
		payload, err := json.Marshal(queuedWebTurn{Owner: owner, Input: message})
		if err != nil {
			return "", err
		}
		if err := s.Inputs.Enqueue(ctx, sessioninput.Item{ID: turnID, SessionID: conversationID, Kind: sessioninput.KindWeb, Payload: payload}); err != nil {
			return "", err
		}
		s.startPending(ctx, conversationID)
		return turnID, nil
	}
	if !s.launch(owner, conversationID, turnID, message, nil, "") {
		return "", fmt.Errorf("conversation already has an active turn")
	}
	return turnID, nil
}

func (s *ConversationService) ResolveApproval(ctx context.Context, owner Identity, conversationID, turnID, toolCallID, requestID string, approved bool) (string, error) {
	if s.IsDraining != nil && s.IsDraining() {
		return "", fmt.Errorf("service is draining; retry shortly")
	}
	if _, err := s.Store.GetConversation(ctx, owner, conversationID); err != nil {
		return "", err
	}
	if !requestIDPattern.MatchString(requestID) || turnID == "" || toolCallID == "" {
		return "", fmt.Errorf("approval request is invalid")
	}
	s.mu.Lock()
	if _, exists := s.active[conversationID]; exists {
		s.mu.Unlock()
		return "", fmt.Errorf("conversation already has an active turn")
	}
	continuationID := deterministicTurnID(conversationID, requestID)
	s.mu.Unlock()
	message := model.TextMessage(model.RoleUser, "Continue after the recorded approval decision.")
	message.ID = "approval-continuation:" + continuationID
	resolution := agentruntime.ApprovalResolution{TurnID: turnID, ToolCallID: toolCallID, Approved: approved, UserID: owner.SubjectID}
	if s.Inputs != nil {
		payload, err := json.Marshal(queuedWebTurn{Owner: owner, Input: message, Approval: &resolution})
		if err != nil {
			return "", err
		}
		if err := s.Inputs.Enqueue(ctx, sessioninput.Item{ID: continuationID, SessionID: conversationID, Kind: sessioninput.KindWeb, Payload: payload}); err != nil {
			return "", err
		}
		s.startPending(ctx, conversationID)
		return continuationID, nil
	}
	if err := s.Agent.Runtime.ResolveApproval(ctx, conversationID, resolution); err != nil {
		return "", err
	}
	if !s.launch(owner, conversationID, continuationID, message, nil, "") {
		return "", fmt.Errorf("conversation already has an active turn")
	}
	return continuationID, nil
}

func (s *ConversationService) Stop(owner Identity, conversationID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	active, ok := s.active[conversationID]
	if !ok || active.identity != owner.Key() {
		return false
	}
	active.cancel()
	return true
}

func (s *ConversationService) Events(ctx context.Context, owner Identity, conversationID string, after uint64) ([]ClientEvent, error) {
	if _, err := s.Store.GetConversation(ctx, owner, conversationID); err != nil {
		return nil, err
	}
	events, err := s.Transcript.Load(ctx, conversationID, after)
	if err != nil {
		return nil, err
	}
	views := make([]ClientEvent, 0, len(events))
	for _, event := range events {
		if view, ok := ProjectEvent(event, s.Redactor); ok {
			views = append(views, view)
		}
	}
	return CollapseClientEvents(views), nil
}

func (s *ConversationService) run(ctx context.Context, owner Identity, conversationID, turnID string, input model.Message, history []model.Message) error {
	defer s.finish(conversationID, turnID)
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if s.BeforeRun != nil {
		if err := s.BeforeRun(runCtx, owner.SubjectID); err != nil {
			s.recordStartFailure(conversationID, turnID)
			return err
		}
	}
	fragments := []prompt.Fragment{
		{ID: "hosted-core", Version: "1", Layer: prompt.LayerCore, Content: s.Prompt.SystemPrompt()},
		{ID: "web-output-format", Version: "1", Layer: prompt.LayerProduct, Content: webOutputPrompt},
	}
	_, err := s.Agent.Run(runCtx, hosted.Request{
		SessionID: conversationID, TurnID: turnID, UserID: owner.SubjectID, Workspace: s.Workspace,
		Input: input, History: history, Model: s.Model, Prompt: fragments,
		ScopeValues: map[string]string{"surface": "web", "web_search": "enabled"},
	})
	return err
}

func (s *ConversationService) launch(owner Identity, conversationID, turnID string, input model.Message, history []model.Message, claimID string) bool {
	runCtx, cancel := context.WithCancel(s.baseContext())
	s.mu.Lock()
	if _, exists := s.active[conversationID]; exists {
		s.mu.Unlock()
		cancel()
		return false
	}
	s.active[conversationID] = activeWebTurn{identity: owner.Key(), turnID: turnID, cancel: cancel}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		_ = s.run(runCtx, owner, conversationID, turnID, input, history)
		if claimID != "" && s.Inputs != nil {
			finalCtx, finalCancel := context.WithTimeout(context.Background(), 5*time.Second)
			terminal := s.turnTerminal(finalCtx, conversationID, turnID)
			if terminal {
				_ = s.Inputs.Ack(finalCtx, claimID, s.queueOwner())
			} else {
				_ = s.Inputs.Release(finalCtx, claimID, s.queueOwner())
			}
			finalCancel()
			if terminal {
				s.startPending(s.baseContext(), conversationID)
			}
		}
	}()
	return true
}

func (s *ConversationService) turnTerminal(ctx context.Context, conversationID, turnID string) bool {
	if s.Transcript == nil {
		return false
	}
	events, err := s.Transcript.Load(ctx, conversationID, 0)
	if err != nil {
		return false
	}
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].TurnID == turnID && (events[index].Type == transcript.TurnCompleted || events[index].Type == transcript.TurnFailed) {
			return true
		}
	}
	return false
}

func (s *ConversationService) startPending(ctx context.Context, conversationID string) {
	if s.Inputs == nil {
		return
	}
	if s.IsDraining != nil && s.IsDraining() {
		return
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	s.mu.Lock()
	_, active := s.active[conversationID]
	s.mu.Unlock()
	if active {
		return
	}
	items, err := s.Inputs.Claim(ctx, conversationID, sessioninput.KindWeb, s.queueOwner(), 35*time.Minute, 1)
	if err != nil || len(items) == 0 {
		return
	}
	if items[0].Attempts > 5 {
		s.recordStartFailure(conversationID, items[0].ID)
		_ = s.Inputs.Ack(ctx, items[0].ID, s.queueOwner())
		return
	}
	var queued queuedWebTurn
	if json.Unmarshal(items[0].Payload, &queued) != nil || queued.Owner.Key() == "::" || len(queued.Input.Content) == 0 {
		_ = s.Inputs.Ack(ctx, items[0].ID, s.queueOwner())
		return
	}
	if _, err := s.Store.GetConversation(ctx, queued.Owner, conversationID); err != nil {
		_ = s.Inputs.Ack(ctx, items[0].ID, s.queueOwner())
		return
	}
	if queued.Approval != nil {
		if err := s.Agent.Runtime.ResolveApproval(ctx, conversationID, *queued.Approval); err != nil {
			s.recordStartFailure(conversationID, items[0].ID)
			_ = s.Inputs.Ack(ctx, items[0].ID, s.queueOwner())
			return
		}
	}
	if !s.launch(queued.Owner, conversationID, items[0].ID, queued.Input, nil, items[0].ID) {
		_ = s.Inputs.Release(ctx, items[0].ID, s.queueOwner())
	}
}

func (s *ConversationService) StartRecovery(ctx context.Context) {
	if s.Inputs == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		sessions, _ := s.Inputs.PendingSessions(ctx, sessioninput.KindWeb, 100)
		for _, sessionID := range sessions {
			s.startPending(ctx, sessionID)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *ConversationService) Wait() { s.wg.Wait() }

func (s *ConversationService) WaitContext(ctx context.Context) bool {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		active := len(s.active)
		s.mu.Unlock()
		if active == 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

func (s *ConversationService) queueOwner() string {
	if strings.TrimSpace(s.QueueOwner) != "" {
		return s.QueueOwner
	}
	return "web-worker"
}

func (s *ConversationService) recordStartFailure(conversationID, turnID string) {
	if s.Transcript == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	event, err := s.Transcript.Append(ctx, transcript.Event{
		ID: turnID + ":start_failed", SessionID: conversationID, TurnID: turnID,
		Type: transcript.TurnFailed, Timestamp: time.Now().UTC(), Status: "failed",
	})
	if err == nil && s.Hub != nil {
		s.Hub.Publish(ctx, event)
	}
}

func (s *ConversationService) finish(conversationID, turnID string) {
	s.mu.Lock()
	if active, ok := s.active[conversationID]; ok && active.turnID == turnID {
		delete(s.active, conversationID)
		active.cancel()
	}
	s.mu.Unlock()
}

func (s *ConversationService) baseContext() context.Context {
	if s.Lifecycle != nil {
		return s.Lifecycle
	}
	return context.Background()
}

func deterministicTurnID(conversationID, requestID string) string {
	hash := sha256.Sum256([]byte(conversationID + "\n" + requestID))
	return "webturn_" + base64.RawURLEncoding.EncodeToString(hash[:18])
}

func titleFromInput(input string) string {
	input = strings.TrimSpace(strings.SplitN(input, "\n", 2)[0])
	runes := []rune(input)
	if len(runes) > 52 {
		input = strings.TrimSpace(string(runes[:52])) + "…"
	}
	return input
}

func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
