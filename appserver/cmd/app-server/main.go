package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/noknov/kepler-agent/packages/agent/delegation"
	"github.com/noknov/kepler-agent/packages/agent/environment"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/appserver"
	"github.com/noknov/kepler-agent/packages/profiles/local"
	"github.com/noknov/kepler-agent/packages/providers"
	localtools "github.com/noknov/kepler-agent/packages/tools/local"
)

func main() {
	options := serverOptions{}
	flag.StringVar(&options.ConfigPath, "config", "", "path to local config.toml")
	flag.StringVar(&options.Workspace, "workspace", "", "workspace directory (defaults to the current directory)")
	flag.StringVar(&options.StateDir, "state-dir", "", "directory for local sessions and approvals")
	flag.Parse()
	ctx, stop := signalContext()
	defer stop()
	if err := run(ctx, options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// serverOptions are deliberately local-only: a desktop host chooses a project
// directory and state directory, while credentials and model settings remain in
// the user's existing local configuration and environment.
type serverOptions struct {
	ConfigPath string
	Workspace  string
	StateDir   string
}

func run(ctx context.Context, options serverOptions) error {
	config, err := local.LoadConfig(options.ConfigPath)
	if err != nil {
		return err
	}
	workspaceRoot := options.Workspace
	if workspaceRoot == "" {
		workspaceRoot, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	workspace, err := local.NewWorkspace(workspaceRoot)
	if err != nil {
		return err
	}
	defer workspace.Close()
	sandbox := local.Sandbox{Workspace: workspace, AdditionalReadRoots: config.AdditionalReadRoots, UnsafeAllowNoSandbox: config.UnsafeAllowNoSandbox}
	catalog, err := localtools.NewCatalog(workspace, sandbox)
	if err != nil {
		return err
	}
	key := os.Getenv(config.APIKeyEnv)
	if key == "" {
		return fmt.Errorf("model API key environment variable %s is empty", config.APIKeyEnv)
	}
	client, err := providers.New(providers.Config{Provider: config.Provider, Protocol: config.Protocol, BaseURL: config.BaseURL, APIKey: key, AnthropicFlavor: config.AnthropicFlavor, Timeout: config.Timeout})
	if err != nil {
		return err
	}
	stateDir := options.StateDir
	if stateDir == "" {
		stateDir, err = local.DefaultStateDir()
		if err != nil {
			return err
		}
	}
	stateDir, err = filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	exploreRunner := delegation.Runner{
		Config: agentruntime.Config{
			Model: config.Model, ReasoningEffort: config.ReasoningEffort, MaxOutputTokens: config.MaxOutputTokens,
			Temperature: config.Temperature, MaxSteps: 12,
			Context: agentruntime.ContextConfig{MaxTokens: config.MaxContextTokens, ReserveTokens: config.AutocompactBuffer},
		},
		Deps: agentruntime.Dependencies{
			Model: model.Client(client), Policy: local.WorkspacePolicy{},
			Compactor:   agentruntime.ModelCompactor{Client: model.Client(client), Model: config.Model, MaxInputTokens: config.MaxContextTokens - config.AutocompactBuffer},
			Artifacts:   local.ArtifactStore{Root: filepath.Join(stateDir, "sessions")},
			Environment: environment.Config{WorkspaceRoots: []string{workspace.Root}},
		},
		ParentCatalog: catalog,
		AllowedTools:  delegation.DefaultLocalAllowedTools(),
	}
	if err := catalog.Register(delegation.ExploreTool{Runner: exploreRunner}); err != nil {
		return err
	}
	store, err := local.NewJSONLStore(filepath.Join(stateDir, "sessions"))
	if err != nil {
		return err
	}
	broker := appserver.NewApprovalBroker()
	approver := &local.ScopedApprover{Project: workspace.Root, Path: filepath.Join(stateDir, "approvals.json")}
	approver.Prompt = func(ctx context.Context, request tool.PolicyRequest, _ tool.Decision) (local.ApprovalScope, error) {
		scope, err := broker.Wait(ctx, request.Call.Scope.TurnID, request.Call.ID)
		return local.ApprovalScope(scope), err
	}
	server := appserver.New(nil, os.Stdin, os.Stdout)
	stream := &eventStream{server: server}
	runner, err := agentruntime.New(agentruntime.Config{
		Model: config.Model, ReasoningEffort: config.ReasoningEffort, MaxOutputTokens: config.MaxOutputTokens,
		Temperature: config.Temperature, MaxSteps: config.MaxSteps, MaxModelRetries: 2, MaxEmptyResponseRetries: 3,
		Context:        agentruntime.ContextConfig{MaxTokens: config.MaxContextTokens, ReserveTokens: config.AutocompactBuffer},
		CircuitBreaker: agentruntime.CircuitBreakerConfig{Enabled: true},
	}, agentruntime.Dependencies{
		Model: model.Client(client), Tools: catalog, Policy: local.WorkspacePolicy{}, Approver: approver, Transcript: store,
		Events:      transcript.SinkFunc(stream.publish),
		Compactor:   agentruntime.ModelCompactor{Client: model.Client(client), Model: config.Model, MaxInputTokens: config.MaxContextTokens - config.AutocompactBuffer},
		Artifacts:   local.ArtifactStore{Root: filepath.Join(stateDir, "sessions")},
		Environment: environment.Config{WorkspaceRoots: []string{workspace.Root}},
	})
	if err != nil {
		return err
	}
	server.Runtime = runner
	server.Transcript = store
	server.Model = config.Model
	server.Workspace = workspace.Root
	server.Approvals = broker
	server.Prompt = []prompt.Fragment{{ID: "appserver-core", Layer: prompt.LayerCore, Content: "You are a coding agent exposed through the app server protocol."}}
	return server.Serve(ctx)
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

type eventStream struct {
	server *appserver.Server
}

func (s *eventStream) publish(_ context.Context, event transcript.Event) {
	if s.server != nil {
		s.server.NotifyEvent(event)
	}
}
