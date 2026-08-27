package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type CommandRequest struct {
	Argv        []string
	Workdir     string
	Network     bool
	Environment []string
}

type CommandResult struct {
	Output    string
	ExitCode  int
	Truncated bool
}

type Sandbox struct {
	Workspace            Workspace
	AdditionalReadRoots  []string
	UnsafeAllowNoSandbox bool
}

func (s Sandbox) Run(ctx context.Context, request CommandRequest) (CommandResult, error) {
	if len(request.Argv) == 0 || strings.TrimSpace(request.Argv[0]) == "" {
		return CommandResult{}, fmt.Errorf("argv is required")
	}
	workdir, err := s.Workspace.Resolve(request.Workdir, false)
	if request.Workdir == "" {
		workdir, err = s.Workspace.Root, nil
	}
	if err != nil {
		return CommandResult{}, err
	}
	var command *exec.Cmd
	sensitive, err := s.Workspace.SensitivePaths()
	if err != nil {
		return CommandResult{}, fmt.Errorf("scan sensitive workspace paths: %w", err)
	}
	readRoots, err := canonicalReadRoots(s.AdditionalReadRoots)
	if err != nil {
		return CommandResult{}, err
	}
	switch runtime.GOOS {
	case "darwin":
		path := "/usr/bin/sandbox-exec"
		if _, statErr := os.Stat(path); statErr != nil {
			return s.unsandboxed(ctx, request, workdir, statErr)
		}
		profile := "(version 1)\n(allow default)\n(deny file-write*)\n"
		profile += "(allow file-write* (subpath " + strconv.Quote(s.Workspace.Root) + ") (subpath " + strconv.Quote(s.Workspace.Temp) + "))\n"
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			profile += "(deny file-read* (subpath " + strconv.Quote(home) + "))\n"
		}
		profile += "(allow file-read* (subpath " + strconv.Quote(s.Workspace.Root) + "))\n"
		for _, root := range readRoots {
			profile += "(allow file-read* (subpath " + strconv.Quote(root) + "))\n"
		}
		for _, path := range sensitive {
			profile += "(deny file-read* file-write* (literal " + strconv.Quote(path) + "))\n"
		}
		if !request.Network {
			profile += "(deny network*)\n"
		}
		command = exec.CommandContext(ctx, path, append([]string{"-p", profile, request.Argv[0]}, request.Argv[1:]...)...)
	case "linux":
		path, lookupErr := exec.LookPath("bwrap")
		if lookupErr != nil {
			return s.unsandboxed(ctx, request, workdir, lookupErr)
		}
		args := []string{"--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try", "--cap-drop", "ALL", "--ro-bind", "/", "/"}
		if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "/" {
			args = append(args, "--tmpfs", home)
			args = append(args, bindParentArgs(home, s.Workspace.Root)...)
		}
		args = append(args, "--tmpfs", "/tmp", "--bind", s.Workspace.Root, s.Workspace.Root, "--bind", s.Workspace.Temp, s.Workspace.Temp)
		for _, hidden := range sensitive {
			info, statErr := os.Stat(hidden)
			if statErr != nil {
				continue
			}
			if info.IsDir() {
				args = append(args, "--tmpfs", hidden)
			} else {
				args = append(args, "--ro-bind", "/dev/null", hidden)
			}
		}
		for _, root := range readRoots {
			args = append(args, "--ro-bind", root, root)
		}
		args = append(args, "--chdir", workdir, "--proc", "/proc", "--dev", "/dev")
		if !request.Network {
			args = append(args, "--unshare-net")
		}
		args = append(args, "--")
		args = append(args, request.Argv...)
		command = exec.CommandContext(ctx, path, args...)
	default:
		return CommandResult{}, fmt.Errorf("sandbox is not supported on %s", runtime.GOOS)
	}
	result, runErr := runCommand(command, workdir, request.Environment, s.Workspace.Temp)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, runErr
}

func (s Sandbox) unsandboxed(ctx context.Context, request CommandRequest, workdir string, cause error) (CommandResult, error) {
	if !s.UnsafeAllowNoSandbox {
		return CommandResult{}, fmt.Errorf("required sandbox is unavailable: %w", cause)
	}
	result, runErr := runCommand(exec.CommandContext(ctx, request.Argv[0], request.Argv[1:]...), workdir, request.Environment, s.Workspace.Temp)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, runErr
}

func runCommand(command *exec.Cmd, workdir string, extraEnvironment []string, isolatedHome string) (CommandResult, error) {
	command.Dir = workdir
	command.Env = safeEnvironment(isolatedHome, extraEnvironment)
	var output limitedBuffer
	output.limit = 8 << 20
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	result := CommandResult{Output: output.String(), Truncated: output.truncated}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, err
}

type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		b.truncated = true
	}
	_, err := b.Buffer.Write(value)
	return original, err
}

func safeEnvironment(isolatedHome string, extra []string) []string {
	allowed := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TERM": true, "TMPDIR": true, "SHELL": true}
	values := make([]string, 0, len(allowed)+len(extra)+1)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			values = append(values, entry)
		}
	}
	values = append(values, "HOME="+isolatedHome)
	for _, entry := range extra {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !allowed[key] || key == "PATH" || key == "TMPDIR" || key == "SHELL" {
			continue
		}
		values = append(values, entry)
	}
	return values
}

func canonicalReadRoots(values []string) ([]string, error) {
	home, _ := os.UserHomeDir()
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		abs, err := filepath.Abs(value)
		if err != nil {
			return nil, fmt.Errorf("resolve additional read root: %w", err)
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("resolve additional read root %q: %w", value, err)
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("additional read root %q must be an existing directory", value)
		}
		if real == string(filepath.Separator) || (home != "" && real == home) {
			return nil, fmt.Errorf("additional read root %q is too broad", value)
		}
		if !seen[real] {
			seen[real] = true
			out = append(out, real)
		}
	}
	return out, nil
}

func bindParentArgs(home, target string) []string {
	if !within(target, home) {
		return nil
	}
	parent := strings.TrimPrefix(target, home)
	parts := strings.Split(strings.Trim(parent, string(os.PathSeparator)), string(os.PathSeparator))
	current := home
	var args []string
	for _, part := range parts[:max(0, len(parts)-1)] {
		current += string(os.PathSeparator) + part
		args = append(args, "--dir", current)
	}
	return args
}
