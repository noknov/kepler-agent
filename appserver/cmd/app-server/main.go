package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/noknov/slack-copilot-agent/packages/agent/environment"
	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agent/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
	"github.com/noknov/slack-copilot-agent/packages/appserver"
	"github.com/noknov/slack-copilot-agent/packages/profiles/local"
	"github.com/noknov/slack-copilot-agent/packages/providers"
	localtools "github.com/noknov/slack-copilot-agent/packages/tools/local"
)

func main() {
	if err := run(signalContext()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	config, err := local.LoadConfig("")
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
	key := os.Getenv(config.APIKeyEnv)
	if key == "" {
		return fmt.Errorf("model API key environment variable %s is empty", config.APIKeyEnv)
	}
	client, err := providers.New(providers.Config{Provider: config.Provider, Protocol: config.Protocol, BaseURL: config.BaseURL, APIKey: key, AnthropicFlavor: config.AnthropicFlavor, Timeout: config.Timeout})
	if err != nil {
		return err
	}
	stateDir, err := local.DefaultStateDir()
	if err != nil {
		return err
	}
	store, err := local.NewJSONLStore(filepath.Join(stateDir, "sessions"))
	if err != nil {
		return err
	}
	server := appserver.New(nil, os.Stdin, os.Stdout)
	stream := &eventStream{server: server}
	runner, err := agentruntime.New(agentruntime.Config{
		Model: config.Model, ReasoningEffort: config.ReasoningEffort, MaxOutputTokens: config.MaxOutputTokens,
		Temperature: config.Temperature, MaxSteps: config.MaxSteps, MaxModelRetries: 2,
		Context:        agentruntime.ContextConfig{MaxTokens: config.MaxContextTokens, ReserveTokens: config.AutocompactBuffer},
		CircuitBreaker: agentruntime.CircuitBreakerConfig{Enabled: true},
	}, agentruntime.Dependencies{
		Model: model.Client(client), Tools: catalog, Policy: local.WorkspacePolicy{}, Transcript: store,
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
	server.Prompt = []prompt.Fragment{{ID: "appserver-core", Layer: prompt.LayerCore, Content: "You are a coding agent exposed through the app server protocol."}}
	return server.Serve(ctx)
}

func signalContext() context.Context {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx
}

type eventStream struct {
	server *appserver.Server
}

func (s *eventStream) publish(_ context.Context, event transcript.Event) {
	if s.server != nil {
		s.server.NotifyEvent(event)
	}
}
