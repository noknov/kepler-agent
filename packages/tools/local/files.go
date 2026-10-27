// Package localtools provides the local CLI tool profile.
package localtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
	"github.com/noknov/kepler-agent/packages/safety"
)

type ReadFile struct{ Workspace local.Workspace }

func (ReadFile) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "read_file", Description: "Read UTF-8 byte ranges from regular workspace files. Use paths for independent files; offset and limit apply per file. Default limit is 64 KiB, with a 16 MiB aggregate requested-byte budget per call.", InputSchema: schema(fmt.Sprintf(`{"path":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":%d}}`, maxFileReadBytes)), Effects: []tool.Effect{tool.EffectRead}, Exposure: tool.ExposureEager, Parallel: true}
}

func (t ReadFile) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Path   string   `json:"path"`
		Paths  []string `json:"paths"`
		Offset int      `json:"offset"`
		Limit  int      `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	files := tool.NormalizePaths(arguments.Path, arguments.Paths)
	if len(files) == 0 {
		return tool.Result{}, fmt.Errorf("path or paths is required")
	}
	limit, err := fileReadLimit(arguments.Limit)
	if err != nil {
		return tool.Result{}, err
	}
	if arguments.Offset < 0 {
		return tool.Result{}, fmt.Errorf("offset must not be negative")
	}
	if limit > maxFileReadBytes/len(files) {
		return tool.Result{}, fmt.Errorf("read batch exceeds %d byte requested budget; reduce paths or limit", maxFileReadBytes)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if len(files) == 1 {
		body, truncated, err := t.readOne(files[0], arguments.Offset, limit)
		if err != nil {
			return tool.Result{}, err
		}
		if err := ctx.Err(); err != nil {
			return tool.Result{}, err
		}
		result := tool.TextResult(body)
		result.Truncated = truncated
		return result, nil
	}
	truncated := make([]bool, len(files))
	text, err := tool.MapOrdered(len(files), func(i int) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		body, cut, readErr := t.readOne(files[i], arguments.Offset, limit)
		truncated[i] = cut
		if readErr != nil {
			return "", readErr
		}
		return "## " + files[i] + "\n" + body, nil
	})
	if err != nil {
		return tool.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	result := tool.TextResult(text)
	result.Truncated = slices.Contains(truncated, true)
	return result, nil
}

func (t ReadFile) readOne(rawPath string, offset, limit int) (string, bool, error) {
	limit, err := fileReadLimit(limit)
	if err != nil {
		return "", false, err
	}
	file, err := t.Workspace.OpenReadFile(rawPath)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	if offset < 0 || int64(offset) > info.Size() {
		return "", false, fmt.Errorf("offset is outside file")
	}
	if _, err := file.Seek(int64(offset), io.SeekStart); err != nil {
		return "", false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return "", false, err
	}
	// Lookahead may end inside a UTF-8 rune. Trim only an incomplete rune at
	// the requested boundary; malformed bytes elsewhere must still fail.
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
		start := len(data) - 1
		for start >= 0 && !utf8.RuneStart(data[start]) {
			start--
		}
		if start >= 0 && !utf8.FullRune(data[start:]) {
			data = data[:start]
		}
	}
	if !utf8.Valid(data) {
		return "", false, fmt.Errorf("file is not valid UTF-8")
	}
	return string(data), truncated, nil
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
	command := exec.CommandContext(ctx, "rg", append([]string{"--no-config", "--files", "--hidden"}, safeRGGlobs()...)...)
	command.Dir = root
	var output boundedBuffer
	output.limit = 4 << 20
	command.Stdout = &output
	err := command.Run()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return tool.Result{}, err
		}
	}
	if arguments.Limit <= 0 {
		arguments.Limit = 500
	}
	return rgResult(&output, arguments.Limit), nil
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
	target := "."
	if arguments.Path != "" {
		var err error
		root, err = t.Workspace.Resolve(arguments.Path, false)
		if err != nil {
			return tool.Result{}, err
		}
		if info, statErr := os.Stat(root); statErr == nil && !info.IsDir() {
			target = filepath.Base(root)
			root = filepath.Dir(root)
		}
	}
	if arguments.Limit <= 0 {
		arguments.Limit = 200
	}
	// One extra match proves that a per-file cap cannot silently hide results
	// within the caller's requested line budget. Convert before adding to avoid
	// overflowing int for the largest accepted JSON integer.
	maxCount := strconv.FormatUint(uint64(arguments.Limit)+1, 10)
	args := append([]string{"--no-config", "-n", "--hidden", "--max-count=" + maxCount}, safeRGGlobs()...)
	args = append(args, "--", arguments.Query, target)
	command := exec.CommandContext(ctx, "rg", args...)
	command.Dir = root
	var output boundedBuffer
	output.limit = 4 << 20
	command.Stdout = &output
	err := command.Run()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return tool.Result{}, err
		}
	}
	return rgResult(&output, arguments.Limit), nil
}

type WriteFile struct{ Workspace local.Workspace }

func (WriteFile) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "write_file", Description: "Write a complete file inside the workspace.", InputSchema: schema(`{"path":{"type":"string"},"content":{"type":"string"}}`, "path", "content"), Effects: []tool.Effect{tool.EffectWorkspaceWrite}, Exposure: tool.ExposureEager}
}

func (t WriteFile) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	file, err := t.Workspace.OpenWritableFile(arguments.Path)
	if err != nil {
		return tool.Result{}, err
	}
	defer file.Close()
	if err := file.Write(ctx, []byte(arguments.Content), 0o644); err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(fmt.Sprintf("Wrote %d bytes to %s.", len(arguments.Content), relative(t.Workspace.Root, file.Path()))), nil
}

type EditFile struct{ Workspace local.Workspace }

func (EditFile) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "edit_file", Description: "Replace exact text in one workspace file.", InputSchema: schema(`{"path":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"},"replace_all":{"type":"boolean"}}`, "path", "old_text", "new_text"), Effects: []tool.Effect{tool.EffectWorkspaceWrite}, Exposure: tool.ExposureEager}
}

