// Package localtools provides the local CLI tool profile.
package localtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

type ReadFile struct{ Workspace local.Workspace }

func (ReadFile) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "read_file", Description: "Read a UTF-8 file inside the workspace.", InputSchema: schema(`{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}}`, "path"), Effects: []tool.Effect{tool.EffectRead}, Exposure: tool.ExposureEager, Parallel: true}
}

func (t ReadFile) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	path, err := t.Workspace.Resolve(arguments.Path, false)
	if err != nil {
		return tool.Result{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return tool.Result{}, err
	}
	if !utf8.Valid(data) {
		return tool.Result{}, fmt.Errorf("file is not valid UTF-8")
	}
	if arguments.Offset < 0 || arguments.Offset > len(data) {
		return tool.Result{}, fmt.Errorf("offset is outside file")
	}
	data = data[arguments.Offset:]
	if !utf8.Valid(data) {
		return tool.Result{}, fmt.Errorf("offset must be on a UTF-8 boundary")
	}
	if arguments.Limit <= 0 {
		arguments.Limit = 64 << 10
	}
	truncated := len(data) > arguments.Limit
	if truncated {
		data = data[:arguments.Limit]
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
	}
	result := tool.TextResult(string(data))
	result.Truncated = truncated
	return result, nil
}

type ListFiles struct{ Workspace local.Workspace }

func (ListFiles) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "list_files", Description: "List workspace files with ripgrep.", InputSchema: schema(`{"path":{"type":"string"},"limit":{"type":"integer"}}`), Effects: []tool.Effect{tool.EffectRead}, Exposure: tool.ExposureEager, Parallel: true}
}

func (t ListFiles) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Path  string `json:"path"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	root := t.Workspace.Root
	if arguments.Path != "" {
		var err error
		root, err = t.Workspace.Resolve(arguments.Path, false)
		if err != nil {
			return tool.Result{}, err
		}
	}
	command := exec.CommandContext(ctx, "rg", append([]string{"--files", "--hidden"}, safeRGGlobs()...)...)
	command.Dir = root
	data, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return tool.Result{}, err
		}
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if arguments.Limit <= 0 {
		arguments.Limit = 500
	}
	if len(lines) > arguments.Limit {
		lines = lines[:arguments.Limit]
		lines = append(lines, "[file list truncated]")
	}
	result := tool.TextResult(strings.Join(lines, "\n"))
	result.Truncated = len(lines) > 0 && lines[len(lines)-1] == "[file list truncated]"
	return result, nil
}

type Search struct{ Workspace local.Workspace }

func (Search) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "search", Description: "Search workspace text with ripgrep.", InputSchema: schema(`{"query":{"type":"string"},"path":{"type":"string"},"limit":{"type":"integer"}}`, "query"), Effects: []tool.Effect{tool.EffectRead}, Exposure: tool.ExposureEager, Parallel: true}
}

func (t Search) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Query string `json:"query"`
		Path  string `json:"path"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	root := t.Workspace.Root
	if arguments.Path != "" {
		var err error
		root, err = t.Workspace.Resolve(arguments.Path, false)
		if err != nil {
			return tool.Result{}, err
		}
	}
	args := append([]string{"-n", "--hidden"}, safeRGGlobs()...)
	args = append(args, "--", arguments.Query, ".")
	command := exec.CommandContext(ctx, "rg", args...)
	command.Dir = root
	data, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return tool.Result{}, err
		}
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if arguments.Limit <= 0 {
		arguments.Limit = 200
	}
	if len(lines) > arguments.Limit {
		lines = lines[:arguments.Limit]
		lines = append(lines, "[search results truncated]")
	}
	result := tool.TextResult(strings.Join(lines, "\n"))
	result.Truncated = len(lines) > 0 && lines[len(lines)-1] == "[search results truncated]"
	return result, nil
}

type WriteFile struct{ Workspace local.Workspace }

func (WriteFile) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "write_file", Description: "Write a complete file inside the workspace.", InputSchema: schema(`{"path":{"type":"string"},"content":{"type":"string"}}`, "path", "content"), Effects: []tool.Effect{tool.EffectWorkspaceWrite}, Exposure: tool.ExposureEager}
}

func (t WriteFile) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	path, err := t.Workspace.Resolve(arguments.Path, true)
	if err != nil {
		return tool.Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return tool.Result{}, err
	}
	if err := atomicWrite(path, []byte(arguments.Content), 0o644); err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(fmt.Sprintf("Wrote %d bytes to %s.", len(arguments.Content), relative(t.Workspace.Root, path))), nil
}

type EditFile struct{ Workspace local.Workspace }

func (EditFile) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "edit_file", Description: "Replace exact text in one workspace file.", InputSchema: schema(`{"path":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"},"replace_all":{"type":"boolean"}}`, "path", "old_text", "new_text"), Effects: []tool.Effect{tool.EffectWorkspaceWrite}, Exposure: tool.ExposureEager}
}

func (t EditFile) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Path string `json:"path"`
		Old  string `json:"old_text"`
		New  string `json:"new_text"`
		All  bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	if arguments.Old == "" {
		return tool.Result{}, fmt.Errorf("old_text must not be empty")
	}
	path, err := t.Workspace.Resolve(arguments.Path, true)
	if err != nil {
		return tool.Result{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return tool.Result{}, err
	}
	count := strings.Count(string(data), arguments.Old)
	if count == 0 {
		return tool.Result{}, fmt.Errorf("old_text was not found")
	}
	if !arguments.All && count != 1 {
		return tool.Result{}, fmt.Errorf("old_text matched %d times; provide more context or set replace_all", count)
	}
	limit := 1
	if arguments.All {
		limit = -1
	}
	updated := strings.Replace(string(data), arguments.Old, arguments.New, limit)
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := atomicWrite(path, []byte(updated), mode); err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(fmt.Sprintf("Updated %s (%d replacement(s)).", relative(t.Workspace.Root, path), count)), nil
}

func schema(properties string, required ...string) json.RawMessage {
	value := `{"type":"object","additionalProperties":false,"properties":` + properties
	if len(required) > 0 {
		data, _ := json.Marshal(required)
		value += `,"required":` + string(data)
	}
	return json.RawMessage(value + `}`)
}

func relative(root, path string) string {
	value, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return value
}

func safeRGGlobs() []string {
	patterns := []string{"!.git/**", "!.env", "!.env.*", "!**/.env", "!**/.env.*", "!**/*.pem", "!**/*.key", "!**/*.p12", "!**/*.pfx", "!**/.aws/**", "!**/.gcloud/**", "!**/.kube/**", "!**/secrets/**", "!**/credentials/**"}
	args := make([]string, 0, len(patterns)*2)
	for _, pattern := range patterns {
		args = append(args, "-g", pattern)
	}
	return args
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".agent-write-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	cleanup := func() { _ = file.Close(); _ = os.Remove(temporary) }
	if err := file.Chmod(mode); err != nil {
		cleanup()
		return err
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
