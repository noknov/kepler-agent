package k8s

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/safety"
)

type Base struct {
	KubectlPath    string
	DefaultContext string
	DefaultCluster string
	Guard          safety.CommandPolicy
	Timeout        time.Duration
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

func (b Base) run(ctx context.Context, args []string) (string, error) {
	if b.DefaultContext != "" {
		args = append([]string{"--context", b.DefaultContext}, args...)
	}
	display := b.kubectl() + " " + strings.Join(args, " ")
	if err := b.Guard.Check(display); err != nil {
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
