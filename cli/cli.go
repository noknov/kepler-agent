package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/noknov/slack-copilot-agent/benchmarks/benchcli"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	appruntime "github.com/noknov/slack-copilot-agent/packages/runtime"
)

func Run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printUsage()
		return nil
	}
	switch args[0] {
	case "config":
		if len(args) >= 2 && args[1] == "doctor" {
			return configDoctor()
		}
	case "tools":
		if len(args) >= 2 && args[1] == "list" {
			return listTools(ctx)
		}
	case "bench":
		return benchcli.Run(ctx, args[1:])
	}
	return fmt.Errorf("unknown command %q", strings.Join(args, " "))
}

func printUsage() {
	fmt.Println(`slack-copilot - local Slack Copilot CLI

Usage:
  slack-copilot config doctor
	  slack-copilot tools list
	  slack-copilot bench ...

Benchmark commands also have a standalone entrypoint:
  go run ./benchmarks/cmd/slack-copilot-bench help`)
}

func configDoctor() error {
	fmt.Println("mode: local")
	fmt.Println("server_config_required: false")
	return nil
}

func listTools(ctx context.Context) error {
	_ = ctx
	recorder := observability.NewRecorder()
	cfg, err := config.LoadCLI()
	if err != nil {
		cfg = appruntime.LocalCLIConfig()
	}
	runtime := appruntime.NewAgentRuntime(cfg, nil, nil, recorder, nil, nil)
	specs := runtime.Tools.Specs()
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Function.Name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Println(name)
	}
	return nil
}