func (t EditFile) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
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
	file, err := t.Workspace.OpenWritableFile(arguments.Path)
	if err != nil {
		return tool.Result{}, err
	}
	defer file.Close()
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	data, err := file.Read()
	if err != nil {
		return tool.Result{}, err
	}
	content := string(data)
	count := strings.Count(content, arguments.Old)
	if count == 0 {
		return tool.Result{}, fmt.Errorf("old_text was not found; reread the file and provide exact text")
	}
	if !arguments.All && count != 1 {
		return tool.Result{}, fmt.Errorf("old_text matched %d times; provide more context or set replace_all", count)
	}
	limit := 1
	if arguments.All {
		limit = -1
	}
	updated := strings.Replace(content, arguments.Old, arguments.New, limit)

	if err := file.Write(ctx, []byte(updated), 0o644); err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(fmt.Sprintf("Updated %s (%d replacement(s)).", relative(t.Workspace.Root, file.Path()), count)), nil
}

const maxFileReadBytes = 16 << 20

func fileReadLimit(limit int) (int, error) {
	if limit < 0 || limit > maxFileReadBytes {
		return 0, fmt.Errorf("limit must be between 1 and %d bytes, or omitted", maxFileReadBytes)
	}
	if limit == 0 {
		return 64 << 10, nil
	}
	return limit, nil
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		b.truncated = true
	}
	_, err := b.buffer.Write(value)
	return original, err
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
	patterns := append([]string{"!**/.git", "!**/.git/**"}, safety.SensitiveRGGlobs()...)
	args := make([]string, 0, len(patterns)*2)
	for _, pattern := range patterns {
		args = append(args, "--iglob", pattern)
	}
	return args
}

func rgResult(output *boundedBuffer, limit int) tool.Result {
	lines := strings.Split(strings.TrimSpace(output.buffer.String()), "\n")
	truncated := output.truncated || len(lines) > limit
	if len(lines) > limit {
		lines = lines[:limit]
	}
	result := tool.TextResult(strings.Join(lines, "\n"))
	result.Truncated = truncated
	return result
}
