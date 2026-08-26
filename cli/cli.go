package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/delegation"
	"github.com/noknov/kepler-agent/packages/agent/environment"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/infra/telemetry"
	"github.com/noknov/kepler-agent/packages/mcp"
	"github.com/noknov/kepler-agent/packages/profiles/local"
	"github.com/noknov/kepler-agent/packages/providers"
	"github.com/noknov/kepler-agent/packages/tools/local"
	"github.com/noknov/kepler-agent/packages/tools/mcp"
	"github.com/noknov/kepler-agent/packages/tools/skills"
)

type options struct {
	configPath, cwd, stateDir, provider, protocol, model, baseURL, apiKeyEnv, routing, output, session, approval, profile string
	resume, unsafe                                                                                                        bool
}

type turnDone struct {
	result agentruntime.TurnResult
	err    error
}
type approvalQuestion struct {
	request  tool.PolicyRequest
	decision tool.Decision
	answer   chan local.ApprovalScope
}

func Run() error {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Fprintln(os.Stdout, "kepler-agent (local CLI harness)")
		return nil
	}
	if len(os.Args) > 1 && os.Args[1] == "connect" {
		return runConnect(os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "config" {
		return runConfig(os.Args[2:])
	}
	var values options
	flag.StringVar(&values.configPath, "config", "", "configuration TOML path")
	flag.StringVar(&values.profile, "profile", "", "named model profile from config")
	flag.StringVar(&values.cwd, "cwd", ".", "workspace root")
	flag.StringVar(&values.stateDir, "state-dir", "", "session and approval state directory")
	flag.StringVar(&values.provider, "provider", "", "openai or anthropic")
	flag.StringVar(&values.protocol, "protocol", "", "openai, responses, or anthropic")
	flag.StringVar(&values.model, "model", "", "model name")
	flag.StringVar(&values.baseURL, "base-url", "", "provider API base URL")
	flag.StringVar(&values.apiKeyEnv, "api-key-env", "", "environment variable containing the API key")
	flag.StringVar(&values.routing, "input-routing", "", "steer or queue")
	flag.StringVar(&values.output, "output", "", "text or jsonl")
	flag.StringVar(&values.session, "session", "", "session ID to create or resume")
	flag.BoolVar(&values.resume, "resume", false, "resume the most recently modified session")
	flag.StringVar(&values.approval, "approval", "deny", "headless approval: deny, once, session, or project")
	flag.BoolVar(&values.unsafe, "unsafe-allow-no-sandbox", false, "run commands without an OS sandbox if unavailable")
	flag.Parse()

	config, err := local.LoadConfig(values.configPath)
	if err != nil {
		return err
	}
	if config, err = config.WithProfile(values.profile); err != nil {
		return err
	}
	visited := make(map[string]bool)
	flag.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	if visited["provider"] {
		config.Provider = values.provider
	}
	if visited["protocol"] {
		config.Protocol = values.protocol
	}
	if visited["model"] {
		config.Model = values.model
	}
	if visited["base-url"] {
		config.BaseURL = values.baseURL
	}
	if visited["api-key-env"] {
		config.APIKeyEnv = values.apiKeyEnv
	}
	if visited["input-routing"] {
		config.InputRouting = values.routing
	}
	if visited["output"] {
		config.Output = values.output
	}
	if visited["unsafe-allow-no-sandbox"] {
		config.UnsafeAllowNoSandbox = values.unsafe
	}
	if err := config.Validate(); err != nil {
		return err
	}
	if config.Model == "" {
		return errors.New("model is required (set model in config.toml or pass --model)")
	}

	workspace, err := local.NewWorkspace(values.cwd)
	if err != nil {
		return err
	}
	defer workspace.Close()
	if values.stateDir == "" {
		values.stateDir, err = local.DefaultStateDir()
		if err != nil {
			return err
		}
	}
	store, err := local.NewJSONLStore(filepath.Join(values.stateDir, "sessions"))
	if err != nil {
		return err
	}
	if values.resume {
		sessions, listErr := store.ListSessions()
		if listErr != nil {
			return listErr
		}
		if len(sessions) == 0 {
			return errors.New("no prior session to resume")
		}
		values.session = sessions[0].ID
	}
	if values.session == "" {
		values.session = newID("ses")
	}

	client, err := modelClient(config)
	if err != nil {
		return err
	}
	sandbox := local.Sandbox{Workspace: workspace, AdditionalReadRoots: config.AdditionalReadRoots, UnsafeAllowNoSandbox: config.UnsafeAllowNoSandbox}
	catalog, err := localtools.NewCatalog(workspace, sandbox)
	if err != nil {
		return err
	}
	skillRoots := []string{filepath.Join(workspace.Root, ".agents", "skills"), filepath.Join(workspace.Root, ".codex", "skills")}
	skillRoots = append(skillRoots, config.SkillRoots...)
	skillCatalog, err := skills.Discover(skillRoots)
	if err != nil {
		return err
	}
	if skillCatalog.Prompt() != "" {
		if err := catalog.Register(skillCatalog.Tool()); err != nil {
			return err
		}
	}
	for _, server := range config.MCPServers {
		if server.Name == "" || server.URL == "" {
			return errors.New("every mcp_servers entry requires name and url")
		}
		items, discoverErr := mcptools.Discover(context.Background(), mcptools.Server{Name: server.Name, Client: &mcp.Client{ServiceName: server.Name, URL: server.URL, Token: os.Getenv(server.TokenEnv), Headers: server.Headers}, Effects: effects(server.Effects)})
		if discoverErr != nil {
			return fmt.Errorf("discover MCP server %s: %w", server.Name, discoverErr)
		}
		for _, item := range items {
			if registerErr := catalog.Register(item); registerErr != nil {
				return registerErr
			}
		}
	}
	fragments, err := prompts(config, workspace.Root, skillCatalog.Prompt())
	if err != nil {
		return err
	}
	renderer := &eventRenderer{mode: config.Output, stdout: os.Stdout, stderr: os.Stderr, color: config.Output == "text" && isTerminal(os.Stdout), started: make(map[string]time.Time)}

	interactive := len(flag.Args()) == 0 && isTerminal(os.Stdin)
	questions := make(chan approvalQuestion)
	approver := &local.ScopedApprover{Project: workspace.Root, Path: filepath.Join(values.stateDir, "approvals.json")}
	if interactive {
		approver.Prompt = func(ctx context.Context, request tool.PolicyRequest, decision tool.Decision) (local.ApprovalScope, error) {
			question := approvalQuestion{request: request, decision: decision, answer: make(chan local.ApprovalScope, 1)}
			select {
			case questions <- question:
			case <-ctx.Done():
				return local.ApprovalDeny, ctx.Err()
			}
			select {
			case answer := <-question.answer:
				return answer, nil
			case <-ctx.Done():
				return local.ApprovalDeny, ctx.Err()
			}
		}
	} else {
		scope := local.ApprovalScope(values.approval)
		if scope != local.ApprovalDeny && scope != local.ApprovalOnce && scope != local.ApprovalSession && scope != local.ApprovalProject {
			return fmt.Errorf("invalid --approval value %q", values.approval)
		}
		approver.Prompt = func(context.Context, tool.PolicyRequest, tool.Decision) (local.ApprovalScope, error) {
			return scope, nil
		}
	}

	exploreRunner := delegation.Runner{
		Config: agentruntime.Config{
			Model: config.Model, ReasoningEffort: config.ReasoningEffort, MaxOutputTokens: config.MaxOutputTokens,
			Temperature: config.Temperature, MaxSteps: 12,
			Context: agentruntime.ContextConfig{MaxTokens: config.MaxContextTokens, ReserveTokens: config.AutocompactBuffer},
		},
		Deps: agentruntime.Dependencies{
			Model: client, Policy: local.WorkspacePolicy{},
			Transcript:  store,
			Compactor:   agentruntime.ModelCompactor{Client: client, Model: config.Model, MaxInputTokens: config.MaxContextTokens - config.AutocompactBuffer},
			Artifacts:   local.ArtifactStore{Root: filepath.Join(values.stateDir, "sessions")},
			Environment: environment.Config{WorkspaceRoots: []string{workspace.Root}},
		},
		ParentCatalog: catalog,
		AllowedTools:  delegation.DefaultLocalAllowedTools(),
	}
	if err := catalog.Register(delegation.ExploreTool{Runner: exploreRunner}); err != nil {
		return err
	}

	runner, err := agentruntime.New(agentruntime.Config{Model: config.Model, ReasoningEffort: config.ReasoningEffort, MaxOutputTokens: config.MaxOutputTokens, Temperature: config.Temperature, MaxSteps: config.MaxSteps, MaxModelRetries: 2, MaxEmptyResponseRetries: 3, Context: agentruntime.ContextConfig{MaxTokens: config.MaxContextTokens, ReserveTokens: config.AutocompactBuffer}}, agentruntime.Dependencies{
		Model: client, Tools: catalog, Policy: local.WorkspacePolicy{}, Approver: approver, Transcript: store, Events: renderer,
		Compactor: agentruntime.ModelCompactor{Client: client, Model: config.Model, MaxInputTokens: config.MaxContextTokens - config.AutocompactBuffer}, Artifacts: local.ArtifactStore{Root: filepath.Join(values.stateDir, "sessions")},
		Environment: environment.Config{WorkspaceRoots: []string{workspace.Root}},
	})
	if err != nil {
		return err
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
	if interactive {
		return interactiveLoop(ctx, runner, values.session, workspace.Root, fragments, config.InputRouting, questions, config, renderer)
	}
	input, err := headlessInput(flag.Args(), os.Stdin)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input) == "" {
		return errors.New("prompt is empty")
	}
	result, err := runner.RunTurn(ctx, turnRequest(values.session, workspace.Root, input, fragments, nil))
	renderer.finish(result)
	return err
}

func modelClient(config local.Config) (model.Client, error) {
	key := os.Getenv(config.APIKeyEnv)
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("model API key environment variable %s is empty", config.APIKeyEnv)
	}
	return providers.New(providers.Config{Provider: config.Provider, Protocol: config.Protocol, BaseURL: config.BaseURL, APIKey: key, AnthropicFlavor: config.AnthropicFlavor, Timeout: config.Timeout})
}

