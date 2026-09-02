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
	localtools "github.com/noknov/kepler-agent/packages/tools/local"
	mcptools "github.com/noknov/kepler-agent/packages/tools/mcp"
	"github.com/noknov/kepler-agent/packages/tools/skills"
)

type options struct {
	configPath, cwd, stateDir, routing, output, session, approval, apiURL string
	resume, unsafe                                                        bool
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
		if DefaultAPIURL != "" {
			fmt.Fprintf(os.Stdout, "kepler-agent (local CLI harness)\n%s\n", DefaultAPIURL)
		} else {
			fmt.Fprintln(os.Stdout, "kepler-agent (local CLI harness)")
		}
		return nil
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "connect":
			return runConnect(os.Args[2:])
		case "config":
			return runConfig(os.Args[2:])
		case "login":
			return runLogin(os.Args[2:])
		case "logout":
			return runLogout()
		case "whoami":
			return runWhoami()
		}
	}
	var values options
	flag.StringVar(&values.configPath, "config", "", "configuration TOML path")
	flag.StringVar(&values.cwd, "cwd", ".", "workspace root")
	flag.StringVar(&values.stateDir, "state-dir", "", "session and approval state directory")
	flag.StringVar(&values.routing, "input-routing", "", "steer or queue")
	flag.StringVar(&values.output, "output", "", "text or jsonl")
	flag.StringVar(&values.session, "session", "", "session ID to create or resume")
	flag.BoolVar(&values.resume, "resume", false, "resume the most recently modified session")
	flag.StringVar(&values.approval, "approval", "deny", "headless approval: deny, once, session, or project")
	flag.BoolVar(&values.unsafe, "unsafe-allow-no-sandbox", false, "run commands without an OS sandbox if unavailable")
	flag.StringVar(&values.apiURL, "api-url", "", "Kepler gateway URL (overrides credentials)")
	flag.Parse()

	config, err := local.LoadConfig(values.configPath)
	if err != nil {
		return err
	}
	visited := make(map[string]bool)
	flag.Visit(func(item *flag.Flag) { visited[item.Name] = true })
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

	creds, err := resolveCredentials(values.apiURL)
	if err != nil {
		return err
	}
	bootstrap, err := fetchBootstrap(context.Background(), creds)
	if err != nil {
		return err
	}
	config = applyBootstrap(config, bootstrap)

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

	client, err := providers.New(providers.Config{
		Provider: "kepler", Protocol: "kepler", BaseURL: creds.APIURL,
		APIKey: creds.Token, Timeout: config.Timeout,
	})
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
	interactive := len(flag.Args()) == 0 && isTerminal(os.Stdin)
	out := os.Stdout
	if interactive && (config.Output == "text" || config.Output == "") {
		out = os.Stderr
	}
	renderer := &eventRenderer{mode: config.Output, stdout: out, stderr: os.Stderr, color: config.Output == "text" && isTerminal(os.Stderr), started: make(map[string]time.Time)}
	var eventSink transcript.Sink = renderer
	var forward *forwardSink
	useTUI := interactive && (config.Output == "text" || config.Output == "")
	if useTUI {
		forward = newForwardSink()
		eventSink = forward
	}
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
			MaxSteps: 12,
			Context:  agentruntime.ContextConfig{MaxTokens: config.MaxContextTokens, ReserveTokens: config.AutocompactBuffer},
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

	runner, err := agentruntime.New(agentruntime.Config{Model: config.Model, ReasoningEffort: config.ReasoningEffort, MaxOutputTokens: config.MaxOutputTokens, MaxSteps: config.MaxSteps, MaxModelRetries: 2, MaxEmptyResponseRetries: 3, Context: agentruntime.ContextConfig{MaxTokens: config.MaxContextTokens, ReserveTokens: config.AutocompactBuffer}}, agentruntime.Dependencies{
		Model: client, Tools: catalog, Policy: local.WorkspacePolicy{}, Approver: approver, Transcript: store, Events: eventSink,
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
		if useTUI {
			cwd := workspace.Root
			if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(cwd, home) {
				cwd = "~" + strings.TrimPrefix(cwd, home)
			}
			return runSessionUI(ctx, runner, values.session, workspace.Root, cwd, fragments, config, creds, questions, forward)
		}
		return interactivePlainLoop(ctx, runner, values.session, workspace.Root, fragments, questions, config, renderer, creds)
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
	return agentruntime.TurnRequest{SessionID: session, Input: model.TextMessage(model.RoleUser, input), Prompt: fragments, Scope: tool.Scope{SessionID: session, Workspace: workspace}, Steering: steering}
}

func interactivePlainLoop(ctx context.Context, runner *agentruntime.Runtime, session, workspace string, fragments []prompt.Fragment, questions <-chan approvalQuestion, config local.Config, renderer *eventRenderer, creds credentials) error {
	reader := bufio.NewReader(os.Stdin)
	var queued []string
	for {
		var input string
		if len(queued) > 0 {
			input, queued = queued[0], queued[1:]
		} else {
			fmt.Fprint(os.Stderr, "> ")
			line, err := reader.ReadString('\n')
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			}
			input = strings.TrimSpace(line)
		}
		if strings.TrimSpace(input) == "" {
			continue
		}
		if input == "/exit" || input == "/quit" {
			return nil
		}
		if handled := renderer.command(input, session, workspace, config, creds); handled {
			continue
		}
		steering := &agentruntime.InputBuffer{}
		done := make(chan turnDone, 1)
		go func(value string) {
			result, err := runner.RunTurn(ctx, turnRequest(session, workspace, value, fragments, steering))
			done <- turnDone{result, err}
		}(input)
		renderer.resetTurn()
		renderer.startWait()
		for {
			select {
			case <-ctx.Done():
				renderer.stopWait()
				return nil
			case question := <-questions:
				renderer.stopWait()
				fmt.Fprintf(os.Stderr, "\n  %s %s\n  %s\n  %s ", renderer.paint("!", colorError), question.request.Call.Name, renderer.paint(question.decision.Reason, colorDim), renderer.paint("[o]nce [s]ession [p]roject [n]o", colorDim))
				answer, _ := reader.ReadString('\n')
				question.answer <- parseApproval(answer)
			case outcome := <-done:
				renderer.stopWait()
				for _, pending := range steering.Drain() {
					if text := strings.TrimSpace(pending.Text()); text != "" {
						queued = append(queued, text)
					}
				}
				if outcome.err != nil {
					fmt.Fprintln(os.Stderr, renderer.paint("  "+outcome.err.Error(), colorError))
				}
				fmt.Fprint(os.Stderr, "\n\n")
				goto nextTurn
			}
		}
	nextTurn:
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
	streamed       bool
	toolActivity   bool
	waiting        *waitSpinner
}

func (r *eventRenderer) resetTurn() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streamed = false
	r.toolActivity = false
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
		r.stopWaitLocked()
		if !r.streamed {
			gap := "\n"
			if !r.toolActivity {
				gap = "\n\n"
			}
			fmt.Fprint(r.stdout, gap+r.paint("⎿", colorDim)+" ")
			r.streamed = true
		}
		fmt.Fprint(r.stdout, strings.ReplaceAll(event.Model.Text, "\n", assistantWrapIndent))
	}
	if event.Type == transcript.ToolCallStarted && event.ToolCall != nil {
		r.stopWaitLocked()
		r.toolActivity = true
		if r.streamed {
			fmt.Fprint(r.stderr, "\n")
			r.streamed = false
		}
		r.started[event.ToolCall.ID] = time.Now()
		summary := toolArgSummary(event.ToolCall.Arguments)
		line := r.paint("⏺", colorClaude) + " " + r.paint(toolDisplayName(event.ToolCall.Name), colorBold)
		if summary != "" {
			line += r.paint("("+summary+")", colorDim)
		}
		fmt.Fprintf(r.stderr, "  %s\n", line)
	}
	if (event.Type == transcript.ToolCallCompleted || event.Type == transcript.ToolCallFailed) && event.ToolCall != nil {
		r.stopWaitLocked()
		elapsed := time.Since(r.started[event.ToolCall.ID]).Round(10 * time.Millisecond)
		detail := toolResultSummary(event.ToolResult)
		if event.Type == transcript.ToolCallFailed {
			detail = "failed · " + elapsed.String()
			if event.ToolResult != nil {
				if msg := clipWidth(firstLine(event.ToolResult.Text()), 60); msg != "" {
					detail = msg + " · " + elapsed.String()
				}
			}
			fmt.Fprintf(r.stderr, "    %s %s\n", r.paint("✗", colorError), r.paint(detail, colorError))
		} else {
			if detail == "" {
				detail = elapsed.String()
			} else {
				detail = detail + " · " + elapsed.String()
			}
			fmt.Fprintf(r.stderr, "    %s %s\n", r.paint("⎿", colorDim), r.paint(detail, colorDim))
		}
		delete(r.started, event.ToolCall.ID)
		r.startWaitLocked()
	}
	if event.Type == transcript.TurnCompleted || event.Type == transcript.TurnFailed || event.Type == transcript.TurnCanceled {
		r.stopWaitLocked()
		r.streamed = false
		r.toolActivity = false
	}
}
func (r *eventRenderer) finish(result agentruntime.TurnResult) {
	if r.mode == "text" && result.Message.Text() != "" {
		fmt.Fprintln(r.stdout)
	}
}

