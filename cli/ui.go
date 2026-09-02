package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/noknov/kepler-agent/packages/profiles/local"
)

// runUI launches the Ink-based frontend. It does not replace the default
// interactive Bubble Tea path; use `kepler-agent ui` explicitly.
func runUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cwd, apiURL, session, routing string
	var resume bool
	fs.StringVar(&cwd, "cwd", ".", "workspace root")
	fs.StringVar(&apiURL, "api-url", "", "Kepler gateway URL (overrides credentials)")
	fs.StringVar(&session, "session", "", "session ID to create or resume")
	fs.BoolVar(&resume, "resume", false, "resume the most recently modified session")
	fs.StringVar(&routing, "input-routing", "steer", "steer or queue while a turn is active")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if routing != "steer" && routing != "queue" {
		return fmt.Errorf("invalid --input-routing value %q", routing)
	}
	creds, err := resolveCredentials(apiURL)
	if err != nil {
		return err
	}
	if creds.Token == "" {
		return errors.New("not logged in; run kepler-agent login")
	}
	bootstrap, err := fetchBootstrap(context.Background(), creds)
	if err != nil {
		return err
	}
	workspace, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}
	stateDir, err := local.DefaultStateDir()
	if err != nil {
		return err
	}
	if resume {
		store, storeErr := local.NewJSONLStore(filepath.Join(stateDir, "sessions"))
		if storeErr != nil {
			return storeErr
		}
		sessions, listErr := store.ListSessions()
		if listErr != nil {
			return listErr
		}
		if len(sessions) == 0 {
			return errors.New("no prior session to resume")
		}
		session = sessions[0].ID
	}
	command, commandArgs, err := resolveUICommand()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(context.Background(), command, commandArgs...)
	cmd.Dir = workspace
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"KEPLER_TOKEN="+creds.Token,
		"KEPLER_API_URL="+creds.APIURL,
		"KEPLER_CWD="+workspace,
		"KEPLER_APP_SERVER="+resolveAppServerBinary(),
		"KEPLER_MODEL="+bootstrap.Model,
		"KEPLER_INPUT_ROUTING="+routing,
	)
	if session != "" {
		cmd.Env = append(cmd.Env, "KEPLER_SESSION="+session)
	}
	if resume {
		cmd.Env = append(cmd.Env, "KEPLER_RESUME=1")
	}
	if creds.UserID != "" {
		cmd.Env = append(cmd.Env, "KEPLER_USER_ID="+creds.UserID)
	}
	return cmd.Run()
}

func resolveAppServerBinary() string {
	if path := os.Getenv("KEPLER_APP_SERVER"); path != "" {
		return path
	}
	if execPath, err := os.Executable(); err == nil {
		dir := filepath.Dir(execPath)
		for _, name := range []string{"kepler-agent-app-server", "app-server"} {
			candidate := filepath.Join(dir, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return "app-server"
}

func resolveUICommand() (string, []string, error) {
	if entry := os.Getenv("KEPLER_UI_ENTRY"); entry != "" {
		return "node", []string{entry}, nil
	}
	if roots, err := uiSearchRoots(); err == nil {
		for _, root := range roots {
			dist := filepath.Join(root, "apps/cli/dist/main.js")
			if _, err := os.Stat(dist); err == nil {
				return "node", []string{dist}, nil
			}
			src := filepath.Join(root, "apps/cli/src/main.tsx")
			if _, err := os.Stat(src); err == nil {
				if tsx, err := lookPath("tsx"); err == nil {
					return tsx, []string{src}, nil
				}
				return "npx", []string{"--yes", "tsx", src}, nil
			}
		}
	}
	return "", nil, fmt.Errorf("kepler UI not found; build apps/cli or set KEPLER_UI_ENTRY")
}

func uiSearchRoots() ([]string, error) {
	var roots []string
	if source := os.Getenv("KEPLER_SOURCE_DIR"); source != "" {
		roots = append(roots, source)
	}
	if cwd, err := os.Getwd(); err == nil {
		roots = append(roots, cwd)
	}
	if execPath, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Dir(execPath))
	}
	seen := make(map[string]bool)
	var unique []string
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		for dir := abs; !seen[dir]; dir = filepath.Dir(dir) {
			seen[dir] = true
			unique = append(unique, dir)
			if filepath.Base(dir) == "kepler-agent" {
				break
			}
			if dir == filepath.Dir(dir) {
				break
			}
		}
	}
	if len(unique) == 0 {
		return nil, errors.New("no search roots")
	}
	return unique, nil
}

func lookPath(name string) (string, error) {
	return exec.LookPath(name)
}
