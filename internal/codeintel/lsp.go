package codeintel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wati/oncall-agent/internal/safety"
)

type Manager struct {
	Paths   safety.WorkspacePolicy
	Timeout time.Duration
}

type Position struct {
	Repo      string
	Path      string
	Line      int
	Character int
}

type Symbol struct {
	Name      string `json:"name"`
	Kind      int    `json:"kind"`
	KindName  string `json:"kind_name"`
	Container string `json:"container,omitempty"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

type Location struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

type Diagnostic struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
	Severity  int    `json:"severity,omitempty"`
	Message   string `json:"message"`
	Source    string `json:"source,omitempty"`
}

func (m Manager) Symbols(ctx context.Context, repoPath, query string, limit int) ([]Symbol, error) {
	repo, server, err := m.repoAndServer(repoPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if server.Language == "go" {
		return m.goSymbols(ctx, repo, query, limit, server)
	}
	client, err := startClient(ctx, server, serverRoot(repo, server), m.timeout())
	if err != nil {
		return nil, err
	}
	defer client.Close()
	var raw []workspaceSymbol
	if err := client.Request(ctx, "workspace/symbol", map[string]any{"query": query}, &raw); err != nil {
		return nil, err
	}
	out := make([]Symbol, 0, len(raw))
	for _, sym := range raw {
		loc := sym.Location
		if loc.URI == "" {
			loc = sym.Data.Location
		}
		path, line, char := locationParts(repo, loc)
		out = append(out, Symbol{
			Name:      sym.Name,
			Kind:      sym.Kind,
			KindName:  symbolKind(sym.Kind),
			Container: sym.ContainerName,
			Path:      path,
			Line:      line,
			Character: char,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m Manager) Definition(ctx context.Context, pos Position) ([]Location, error) {
	return m.locationRequest(ctx, pos, "textDocument/definition")
}

func (m Manager) References(ctx context.Context, pos Position) ([]Location, error) {
	return m.locationRequest(ctx, pos, "textDocument/references")
}

func (m Manager) Diagnostics(ctx context.Context, repoPath, path string) ([]Diagnostic, error) {
	repo, server, err := m.repoAndServer(repoPath)
	if err != nil {
		return nil, err
	}
	file, err := m.resolveFile(repo, path)
	if err != nil {
		return nil, err
	}
	if server.Language == "go" {
		return m.goDiagnostics(ctx, repo, file, server)
	}
	client, err := startClient(ctx, server, serverRoot(repo, server), m.timeout())
	if err != nil {
		return nil, err
	}
	defer client.Close()
	uri := fileURI(file)
	if err := client.DidOpen(ctx, uri, languageID(file), file); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(1200 * time.Millisecond):
	}
	client.mu.Lock()
	raw := append([]diagnostic(nil), client.diagnostics[uri]...)
	client.mu.Unlock()
	out := make([]Diagnostic, 0, len(raw))
	rel := relPath(repo, file)
	for _, d := range raw {
		out = append(out, Diagnostic{
			Path:      rel,
			Line:      d.Range.Start.Line + 1,
			Character: d.Range.Start.Character + 1,
			Severity:  d.Severity,
			Message:   strings.TrimSpace(d.Message),
			Source:    d.Source,
		})
	}
	return out, nil
}

func (m Manager) locationRequest(ctx context.Context, pos Position, method string) ([]Location, error) {
	repo, server, err := m.repoAndServer(pos.Repo)
	if err != nil {
		return nil, err
	}
	file, err := m.resolveFile(repo, pos.Path)
	if err != nil {
		return nil, err
	}
	if pos.Line <= 0 || pos.Character <= 0 {
		return nil, fmt.Errorf("line and character are required and 1-based")
	}
	if server.Language == "go" {
		if method == "textDocument/definition" {
			return m.goLocations(ctx, repo, file, pos, server, "definition")
		}
		return m.goLocations(ctx, repo, file, pos, server, "references")
	}
	client, err := startClient(ctx, server, serverRoot(repo, server), m.timeout())
	if err != nil {
		return nil, err
	}
	defer client.Close()
	uri := fileURI(file)
	if err := client.DidOpen(ctx, uri, languageID(file), file); err != nil {
		return nil, err
	}
	params := map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": pos.Line - 1, "character": pos.Character - 1},
	}
	if method == "textDocument/references" {
		params["context"] = map[string]any{"includeDeclaration": true}
	}
	var raw json.RawMessage
	if err := client.Request(ctx, method, params, &raw); err != nil {
		return nil, err
	}
	return parseLocations(repo, raw)
}

func (m Manager) repoAndServer(repoPath string) (string, serverSpec, error) {
	repo, err := m.Paths.Resolve(repoPath)
	if err != nil {
		return "", serverSpec{}, err
	}
	info, err := os.Stat(repo)
	if err != nil {
		return "", serverSpec{}, err
	}
	if !info.IsDir() {
		repo = filepath.Dir(repo)
	}
	server, err := detectServer(repo)
	if err != nil {
		return "", serverSpec{}, err
	}
	return repo, server, nil
}

func (m Manager) resolveFile(repo, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	var full string
	if filepath.IsAbs(path) {
		full = filepath.Clean(path)
	} else {
		full = filepath.Join(repo, filepath.Clean(path))
	}
	if !isWithin(full, repo) {
		return "", fmt.Errorf("path is outside repo")
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory")
	}
	return full, nil
}

func (m Manager) timeout() time.Duration {
	if m.Timeout > 0 {
		return m.Timeout
	}
	return 15 * time.Second
}

type serverSpec struct {
	Language string
	Name     string
	Args     []string
	Root     string
}

func detectServer(repo string) (serverSpec, error) {
	if hasFile(repo, "go.mod") {
		if path, err := exec.LookPath("gopls"); err == nil {
			return serverSpec{Language: "go", Name: path, Args: []string{"serve"}}, nil
		}
		return serverSpec{}, fmt.Errorf("Go repository detected but gopls is not installed")
	}
	if root, ok := findCSharpRoot(repo); ok {
		if path, err := exec.LookPath("csharp-ls"); err == nil {
			return serverSpec{Language: "csharp", Name: path, Root: root}, nil
		}
		return serverSpec{}, fmt.Errorf("C# repository detected under %s but csharp-ls is not installed", relPath(repo, root))
	}
	return serverSpec{}, fmt.Errorf("unsupported repository for code intelligence; install gopls for Go or csharp-ls for C#")
}

func serverRoot(repo string, server serverSpec) string {
	if server.Root != "" {
		return server.Root
	}
	return repo
}

func hasFile(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func hasGlob(dir, pattern string) bool {
	matches, _ := filepath.Glob(filepath.Join(dir, pattern))
	return len(matches) > 0
}

func findCSharpRoot(repo string) (string, bool) {
	if hasGlob(repo, "*.sln") || hasGlob(repo, "*.csproj") || hasGlob(filepath.Join(repo, "*"), "*.csproj") {
		return repo, true
	}
	var firstProject string
	var firstSolution string
	_ = filepath.WalkDir(repo, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == repo {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "bin" || name == "obj" || name == ".vs" {
				return filepath.SkipDir
			}
			if depth(repo, path) > 3 {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".sln":
			firstSolution = filepath.Dir(path)
			return filepath.SkipAll
		case ".csproj":
			if firstProject == "" {
				firstProject = filepath.Dir(path)
			}
		}
		return nil
	})
	if firstSolution != "" {
		return firstSolution, true
	}
	if firstProject != "" {
		return firstProject, true
	}
	return "", false
}

func depth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(rel), "/"))
}

type lspClient struct {
	cmd         *exec.Cmd
	in          io.WriteCloser
	out         *bufio.Reader
	mu          sync.Mutex
	nextID      int
	pending     map[int]chan responseMessage
	diagnostics map[string][]diagnostic
}

func startClient(ctx context.Context, spec serverSpec, root string, timeout time.Duration) (*lspClient, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &lspClient{
		cmd:         cmd,
		in:          stdin,
		out:         bufio.NewReader(stdout),
		pending:     map[int]chan responseMessage{},
		diagnostics: map[string][]diagnostic{},
	}
	go c.readLoop()
	var result any
	err = c.Request(ctx, "initialize", map[string]any{
		"processId": nil,
		"rootUri":   fileURI(root),
		"capabilities": map[string]any{
			"textDocument": map[string]any{"synchronization": map[string]any{"didSave": true}},
			"workspace":    map[string]any{"symbol": map[string]any{}},
		},
	}, &result)
	if err != nil {
		_ = c.Close()
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil, err
	}
	_ = c.Notify(ctx, "initialized", map[string]any{})
	return c, nil
}

func (c *lspClient) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out any
	_ = c.Request(ctx, "shutdown", nil, &out)
	_ = c.Notify(ctx, "exit", nil)
	_ = c.in.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
	return nil
}

func (c *lspClient) Request(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan responseMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(jsonrpcMessage{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("lsp %s failed: %s", method, resp.Error.Message)
		}
		if out != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	}
}

func (c *lspClient) Notify(ctx context.Context, method string, params any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return c.write(jsonrpcMessage{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *lspClient) DidOpen(ctx context.Context, uri, lang, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return c.Notify(ctx, "textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": lang,
			"version":    1,
			"text":       string(data),
		},
	})
}

func (c *lspClient) write(msg jsonrpcMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		return err
	}
	_, err = c.in.Write(data)
	return err
}

func (c *lspClient) readLoop() {
	for {
		msg, err := readMessage(c.out)
		if err != nil {
			return
		}
		if msg.ID != nil {
			id := int(*msg.ID)
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		if msg.Method == "textDocument/publishDiagnostics" {
			var params struct {
				URI         string       `json:"uri"`
				Diagnostics []diagnostic `json:"diagnostics"`
			}
			if json.Unmarshal(msg.Params, &params) == nil {
				c.mu.Lock()
				c.diagnostics[params.URI] = params.Diagnostics
				c.mu.Unlock()
			}
		}
	}
}

type jsonrpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type responseMessage struct {
	ID     *int            `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func readMessage(r *bufio.Reader) (responseMessage, error) {
	length := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return responseMessage{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
		}
	}
	if length <= 0 {
		return responseMessage{}, fmt.Errorf("missing Content-Length")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return responseMessage{}, err
	}
	var msg responseMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return responseMessage{}, err
	}
	return msg, nil
}