func (r *eventRenderer) paint(value, code string) string {
	return paintANSI(r.color, value, code)
}

func (r *eventRenderer) welcome(_ string, workspace string, config local.Config, creds credentials) {
	user := creds.UserID
	if user == "" {
		user = "session"
	}
	fmt.Fprintln(r.stderr)
	writeKeplerMark(r.stderr, r.paint)
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(workspace, home) {
		workspace = "~" + strings.TrimPrefix(workspace, home)
	}
	fmt.Fprintf(r.stderr, "\n  %s\n  %s\n\n", r.paint(workspace, colorDim), r.paint(config.Model+" · "+user, colorDim))
}

func (r *eventRenderer) command(input, session, workspace string, config local.Config, creds credentials) bool {
	switch strings.TrimSpace(input) {
	case "/help":
		fmt.Fprint(r.stderr, "\n  /help   commands\n  /status session\n  /exit   quit\n\n")
	case "/status":
		fmt.Fprintf(r.stderr, "\n  model     %s\n  protocol  %s/%s\n  gateway   %s\n  slack     %s\n  workspace %s\n  session   %s\n\n", config.Model, config.Provider, config.Protocol, creds.APIURL, creds.UserID, workspace, session)
	case "/clear":
		if r.color {
			fmt.Fprint(r.stderr, "\x1b[2J\x1b[H")
		}
	default:
		return false
	}
	return true
}

