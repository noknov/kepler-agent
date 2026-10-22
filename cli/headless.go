package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/delegation"
	"github.com/noknov/kepler-agent/packages/agent/environment"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/infra/telemetry"
	"github.com/noknov/kepler-agent/packages/profiles/local"
	"github.com/noknov/kepler-agent/packages/providers"
	localtools "github.com/noknov/kepler-agent/packages/tools/local"
)

type localHarness struct {
	session   string
	workspace local.Workspace
	runner    *agentruntime.Runtime
	fragments []prompt.Fragment
	renderer  *eventRenderer
}

func runHeadless(values options, config local.Config, creds credentials) error {
	harness, err := newLocalHarness(values, config, creds)
	if err != nil {
		return err
	}
	defer harness.workspace.Close()

	input, err := headlessInput(flag.Args(), os.Stdin)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input) == "" {
		return errors.New("prompt is empty")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	shutdownTelemetry, err := telemetry.Setup(ctx, "kepler-agent-cli")
	if err != nil {
		return fmt.Errorf("configure telemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTelemetry(shutdownCtx)
	}()

	result, err := harness.runner.RunTurn(ctx, turnRequest(harness.session, harness.workspace.Root, input, harness.fragments, nil))
	harness.renderer.finish(result)
	return err
}

func newLocalHarness(values options, config local.Config, creds credentials) (*localHarness, error) {
	workspace, err := local.NewWorkspace(values.cwd)
	if err != nil {
		return nil, err
	}
	stateDir := values.stateDir
	if stateDir == "" {
		stateDir, err = local.DefaultStateDir()
		if err != nil {
			workspace.Close()
			return nil, err
		}
	}
	store, err := local.NewJSONLStore(filepath.Join(stateDir, "sessions"))
	if err != nil {
		workspace.Close()
		return nil, err
	}
	session, err := resolveSessionID(values, store)
	if err != nil {
		workspace.Close()
		return nil, err
	}

	client, err := providers.New(providers.Config{
		Provider: "kepler", Protocol: "kepler", BaseURL: creds.APIURL,
		APIKey: creds.Token, Timeout: config.Timeout, OutputTokens: config.MaxOutputTokens,
	})
	if err != nil {
		workspace.Close()
		return nil, err
	}
	resilientClient := model.Client(&model.ResilientClient{Primary: client, PrimaryProvider: "kepler"})

	sandbox := local.Sandbox{Workspace: workspace, AdditionalReadRoots: config.AdditionalReadRoots, UnsafeAllowNoSandbox: config.UnsafeAllowNoSandbox}
	catalog, err := localtools.NewCatalog(workspace, sandbox)
	if err != nil {
		workspace.Close()
		return nil, err
	}
	skillText, err := registerSkillsAndMCP(context.Background(), catalog, workspace, config)
	if err != nil {
		workspace.Close()
		return nil, err
	}
	fragments, err := prompts(config, workspace.Root, skillText)
	if err != nil {
		workspace.Close()
		return nil, err
	}

	scope := local.ApprovalScope(values.approval)
	if scope != local.ApprovalDeny && scope != local.ApprovalOnce && scope != local.ApprovalSession && scope != local.ApprovalProject {
		workspace.Close()
		return nil, fmt.Errorf("invalid --approval value %q", values.approval)
	}
	approver := &local.ScopedApprover{
		Project: workspace.Root,
		Path:    filepath.Join(stateDir, "approvals.json"),
		Prompt: func(context.Context, tool.PolicyRequest, tool.Decision) (local.ApprovalScope, error) {
			return scope, nil
		},
	}

	artifactRoot := filepath.Join(stateDir, "sessions")
	exploreRunner := delegation.Runner{
		Config: agentruntime.Config{
			Model: config.Model, ReasoningEffort: config.ReasoningEffort, MaxOutputTokens: config.MaxOutputTokens,
			MaxSteps: 12,
			Context:  agentruntime.ContextConfig{MaxTokens: config.MaxContextTokens, ReserveTokens: config.AutocompactBuffer},
		},
		Deps: agentruntime.Dependencies{
			TraceContent: telemetry.ContentRecorder(),
			Model:        resilientClient, Policy: local.NewWorkspacePolicy(), Transcript: store,
			Compactor:   agentruntime.ModelCompactor{Client: resilientClient, Model: config.Model, MaxInputTokens: config.MaxContextTokens - config.AutocompactBuffer},
			Artifacts:   local.ArtifactStore{Root: artifactRoot},
			Environment: environment.Config{WorkspaceRoots: []string{workspace.Root}},
		},
		ParentCatalog: catalog,
		AllowedTools:  delegation.DefaultLocalAllowedTools(),
	}
	if err := catalog.Register(delegation.EvidenceTool{Store: store}); err != nil {
		workspace.Close()
		return nil, err
	}
	if err := catalog.Register(delegation.ExploreTool{Runner: exploreRunner}); err != nil {
		workspace.Close()
		return nil, err
	}

	renderer := newEventRenderer(config.Output, os.Stdout, os.Stderr)
	runner, err := agentruntime.New(
		agentruntime.Config{
			Model: config.Model, ReasoningEffort: config.ReasoningEffort, MaxOutputTokens: config.MaxOutputTokens,
			MaxTotalTokens: config.MaxTotalTokens, MaxSteps: config.MaxSteps, MaxParallelToolCalls: config.MaxParallelToolCalls, MaxModelRetries: 0, MaxEmptyResponseRetries: 3,
			Context:        agentruntime.ContextConfig{MaxTokens: config.MaxContextTokens, ReserveTokens: config.AutocompactBuffer},
			CircuitBreaker: agentruntime.CircuitBreakerConfig{Enabled: true},
		},
		agentruntime.Dependencies{
			TraceContent: telemetry.ContentRecorder(),
			Model:        resilientClient, Tools: catalog, Policy: local.NewWorkspacePolicy(), Approver: approver, Transcript: store, Events: renderer,
			Compactor:   agentruntime.ModelCompactor{Client: resilientClient, Model: config.Model, MaxInputTokens: config.MaxContextTokens - config.AutocompactBuffer},
			Artifacts:   local.ArtifactStore{Root: artifactRoot},
			Environment: environment.Config{WorkspaceRoots: []string{workspace.Root}},
		},
	)
	if err != nil {
		workspace.Close()
		return nil, err
	}
	return &localHarness{session: session, workspace: workspace, runner: runner, fragments: fragments, renderer: renderer}, nil
}

func resolveSessionID(values options, store *local.JSONLStore) (string, error) {
	session := values.session
	if values.resume {
		sessions, err := store.ListSessions()
		if err != nil {
			return "", err
		}
		if len(sessions) == 0 {
			return "", errors.New("no prior session to resume")
		}
		return sessions[0].ID, nil
	}
	if session == "" {
		session = newID("ses")
	}
	return session, nil
}

func registerSkillsAndMCP(ctx context.Context, catalog *tool.Catalog, workspace local.Workspace, config local.Config) (string, error) {
	return localtools.RegisterSkillsAndMCP(ctx, catalog, workspace, config)
}

func prompts(config local.Config, root, skillPrompt string) ([]prompt.Fragment, error) {
	return localtools.BuildPrompts(
		"cli-core",
		"You are a coding agent in a local workspace. Use the available tools to inspect evidence and verify material changes. Preserve unrelated work and report outcomes and limitations precisely.",
		config,
		root,
		skillPrompt,
	)
}

func turnRequest(session, workspace, input string, fragments []prompt.Fragment, steering agentruntime.InputSource) agentruntime.TurnRequest {
	return agentruntime.TurnRequest{
		SessionID: session,
		Input:     model.TextMessage(model.RoleUser, input),
		Prompt:    fragments,
		Scope:     tool.Scope{SessionID: session, Workspace: workspace, Values: map[string]string{"surface": "cli"}},
		Steering:  steering,
	}
}

func headlessInput(arguments []string, reader io.Reader) (string, error) {
	if len(arguments) > 0 {
		return strings.Join(arguments, " "), nil
	}
	data, err := io.ReadAll(reader)
	return string(data), err
}

func newID(prefix string) string {
	data := make([]byte, 12)
	_, _ = rand.Read(data)
	return prefix + "_" + hex.EncodeToString(data)
}
