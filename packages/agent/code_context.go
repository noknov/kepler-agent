package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

const (
	codeContextCacheKey = "code-investigation-context"
	maxCodeEvidence     = 80
	maxReadStateContent = 24_000
)

type codeInvestigationContext struct {
	Reads    []codeReadRecord     `json:"reads,omitempty"`
	Evidence []codeEvidenceRecord `json:"evidence,omitempty"`
	Explored bool                 `json:"explored,omitempty"`
}

type codeReadRecord struct {
	Path      string `json:"path"`
	Base      string `json:"base,omitempty"`
	Tool      string `json:"tool"`
	Source    string `json:"source,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Commit    string `json:"commit,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Hash      string `json:"hash,omitempty"`
	Content   string `json:"content,omitempty"`
}

type codeEvidenceRecord struct {
	Path string `json:"path"`
	Base string `json:"base,omitempty"`
	Line int    `json:"line,omitempty"`
	Tool string `json:"tool"`
	Kind string `json:"kind"`
}

func codeContext(rt registry.Runtime) *codeInvestigationContext {
	if rt.Cache == nil {
		return nil
	}
	if existing, ok := rt.Cache.Get(codeContextCacheKey); ok {
		if ctx, ok := existing.(*codeInvestigationContext); ok {
			return ctx
		}
	}
	ctx := &codeInvestigationContext{}
	rt.Cache.Set(codeContextCacheKey, ctx)
	return ctx
}

func recordCodeToolResult(rt registry.Runtime, toolName string, args json.RawMessage, output string) {
	ctx := codeContext(rt)
	if ctx == nil || strings.TrimSpace(output) == "" || strings.HasPrefix(strings.TrimSpace(output), "[tool error]") {
		return
	}
	if toolName == "explore-code" {
		ctx.Explored = true
	}
	for _, rec := range parseCodeReads(toolName, args, output) {
		ctx.Reads = upsertReadRecord(ctx.Reads, rec)
		ctx.Evidence = appendEvidence(ctx.Evidence, codeEvidenceRecord{Path: rec.Path, Base: rec.Base, Line: rec.StartLine, Tool: toolName, Kind: "read"})
	}
	for _, ev := range parseCodeEvidence(toolName, output) {
		ctx.Evidence = appendEvidence(ctx.Evidence, ev)
	}
}

func renderCodeContext(rt registry.Runtime) string {
	ctx := codeContext(rt)
	if ctx == nil || (len(ctx.Reads) == 0 && len(ctx.Evidence) == 0 && !ctx.Explored) {
		return ""
	}
	var b strings.Builder
	b.WriteString("Code investigation state for this run. Treat this as session-local state, not long-term memory.\n")
	if ctx.Explored {
		b.WriteString("- explore-code has already been used in this investigation.\n")
	}
	if len(ctx.Reads) > 0 {
		b.WriteString("\nRead files:\n")
		for _, rec := range ctx.Reads {
			fmt.Fprintf(&b, "- %s", rec.Path)
			if rec.StartLine > 0 || rec.EndLine > 0 {
				fmt.Fprintf(&b, " lines=%d-%d", rec.StartLine, rec.EndLine)
			}
			if rec.Ref != "" {
				fmt.Fprintf(&b, " ref=%s", rec.Ref)
			}
			if rec.Commit != "" {
				fmt.Fprintf(&b, " commit=%s", rec.Commit)
			}
			if rec.Hash != "" {
				fmt.Fprintf(&b, " hash=%s", rec.Hash)
			}
			b.WriteString("\n")
		}
	}
	if len(ctx.Evidence) > 0 {
		b.WriteString("\nEvidence ledger:\n")
		for _, ev := range ctx.Evidence {
			fmt.Fprintf(&b, "- %s", ev.Path)
			if ev.Line > 0 {
				fmt.Fprintf(&b, ":%d", ev.Line)
			}
			fmt.Fprintf(&b, " via %s (%s)\n", ev.Tool, ev.Kind)
		}
	}
	b.WriteString("\nRules: search/symbol/reference hits are hints. For final claims about code behavior, cite files that appear in Read files above or explicitly state uncertainty.")
	return b.String()
}

func codeContextSteeringMessage(rt registry.Runtime) []llm.Message {
	content := strings.TrimSpace(renderCodeContext(rt))
	if content == "" {
		return nil
	}
	return []llm.Message{{Role: "system", Content: content}}
}

func maybeAutoExplorePrompt(req Request, rt registry.Runtime, step int) []llm.Message {
	if step != 0 || !looksLikeComplexCodeQuestion(req.UserQuestion) {
		return nil
	}
	ctx := codeContext(rt)
	if ctx != nil && ctx.Explored {
		return nil
	}
	return []llm.Message{{
		Role:    "system",
		Content: "This looks like a complex code investigation. Prefer starting with explore-code to map entry points, call sites, implementations, and tests before synthesizing. Use direct code-read_file/code-search only if the question is narrow and the decisive files are already known.",
	}}
}

func looksLikeComplexCodeQuestion(q string) bool {
	q = strings.ToLower(q)
	codeTerms := []string{"代码", "实现", "函数", "class", "handler", "service", "repo", "branch", "bug", "trace", "call", "调用", "架构", "pipeline", "上下文", "memory"}
	hits := 0
	for _, term := range codeTerms {
		if strings.Contains(q, term) {
			hits++
		}
	}
	return hits >= 2 || (hits >= 1 && len([]rune(q)) > 80)
}

func finalHasUnevidencedCodeFiles(answer string, rt registry.Runtime) bool {
	referenced := extractReferencedFiles(answer)
	if len(referenced) == 0 {
		return false
	}
	ctx := codeContext(rt)
	if ctx == nil {
		return len(referenced) > 0
	}
	read := map[string]bool{}
	for _, rec := range ctx.Reads {
		read[rec.Base] = true
		read[baseName(rec.Path)] = true
	}
	for f := range referenced {
		if !read[f] {
			return true
		}
	}
	return false
}

