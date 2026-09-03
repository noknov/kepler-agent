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
	"github.com/noknov/kepler-agent/packages/mcp"
	"github.com/noknov/kepler-agent/packages/profiles/local"
	"github.com/noknov/kepler-agent/packages/providers"
	localtools "github.com/noknov/kepler-agent/packages/tools/local"
	mcptools "github.com/noknov/kepler-agent/packages/tools/mcp"
	"github.com/noknov/kepler-agent/packages/tools/skills"
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
		APIKey: creds.Token, Timeout: config.Timeout,
	})
	if err != nil {
		workspace.Close()
		return nil, err
	}

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
			Model: client, Policy: local.WorkspacePolicy{}, Transcript: store,
			Compactor:   agentruntime.ModelCompactor{Client: client, Model: config.Model, MaxInputTokens: config.MaxContextTokens - config.AutocompactBuffer},
			Artifacts:   local.ArtifactStore{Root: artifactRoot},
			Environment: environment.Config{WorkspaceRoots: []string{workspace.Root}},
		},
		ParentCatalog: catalog,
		AllowedTools:  delegation.DefaultLocalAllowedTools(),
	}
	if err := catalog.Register(delegation.ExploreTool{Runner: exploreRunner}); err != nil {
		workspace.Close()
		return nil, err
	}

	renderer := newEventRenderer(config.Output, os.Stdout, os.Stderr)
	runner, err := agentruntime.New(
		agentruntime.Config{
			Model: config.Model, ReasoningEffort: config.ReasoningEffort, MaxOutputTokens: config.MaxOutputTokens,
			MaxSteps: config.MaxSteps, MaxModelRetries: 2, MaxEmptyResponseRetries: 3,
			Context: agentruntime.ContextConfig{MaxTokens: config.MaxContextTokens, ReserveTokens: config.AutocompactBuffer},
		},
		agentruntime.Dependencies{
			Model: client, Tools: catalog, Policy: local.WorkspacePolicy{}, Approver: approver, Transcript: store, Events: renderer,
			Compactor:   agentruntime.ModelCompactor{Client: client, Model: config.Model, MaxInputTokens: config.MaxContextTokens - config.AutocompactBuffer},
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
	roots := []string{
		filepath.Join(workspace.Root, ".agents", "skills"),
		filepath.Join(workspace.Root, ".codex", "skills"),
	}
	roots = append(roots, config.SkillRoots...)
	skillCatalog, err := skills.Discover(roots)
	if err != nil {
		return "", err
	}
	if skillCatalog.Prompt() != "" {
		if err := catalog.Register(skillCatalog.Tool()); err != nil {
			return "", err
		}
	}
	for _, server := range config.MCPServers {
		if server.Name == "" || server.URL == "" {
			return "", errors.New("every mcp_servers entry requires name and url")
		}
		items, discoverErr := mcptools.Discover(ctx, mcptools.Server{
			Name: server.Name,
			Client: &mcp.Client{
				ServiceName: server.Name,
				URL:         server.URL,
				Token:       os.Getenv(server.TokenEnv),
				Headers:     server.Headers,
			},
			Effects: effects(server.Effects),
		})
		if discoverErr != nil {
			return "", fmt.Errorf("discover MCP server %s: %w", server.Name, discoverErr)
		}
		for _, item := range items {
			if registerErr := catalog.Register(item); registerErr != nil {
				return "", registerErr
			}
		}
	}
	return skillCatalog.Prompt(), nil
}

func prompts(config local.Config, root, skillPrompt string) ([]prompt.Fragment, error) {
	fragments := []prompt.Fragment{
		{ID: "cli-core", Version: "2", Layer: prompt.LayerCore, Content: "You are a coding agent in a local workspace. Inspect evidence before making claims. Emit independent reads in one step. Prefer exec command strings for tests and builds. Verify material changes, preserve unrelated work, and report limitations precisely."},
		{ID: "local-product", Version: "4", Layer: prompt.LayerProduct, Content: "You are running locally with Kepler-hosted models. Filesystem writes must stay within the workspace. Exec network access requires explicit approval. Never seek or expose credentials. Do not ask the user for provider API keys."},
		{ID: "local-environment", Layer: prompt.LayerEnvironment, Content: "Workspace: " + root},
	}
	project, err := local.ProjectInstructions(root)
	if err != nil {
		return nil, err
	}
	for index, value := range project {
		fragments = append(fragments, prompt.Fragment{ID: fmt.Sprintf("project-%d", index), Layer: prompt.LayerProject, Content: value, Order: index})
	}
	overlays, err := local.LoadPromptFiles(config.PromptFiles)
	if err != nil {
		return nil, err
	}
	for index, value := range overlays {
		fragments = append(fragments, prompt.Fragment{ID: fmt.Sprintf("user-overlay-%d", index), Layer: prompt.LayerUser, Content: value, Order: index})
	}
	if skillPrompt != "" {
		fragments = append(fragments, prompt.Fragment{ID: "file-skills", Layer: prompt.LayerSkill, Content: skillPrompt})
	}
	return fragments, nil
}

func effects(values []string) []tool.Effect {
	items := make([]tool.Effect, 0, len(values))
	for _, value := range values {
		switch tool.Effect(value) {
		case tool.EffectRead, tool.EffectWorkspaceWrite, tool.EffectExternalWrite, tool.EffectPrivileged, tool.EffectNetwork:
			items = append(items, tool.Effect(value))
		}
	}
	if len(items) == 0 {
		items = append(items, tool.EffectExternalWrite)
	}
	return items
}

func turnRequest(session, workspace, input string, fragments []prompt.Fragment, steering agentruntime.InputSource) agentruntime.TurnRequest {
	return agentruntime.TurnRequest{
		SessionID: session,
		Input:     model.TextMessage(model.RoleUser, input),
		Prompt:    fragments,
		Scope:     tool.Scope{SessionID: session, Workspace: workspace},
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