func toolDisplayName(name string) string {
	switch name {
	case "agent-explore":
		return "Explore"
	case "read_file":
		return "Read"
	case "write_file":
		return "Write"
	case "edit_file":
		return "Edit"
	case "list_files":
		return "List"
	case "skill_load":
		return "Skill"
	case "bash", "exec":
		return "Bash"
	case "grep":
		return "Grep"
	case "glob":
		return "Glob"
	}
	return name
}

func toolArgSummary(raw json.RawMessage) string {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return clipWidth(strings.Join(strings.Fields(string(raw)), " "), 56)
	}
	if tasks, ok := obj["tasks"].([]any); ok && len(tasks) > 0 {
		if first, ok := tasks[0].(map[string]any); ok {
			if task, _ := first["task"].(string); strings.TrimSpace(task) != "" {
				n := len(tasks)
				if n > 1 {
					return clipWidth(task, 44) + fmt.Sprintf(" · %d tasks", n)
				}
				return clipWidth(task, 56)
			}
		}
	}
	for _, key := range []string{"task", "command", "path", "file_path", "query", "glob", "pattern", "url", "name", "boundaries"} {
		if value, _ := obj[key].(string); strings.TrimSpace(value) != "" {
			return clipWidth(value, 56)
		}
	}
	if paths, ok := obj["paths"].([]any); ok && len(paths) > 0 {
		if path, _ := paths[0].(string); path != "" {
			if len(paths) > 1 {
				return clipWidth(path, 40) + fmt.Sprintf(" +%d", len(paths)-1)
			}
			return clipWidth(path, 56)
		}
	}
	return ""
}

func toolResultSummary(result *tool.Result) string {
	if result == nil {
		return ""
	}
	return clipWidth(firstLine(result.Text()), 56)
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if i := strings.IndexAny(value, "\n\r"); i >= 0 {
		return strings.TrimSpace(value[:i])
	}
	return value
}

func clipWidth(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if displayWidth(s) <= limit {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > limit-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}
