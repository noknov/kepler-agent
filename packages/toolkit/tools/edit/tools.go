package edit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

type WriteFileTool struct {
	Paths safety.WorkspacePolicy
}

func (WriteFileTool) IsWrite() bool { return true }

func (t WriteFileTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"code-write_file",
		"Create or replace a workspace file with exact content. Use this only in isolated coding workspaces, after reading the relevant file when replacing existing code.",
		registry.ObjectSchema([]string{"path", "content"}, map[string]any{
			"path":    map[string]any{"type": "string", "description": "Workspace-relative, root-prefixed, or absolute file path."},
			"content": map[string]any{"type": "string", "description": "The complete file content to write."},
		}),
	)
}

func (t WriteFileTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	path, err := resolveWritablePath(t.Paths, args.Path)
	if err != nil {
		return registry.Result{}, err
	}
	select {
	case <-ctx.Done():
		return registry.Result{}, ctx.Err()
	default:
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return registry.Result{}, err
	}
	if err := os.WriteFile(path, []byte(args.Content), 0o600); err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: fmt.Sprintf("wrote %s (%d bytes)", displayPath(t.Paths, path), len(args.Content))}, nil
}

type ReplaceTool struct {
	Paths safety.WorkspacePolicy
}

func (ReplaceTool) IsWrite() bool { return true }

func (t ReplaceTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"code-replace",
		"Replace one exact text span in a workspace file. Prefer this over code-write_file for small edits. The old_text must match exactly and must occur exactly once.",
		registry.ObjectSchema([]string{"path", "old_text", "new_text"}, map[string]any{
			"path":     map[string]any{"type": "string", "description": "Workspace-relative, root-prefixed, or absolute file path."},
			"old_text": map[string]any{"type": "string", "description": "Exact text to replace. It must occur exactly once."},
			"new_text": map[string]any{"type": "string", "description": "Replacement text."},
		}),
	)
}

func (t ReplaceTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if args.OldText == "" {
		return registry.Result{}, fmt.Errorf("old_text is required")
	}
	path, err := t.Paths.ResolveReadableFile(args.Path)
	if err != nil {
		return registry.Result{}, err
	}
	if safety.IsSensitivePath(path) {
		return registry.Result{}, fmt.Errorf("refusing to edit sensitive file %q", filepath.Base(path))
	}
	select {
	case <-ctx.Done():
		return registry.Result{}, ctx.Err()
	default:
	}
	contentBytes, err := os.ReadFile(path)
	if err != nil {
		return registry.Result{}, err
	}
	content := string(contentBytes)
	count := strings.Count(content, args.OldText)
	if count != 1 {
		return registry.Result{}, fmt.Errorf("old_text matched %d times; expected exactly 1", count)
	}
	updated := strings.Replace(content, args.OldText, args.NewText, 1)
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: fmt.Sprintf("replaced text in %s", displayPath(t.Paths, path))}, nil
}

func resolveWritablePath(policy safety.WorkspacePolicy, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("path is required")
	}
	clean := filepath.Clean(raw)
	var candidates []string
	if filepath.IsAbs(clean) {
		candidates = append(candidates, clean)
	} else {
		for _, root := range policy.Roots {
			root = filepath.Clean(root)
			base := filepath.Base(root)
			rel := clean
			if rel == base {
				rel = "."
			} else if strings.HasPrefix(rel, base+string(filepath.Separator)) {
				rel = strings.TrimPrefix(rel, base+string(filepath.Separator))
			}
			candidates = append(candidates, filepath.Join(root, rel))
		}
	}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if safety.IsSensitivePath(candidate) {
			return "", fmt.Errorf("refusing to edit sensitive file %q", filepath.Base(candidate))
		}
		if withinAnyRoot(policy.Roots, candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("path is outside allowed workspace roots")
}

func withinAnyRoot(roots []string, path string) bool {
	for _, root := range roots {
		root = filepath.Clean(root)
		if path == root {
			return true
		}
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func displayPath(policy safety.WorkspacePolicy, path string) string {
	for _, root := range policy.Roots {
		if rel, err := filepath.Rel(filepath.Clean(root), path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}
