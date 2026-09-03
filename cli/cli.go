package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/noknov/kepler-agent/packages/profiles/local"
)

type options struct {
	configPath, cwd, stateDir, routing, output, session, approval, apiURL string
	resume, unsafe                                                        bool
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

	if isInteractiveSession() {
		output := config.Output
		if output == "" {
			output = "text"
		}
		if output != "text" {
			return fmt.Errorf("interactive mode does not support --output %q", output)
		}
		return runInteractiveInk(values, config, creds, bootstrap)
	}

	return runHeadless(values, config, creds)
}

func isInteractiveSession() bool {
	return len(flag.Args()) == 0 && isTerminal(os.Stdin)
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