func prompts(config local.Config, root, skillPrompt string) ([]prompt.Fragment, error) {
	fragments := []prompt.Fragment{
		{ID: "cli-core", Version: "1", Layer: prompt.LayerCore, Content: "You are a coding agent. Inspect evidence before making claims. Use tools to complete the user request, verify material changes, preserve unrelated work, and report limitations precisely."},
		{ID: "local-product", Version: "3", Layer: prompt.LayerProduct, Content: "You are running locally. Filesystem writes must stay within the workspace. Exec network access requires explicit approval. Never seek or expose credentials."},
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
	return agentruntime.TurnRequest{SessionID: session, Input: model.TextMessage(model.RoleUser, input), Prompt: fragments, Scope: tool.Scope{SessionID: session, Workspace: workspace}, Steering: steering}
}

func interactiveLoop(ctx context.Context, runner *agentruntime.Runtime, session, workspace string, fragments []prompt.Fragment, routing string, questions <-chan approvalQuestion, config local.Config, renderer *eventRenderer) error {
	lines := make(chan string)
	scanErrors := make(chan error, 1)
	go scanLines(os.Stdin, lines, scanErrors)
	renderer.welcome(session, workspace, config, routing)
	var queued []string
	inputClosed := false
	for {
		var input string
		if len(queued) > 0 {
			input, queued = queued[0], queued[1:]
		} else {
			fmt.Fprint(os.Stderr, "> ")
			select {
			case <-ctx.Done():
				return nil
			case err := <-scanErrors:
				return err
			case value, ok := <-lines:
				if !ok {
					return nil
				}
				input = value
			}
		}
		if strings.TrimSpace(input) == "" {
			continue
		}
		if input == "/exit" || input == "/quit" {
			return nil
		}
		if handled := renderer.command(input, session, workspace, config); handled {
			continue
		}
		steering := &agentruntime.InputBuffer{}
		done := make(chan turnDone, 1)
		go func(value string) {
			result, err := runner.RunTurn(ctx, turnRequest(session, workspace, value, fragments, steering))
			done <- turnDone{result, err}
		}(input)
		for {
			select {
			case <-ctx.Done():
				return nil
			case question := <-questions:
				fmt.Fprintf(os.Stderr, "\nApproval required for %s: %s\nArguments: %s\n[o]nce, [s]ession, [p]roject, [n]o? ", question.request.Call.Name, question.decision.Reason, question.request.Call.Arguments)
				if inputClosed {
					question.answer <- local.ApprovalDeny
					continue
				}
				select {
				case answer, ok := <-lines:
					if !ok {
						question.answer <- local.ApprovalDeny
					} else {
						question.answer <- parseApproval(answer)
					}
				case <-ctx.Done():
					question.answer <- local.ApprovalDeny
					return nil
				}
			case value, ok := <-lines:
				if !ok {
					lines = nil
					inputClosed = true
					continue
				}
				if routing == "steer" {
					if steering.Push(model.TextMessage(model.RoleUser, value)) {
						fmt.Fprintln(os.Stderr, "\n[steering accepted]")
					} else {
						queued = append(queued, value)
						fmt.Fprintln(os.Stderr, "\n[turn already finishing; input queued]")
					}
				} else {
					queued = append(queued, value)
					fmt.Fprintln(os.Stderr, "\n[input queued]")
				}
			case outcome := <-done:
				for _, pending := range steering.Drain() {
					if text := strings.TrimSpace(pending.Text()); text != "" {
						queued = append(queued, text)
					}
				}
				fmt.Fprintln(os.Stderr)
				if outcome.err != nil {
					fmt.Fprintln(os.Stderr, "turn:", outcome.err)
				}
				goto nextTurn
			}
		}
	nextTurn:
	}
}

func scanLines(reader io.Reader, lines chan<- string, failures chan<- error) {
	defer close(lines)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		lines <- scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		failures <- err
	}
}

