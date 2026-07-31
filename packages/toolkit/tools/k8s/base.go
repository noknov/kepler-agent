package k8s

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/safety"
)

type Base struct {
	KubectlPath      string
	DefaultContext   string
	DefaultCluster   string
	DefaultNamespace string
	Guard            safety.CommandPolicy
	Timeout          time.Duration
}

func (b Base) kubectl() string {
	if b.KubectlPath != "" {
		return b.KubectlPath
	}
	return "kubectl"
}

func (b Base) timeout() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return 30 * time.Second
}

// namespace returns the effective namespace for a command. When the caller
// provides an explicit namespace it takes priority; otherwise the configured
// default is used. An empty string means "kubectl's current namespace".
func (b Base) namespace(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return b.DefaultNamespace
}

// appendNamespace appends -n <ns> to args when a namespace is determined.
func (b Base) appendNamespace(args []string, explicit string) []string {
	if ns := b.namespace(explicit); ns != "" {
		return append(args, "-n", ns)
	}
	return args
}

// run executes a kubectl command. k8sContext overrides DefaultContext when
// non-empty, allowing per-call cluster switching without modifying Base config.
func (b Base) run(ctx context.Context, k8sContext string, args []string) (string, error) {
	kctx := k8sContext
	if kctx == "" {
		kctx = b.DefaultContext
	}
	if kctx != "" {
		args = append([]string{"--context", kctx}, args...)
	}
	if err := b.Guard.CheckArgv(append([]string{b.kubectl()}, args...)); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, b.kubectl(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", fmt.Errorf("kubectl failed: %s", errMsg)
	}
	return stdout.String(), nil
}