type workspaceSymbol struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	ContainerName string   `json:"containerName"`
	Location      location `json:"location"`
	Data          struct {
		Location location `json:"location"`
	} `json:"data"`
}

type location struct {
	URI   string `json:"uri"`
	Range rng    `json:"range"`
}

type rng struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type diagnostic struct {
	Range    rng    `json:"range"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

func parseLocations(root string, raw json.RawMessage) ([]Location, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var many []location
	if json.Unmarshal(raw, &many) == nil {
		out := make([]Location, 0, len(many))
		for _, loc := range many {
			path, line, char := locationParts(root, loc)
			out = append(out, Location{Path: path, Line: line, Character: char})
		}
		return out, nil
	}
	var one location
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, err
	}
	path, line, char := locationParts(root, one)
	return []Location{{Path: path, Line: line, Character: char}}, nil
}

func locationParts(root string, loc location) (string, int, int) {
	path := uriPath(loc.URI)
	return relPath(root, path), loc.Range.Start.Line + 1, loc.Range.Start.Character + 1
}

func fileURI(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return u.String()
}

func uriPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return uri
	}
	return filepath.FromSlash(u.Path)
}

func relPath(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func languageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".cs":
		return "csharp"
	default:
		return "plaintext"
	}
}

func isWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func symbolKind(kind int) string {
	names := map[int]string{
		1: "File", 2: "Module", 3: "Namespace", 4: "Package", 5: "Class", 6: "Method", 7: "Property",
		8: "Field", 9: "Constructor", 10: "Enum", 11: "Interface", 12: "Function", 13: "Variable",
		14: "Constant", 15: "String", 16: "Number", 17: "Boolean", 18: "Array", 19: "Object",
		20: "Key", 21: "Null", 22: "EnumMember", 23: "Struct", 24: "Event", 25: "Operator", 26: "TypeParameter",
	}
	if name := names[kind]; name != "" {
		return name
	}
	return fmt.Sprintf("Kind%d", kind)
}

func (m Manager) goSymbols(ctx context.Context, repo, query string, limit int, server serverSpec) ([]Symbol, error) {
	out, err := runCommand(ctx, repo, server.Name, m.timeout(), "workspace_symbol", query)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	symbols := make([]Symbol, 0, len(lines))
	for _, line := range lines {
		sym, ok := parseGoSymbol(repo, line)
		if !ok {
			continue
		}
		symbols = append(symbols, sym)
		if len(symbols) >= limit {
			break
		}
	}
	return symbols, nil
}

func (m Manager) goLocations(ctx context.Context, repo, file string, pos Position, server serverSpec, command string) ([]Location, error) {
	rel := relPath(repo, file)
	out, err := runCommand(ctx, repo, server.Name, m.timeout(), command, fmt.Sprintf("%s:%d:%d", rel, pos.Line, pos.Character))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	locations := make([]Location, 0, len(lines))
	for _, line := range lines {
		loc, ok := parseGoLocation(repo, line)
		if ok {
			locations = append(locations, loc)
		}
	}
	return locations, nil
}

func (m Manager) goDiagnostics(ctx context.Context, repo, file string, server serverSpec) ([]Diagnostic, error) {
	rel := relPath(repo, file)
	out, err := runCommand(ctx, repo, server.Name, m.timeout(), "check", rel)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	diagnostics := make([]Diagnostic, 0, len(lines))
	for _, line := range lines {
		if d, ok := parseGoDiagnostic(repo, line); ok {
			diagnostics = append(diagnostics, d)
		}
	}
	return diagnostics, nil
}

func runCommand(ctx context.Context, dir, name string, timeout time.Duration, args ...string) (string, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if filepath.Base(name) == "gopls" {
		env, err := isolatedGoEnv(dir)
		if err != nil {
			return "", err
		}
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("%s failed: %s", filepath.Base(name), msg)
	}
	return stdout.String(), nil
}

func isolatedGoEnv(repo string) ([]string, error) {
	sum := sha256.Sum256([]byte(filepath.Clean(repo)))
	key := hex.EncodeToString(sum[:8])
	base := filepath.Join(os.TempDir(), "oncall-agent-codeintel", key)
	dirs := []string{
		filepath.Join(base, "home"),
		filepath.Join(base, "go-build"),
		filepath.Join(base, "gomodcache"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("prepare gopls cache: %w", err)
		}
	}
	return append(os.Environ(),
		"HOME="+filepath.Join(base, "home"),
		"GOCACHE="+filepath.Join(base, "go-build"),
		"GOMODCACHE="+filepath.Join(base, "gomodcache"),
		"GIT_TERMINAL_PROMPT=0",
	), nil
}

var (
	goSymbolRe     = regexp.MustCompile(`^(.*):(\d+):(\d+)(?:-\d+)?\s+(.+?)\s+(\w+)$`)
	goLocationRe   = regexp.MustCompile(`^(.*):(\d+):(\d+)(?:-\d+)?(?::|\s|$)`)
	goDiagnosticRe = regexp.MustCompile(`^(.*):(\d+):(\d+):\s+(.+)$`)
)

func parseGoSymbol(root, line string) (Symbol, bool) {
	matches := goSymbolRe.FindStringSubmatch(strings.TrimSpace(line))
	if len(matches) != 6 {
		return Symbol{}, false
	}
	lineNo, _ := strconv.Atoi(matches[2])
	char, _ := strconv.Atoi(matches[3])
	return Symbol{
		Name:      matches[4],
		KindName:  matches[5],
		Path:      relPath(root, matches[1]),
		Line:      lineNo,
		Character: char,
	}, true
}

func parseGoLocation(root, line string) (Location, bool) {
	matches := goLocationRe.FindStringSubmatch(strings.TrimSpace(line))
	if len(matches) != 4 {
		return Location{}, false
	}
	lineNo, _ := strconv.Atoi(matches[2])
	char, _ := strconv.Atoi(matches[3])
	return Location{Path: relPath(root, matches[1]), Line: lineNo, Character: char}, true
}

func parseGoDiagnostic(root, line string) (Diagnostic, bool) {
	matches := goDiagnosticRe.FindStringSubmatch(strings.TrimSpace(line))
	if len(matches) != 5 {
		return Diagnostic{}, false
	}
	lineNo, _ := strconv.Atoi(matches[2])
	char, _ := strconv.Atoi(matches[3])
	return Diagnostic{Path: relPath(root, matches[1]), Line: lineNo, Character: char, Message: matches[4], Source: "gopls"}, true
}
