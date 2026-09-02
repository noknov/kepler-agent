package main

import (
	"context"
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
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/appserver"
	"github.com/noknov/kepler-agent/packages/cloud"
	"github.com/noknov/kepler-agent/packages/profiles/local"
	"github.com/noknov/kepler-agent/packages/providers"
	localtools "github.com/noknov/kepler-agent/packages/tools/local"
)

func main() {
	ctx, stop := signalContext()
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	config, err := local.LoadConfig("")
	if err != nil {
		return err
	}
	token := os.Getenv("KEPLER_TOKEN")
	apiURL := os.Getenv("KEPLER_API_URL")
	if token == "" || apiURL == "" {
		return fmt.Errorf("set KEPLER_TOKEN and KEPLER_API_URL (run kepler-agent login)")
	}
	info, err := cloud.FetchBootstrap(ctx, apiURL, token)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	workspace, err := local.NewWorkspace(cwd)
	if err != nil {
		return err
	}
	defer workspace.Close()
	sandbox := local.Sandbox{Workspace: workspace, AdditionalReadRoots: config.AdditionalReadRoots, UnsafeAllowNoSandbox: config.UnsafeAllowNoSandbox}
	catalog, err := localtools.NewCatalog(workspace, sandbox)
	if err != nil {
		return err
	}
	client, err := providers.New(providers.Config{
		Provider: "kepler", Protocol: "kepler", BaseURL: apiURL,
		APIKey: token, Timeout: config.Timeout,
	})
	if err != nil {
		return err
	}
	stateDir, err := local.DefaultStateDir()
	if err != nil {
		return err
	}
	exploreRunner := delegation.Runner{
		Config: agentruntime.Config{
			Model: info.Model, ReasoningEffort: info.Thinking, MaxOutputTokens: config.MaxOutputTokens,
			MaxSteps: 12,
			Context: agentruntime.ContextConfig{MaxTokens: config.MaxContextTokens, ReserveTokens: config.AutocompactBuffer},
		},
		Deps: agentruntime.Dependencies{
			Model: model.Client(client), Policy: local.WorkspacePolicy{},
			Compactor:   agentruntime.ModelCompactor{Client: model.Client(client), Model: info.Model, MaxInputTokens: config.MaxContextTokens - config.AutocompactBuffer},
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
	server := appserver.New(nil, os.Stdin, os.Stdout)
	stream := &eventStream{server: server}
	approver := server.WireApprover(workspace.Root, filepath.Join(stateDir, "approvals.json"))
	runner, err := agentruntime.New(agentruntime.Config{
		Model: info.Model, ReasoningEffort: info.Thinking, MaxOutputTokens: config.MaxOutputTokens,
		MaxSteps: config.MaxSteps, MaxModelRetries: 2, MaxEmptyResponseRetries: 3,
		Context:        agentruntime.ContextConfig{MaxTokens: config.MaxContextTokens, ReserveTokens: config.AutocompactBuffer},
		CircuitBreaker: agentruntime.CircuitBreakerConfig{Enabled: true},
	}, agentruntime.Dependencies{
		Model: model.Client(client), Tools: catalog, Policy: local.WorkspacePolicy{}, Approver: approver, Transcript: store,
		Events:      transcript.SinkFunc(stream.publish),
		Compactor:   agentruntime.ModelCompactor{Client: model.Client(client), Model: info.Model, MaxInputTokens: config.MaxContextTokens - config.AutocompactBuffer},
		Artifacts:   local.ArtifactStore{Root: filepath.Join(stateDir, "sessions")},
		Environment: environment.Config{WorkspaceRoots: []string{workspace.Root}},
	})
	if err != nil {
		return err
	}
	server.Runtime = runner
	server.Transcript = store
	server.Model = info.Model
	server.Workspace = workspace.Root
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
