package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	"github.com/noknov/kepler-agent/packages/session"
)

const webOutputPrompt = `The response is displayed in a modern web chat. Use clear Markdown with short sections only when useful. Prefer direct answers, readable lists, fenced code, and descriptive links. Do not mention the transport or repeat the user's request.`

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,96}$`)

type ConversationService struct {
	Agent      hosted.Agent
	Store      Store
	Transcript transcript.Store
	Hub        *EventHub
	Locker     session.Locker
	Prompt     safety.PromptPolicy
	Redactor   safety.Redactor
	Workspace  string
	Model      string
	Lifecycle  context.Context

	mu     sync.Mutex
	active map[string]activeWebTurn
}

type activeWebTurn struct {
	identity string
	turnID   string
	cancel   context.CancelFunc
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

func (s *ConversationService) StartTurn(ctx context.Context, owner Identity, conversationID, requestID, input string) (string, error) {
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
	if current, exists := s.active[conversationID]; exists {
		s.mu.Unlock()
		if current.turnID == turnID && current.identity == owner.Key() {
			return turnID, nil
		}
		return "", fmt.Errorf("conversation already has an active turn")
	}
	runCtx, cancel := context.WithCancel(s.baseContext())
	s.active[conversationID] = activeWebTurn{identity: owner.Key(), turnID: turnID, cancel: cancel}
	s.mu.Unlock()
	if err := s.Store.TouchConversation(ctx, owner, conversationID, titleFromInput(input)); err != nil {
		s.finish(conversationID, turnID)
		return "", err
	}
	go s.run(runCtx, owner, conversationID, turnID, model.TextMessage(model.RoleUser, input), nil)
	return turnID, nil
}

func (s *ConversationService) ResolveApproval(ctx context.Context, owner Identity, conversationID, turnID, toolCallID, requestID string, approved bool) (string, error) {
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
	runCtx, cancel := context.WithCancel(s.baseContext())
	s.active[conversationID] = activeWebTurn{identity: owner.Key(), turnID: continuationID, cancel: cancel}
	s.mu.Unlock()
	if err := s.Agent.Runtime.ResolveApproval(ctx, conversationID, agentruntime.ApprovalResolution{TurnID: turnID, ToolCallID: toolCallID, Approved: approved, UserID: owner.Key()}); err != nil {
		s.finish(conversationID, continuationID)
		return "", err
	}
	message := model.TextMessage(model.RoleUser, "Continue after the recorded approval decision.")
	message.ID = "approval-continuation:" + continuationID
	go s.run(runCtx, owner, conversationID, continuationID, message, nil)
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
	return views, nil
}

func (s *ConversationService) run(ctx context.Context, owner Identity, conversationID, turnID string, input model.Message, history []model.Message) {
	defer s.finish(conversationID, turnID)
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if s.Locker != nil {
		unlock, err := s.Locker.Lock(runCtx, "session:"+conversationID)
		if err != nil {
			s.recordStartFailure(conversationID, turnID)
			return
		}
		defer unlock()
	}
	fragments := []prompt.Fragment{
		{ID: "hosted-core", Version: "1", Layer: prompt.LayerCore, Content: s.Prompt.SystemPrompt()},
		{ID: "web-output-format", Version: "1", Layer: prompt.LayerProduct, Content: webOutputPrompt},
	}
	_, _ = s.Agent.Run(runCtx, hosted.Request{
		SessionID: conversationID, TurnID: turnID, UserID: owner.Key(), Workspace: s.Workspace,
		Input: input, History: history, Model: s.Model, Prompt: fragments,
		ScopeValues: map[string]string{"surface": "web", "web_search": "enabled"},
	})
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
