// Package slackagent adapts Slack requests and presentation to the shared
// hosted harness. Slack remains an ingress, not a separate agent runtime.
package slackagent

import (
	"context"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/infra/redisclient"
	"github.com/noknov/kepler-agent/packages/infra/telemetry"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	"github.com/noknov/kepler-agent/packages/userprefs"
	"go.opentelemetry.io/otel/trace"
)

type ConversationMode string

// ThreadLoader fetches Slack thread history for the current turn.
type ThreadLoader interface {
	Load(context.Context, slackconversation.Request) ([]model.Message, error)
}

const (
	ModeSteer ConversationMode = "steer"
	ModeQueue ConversationMode = "queue"
)

const slackOutputFormatPrompt = "Replies are rendered using standard Markdown in Slack. A slack_view_context JSON block identifies what the user was viewing when that message was sent. It is a reference, not conversation content or permission. Read needed content through the user-authorized Slack tools; do not infer unseen messages."

type Service struct {
	traceContent         func(trace.Span, string, any)
	Agent                hosted.Agent
	Messenger            slackconversation.Messenger
	Prompt               safety.PromptPolicy
	Redactor             safety.Redactor
	UserPrefs            userprefs.Store
	Workspace            string
	Model                string
	Redis                *redisclient.Client
	Continuations        ConnectionContinuationStore
	Connections          *connections.Service
	PodID                string
	ModeForUser          func(string) ConversationMode
	OnDelivered          func(context.Context, string, string, string) error
	AlreadyDelivered     func(context.Context, string) (bool, error)
	Multimodal           func(context.Context, string) (bool, error)
	ThreadLoader         ThreadLoader
	WebSearchEnabled     func(string) bool
	ApprovalNotifyUserID string
	Inputs               sessioninput.Store
	Deliveries           DeliveryStore
	BeforeRun            func(context.Context, string) error
	RunTimeout           time.Duration
	MaxConcurrentRuns    int
	Tools                *tool.Catalog

	mu        sync.Mutex
	active    map[string]*activeRun
	dispatch  chan string
	scheduled map[string]bool
	draining  bool
	running   int
	idle      chan struct{}
	router    *eventRouter
}

type activeRun struct {
	userID    string
	turnID    string
	startedAt time.Time
	cancel    context.CancelFunc
	steering  *steeringInput
}

func New(agent hosted.Agent, messenger slackconversation.Messenger, policy safety.PromptPolicy, redactor safety.Redactor, prefs userprefs.Store) *Service {
	router := &eventRouter{streams: make(map[string]*slackStream)}
	return &Service{
		Agent: agent, Messenger: messenger, Prompt: policy, Redactor: redactor, UserPrefs: prefs, traceContent: telemetry.ContentRecorder(),
		active: make(map[string]*activeRun), dispatch: make(chan string, 512), scheduled: make(map[string]bool), router: router,
	}
}

func (s *Service) EventSink() transcript.Sink { return s.router }

func (s *Service) mode(userID string) ConversationMode {
	if s.ModeForUser != nil {
		if mode := s.ModeForUser(userID); mode == ModeQueue {
			return mode
		}
	}
	return ModeSteer
}