func parseApproval(value string) local.ApprovalScope {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "o", "once", "y", "yes":
		return local.ApprovalOnce
	case "s", "session":
		return local.ApprovalSession
	case "p", "project":
		return local.ApprovalProject
	default:
		return local.ApprovalDeny
	}
}

func headlessInput(arguments []string, reader io.Reader) (string, error) {
	if len(arguments) > 0 {
		return strings.Join(arguments, " "), nil
	}
	data, err := io.ReadAll(reader)
	return string(data), err
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
func newID(prefix string) string {
	data := make([]byte, 12)
	_, _ = rand.Read(data)
	return prefix + "_" + hex.EncodeToString(data)
}

type eventRenderer struct {
	mode           string
	stdout, stderr io.Writer
	mu             sync.Mutex
	color          bool
	started        map[string]time.Time
}

func (r *eventRenderer) Publish(_ context.Context, event transcript.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mode == "jsonl" {
		data, _ := json.Marshal(event)
		fmt.Fprintln(r.stdout, string(data))
		return
	}
	if event.Type == transcript.ModelStreamed && event.Model != nil && event.Model.Type == model.StreamTextDelta {
		fmt.Fprint(r.stdout, event.Model.Text)
	}
	if event.Type == transcript.ToolCallStarted && event.ToolCall != nil {
		r.started[event.ToolCall.ID] = time.Now()
		fmt.Fprintf(r.stderr, "\n%s %s\n", r.paint("●", "36"), r.paint(toolLabel(event.ToolCall.Name), "1;37"))
	}
	if (event.Type == transcript.ToolCallCompleted || event.Type == transcript.ToolCallFailed) && event.ToolCall != nil {
		elapsed := time.Since(r.started[event.ToolCall.ID]).Round(10 * time.Millisecond)
		marker, color := "✓", "32"
		if event.Type == transcript.ToolCallFailed {
			marker, color = "×", "31"
		}
		fmt.Fprintf(r.stderr, "%s %s %s\n", r.paint(marker, color), toolLabel(event.ToolCall.Name), r.paint(elapsed.String(), "2"))
		delete(r.started, event.ToolCall.ID)
	}
}
func (r *eventRenderer) finish(result agentruntime.TurnResult) {
	if r.mode == "text" && result.Message.Text() != "" {
		fmt.Fprintln(r.stdout)
	}
}

func (r *eventRenderer) paint(value, code string) string {
	if !r.color {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func (r *eventRenderer) welcome(session, workspace string, config local.Config, routing string) {
	if !r.color {
		fmt.Fprintf(r.stderr, "kepler-agent · %s · %s\n", config.Model, workspace)
		return
	}
	fmt.Fprintf(r.stderr, "\n%s\n%s  %s  %s\n%s\n\n", r.paint("✦  kepler-agent", "1;36"), r.paint(config.Model, "1;37"), r.paint(config.Provider+"/"+config.Protocol, "2"), r.paint("session "+session[len(session)-8:], "2"), r.paint("workspace  "+workspace+"  ·  /help for commands  ·  /exit to quit", "2"))
}

func (r *eventRenderer) command(input, session, workspace string, config local.Config) bool {
	switch strings.TrimSpace(input) {
	case "/help":
		fmt.Fprint(r.stderr, "\n  /help    commands     /status  current configuration     /exit  quit\n  while working: type another message to steer (or queue) the agent\n\n")
	case "/status":
		fmt.Fprintf(r.stderr, "\n  model     %s\n  provider  %s/%s\n  workspace %s\n  session   %s\n  key env   %s\n\n", config.Model, config.Provider, config.Protocol, workspace, session, config.APIKeyEnv)
	case "/clear":
		if r.color {
			fmt.Fprint(r.stderr, "\x1b[2J\x1b[H")
		}
	default:
		return false
	}
	return true
}

func toolLabel(name string) string { return strings.ReplaceAll(name, "_", " ") }