func parseCodeReads(toolName string, args json.RawMessage, output string) []codeReadRecord {
	switch toolName {
	case "code-read_file", "repo-read_file", "git-read_file_ref":
	default:
		return nil
	}
	source, ref, commit := parseSourceHeader(output)
	lines := numberedLinePattern.FindAllStringSubmatch(output, -1)
	if len(lines) == 0 {
		return nil
	}
	start, _ := strconv.Atoi(lines[0][1])
	end, _ := strconv.Atoi(lines[len(lines)-1][1])
	path := inferReadPath(toolName, args, output)
	content := trimReadContent(output)
	hash := sha256.Sum256([]byte(content))
	return []codeReadRecord{{
		Path:      path,
		Base:      baseName(path),
		Tool:      toolName,
		Source:    source,
		Ref:       ref,
		Commit:    commit,
		StartLine: start,
		EndLine:   end,
		Hash:      fmt.Sprintf("%x", hash[:6]),
		Content:   content,
	}}
}

func parseCodeEvidence(toolName, output string) []codeEvidenceRecord {
	if !codeReadingTools[toolName] {
		return nil
	}
	var out []codeEvidenceRecord
	for _, m := range pathLinePattern.FindAllStringSubmatch(output, -1) {
		line, _ := strconv.Atoi(m[2])
		path := strings.TrimPrefix(m[1], "./")
		out = append(out, codeEvidenceRecord{Path: path, Base: baseName(path), Line: line, Tool: toolName, Kind: "hit"})
	}
	return out
}

func parseSourceHeader(output string) (source, ref, commit string) {
	first := strings.TrimSpace(strings.SplitN(output, "\n", 2)[0])
	if strings.HasPrefix(first, "[source:") && strings.HasSuffix(first, "]") {
		source = strings.TrimSuffix(strings.TrimPrefix(first, "[source:"), "]")
		for _, field := range strings.Fields(source) {
			if strings.HasPrefix(field, "ref=") {
				ref = strings.TrimPrefix(field, "ref=")
			}
			if strings.HasPrefix(field, "commit=") {
				commit = strings.TrimPrefix(field, "commit=")
			}
		}
	}
	return source, ref, commit
}

func inferReadPath(toolName string, args json.RawMessage, output string) string {
	var raw struct {
		Path string `json:"path"`
		Repo string `json:"repo"`
	}
	if len(args) > 0 && json.Unmarshal(args, &raw) == nil && strings.TrimSpace(raw.Path) != "" {
		path := strings.TrimSpace(raw.Path)
		if raw.Repo != "" && !strings.Contains(path, "/") {
			return strings.Trim(strings.TrimSpace(raw.Repo), "/") + "/" + path
		}
		return strings.Trim(path, "/")
	}
	for _, m := range pathLinePattern.FindAllStringSubmatch(output, -1) {
		return strings.TrimPrefix(m[1], "./")
	}
	// File reads do not always echo the path. Keep a stable placeholder so the
	// read is still visible in rehydration; explicit args are tracked separately
	// by the tool result ledger when the tool output includes path hits.
	return toolName + ":read"
}

func trimReadContent(output string) string {
	output = strings.TrimSpace(output)
	if len(output) <= maxReadStateContent {
		return output
	}
	head := maxReadStateContent / 2
	tail := maxReadStateContent - head
	return output[:head] + "\n...[read state content truncated]...\n" + output[len(output)-tail:]
}

func upsertReadRecord(records []codeReadRecord, rec codeReadRecord) []codeReadRecord {
	key := rec.Path + "\x00" + rec.Ref + "\x00" + strconv.Itoa(rec.StartLine) + "\x00" + strconv.Itoa(rec.EndLine)
	out := records[:0]
	replaced := false
	for _, existing := range records {
		existingKey := existing.Path + "\x00" + existing.Ref + "\x00" + strconv.Itoa(existing.StartLine) + "\x00" + strconv.Itoa(existing.EndLine)
		if existingKey == key {
			if !replaced {
				out = append(out, rec)
				replaced = true
			}
			continue
		}
		out = append(out, existing)
	}
	if !replaced {
		out = append(out, rec)
	}
	if len(out) > 40 {
		out = out[len(out)-40:]
	}
	return out
}

func appendEvidence(records []codeEvidenceRecord, rec codeEvidenceRecord) []codeEvidenceRecord {
	if rec.Path == "" {
		return records
	}
	rec.Base = baseName(rec.Path)
	for _, existing := range records {
		if existing.Path == rec.Path && existing.Line == rec.Line && existing.Tool == rec.Tool && existing.Kind == rec.Kind {
			return records
		}
	}
	records = append(records, rec)
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Path == records[j].Path {
			return records[i].Line < records[j].Line
		}
		return records[i].Path < records[j].Path
	})
	if len(records) > maxCodeEvidence {
		return records[len(records)-maxCodeEvidence:]
	}
	return records
}

func baseName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = strings.TrimSuffix(path, ":")
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

var (
	numberedLinePattern = regexp.MustCompile(`(?m)^\s*(\d+)\s{2}`)
	pathLinePattern     = regexp.MustCompile(`(?m)([\w./-]+\.(?:go|ts|tsx|js|jsx|cs|py|java|rb|rs|yml|yaml|json)):(\d+)(?::\d+)?`)
)

func codeContextJSON(rt registry.Runtime) string {
	ctx := codeContext(rt)
	if ctx == nil {
		return "{}"
	}
	data, _ := json.Marshal(ctx)
	return string(data)
}
