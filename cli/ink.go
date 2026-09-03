package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/noknov/kepler-agent/packages/cloud"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

type inkLaunchConfig struct {
	workspace string
	session   string
	resume    bool
	routing   string
	model     string
	bootstrap cloud.Bootstrap
	creds     credentials
}

// runInteractiveInk launches the Ink frontend backed by app-server. This is the
// default interactive path for kepler-agent.
func runInteractiveInk(values options, config local.Config, creds credentials, bootstrap cloud.Bootstrap) error {
	workspace, err := filepath.Abs(values.cwd)
	if err != nil {
		return err
	}
	stateDir := values.stateDir
	if stateDir == "" {
		stateDir, err = local.DefaultStateDir()
		if err != nil {
			return err
		}
	}
	session := values.session
	resume := values.resume
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
	routing := config.InputRouting
	if routing == "" {
		routing = "steer"
	}
	model := config.Model
	if model == "" {
		model = bootstrap.Model
	}
	return launchInk(inkLaunchConfig{
		workspace: workspace,
		session:   session,
		resume:    resume,
		routing:   routing,
		model:     model,
		bootstrap: bootstrap,
		creds:     creds,
	})
}

func launchInk(cfg inkLaunchConfig) error {
	command, commandArgs, err := resolveInkCommand()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(context.Background(), command, commandArgs...)
	cmd.Dir = cfg.workspace
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	env := append(os.Environ(),
		"KEPLER_TOKEN="+cfg.creds.Token,
		"KEPLER_API_URL="+cfg.creds.APIURL,
		"KEPLER_CWD="+cfg.workspace,
		"KEPLER_APP_SERVER="+resolveAppServerBinary(),
		"KEPLER_MODEL="+cfg.model,
		"KEPLER_INPUT_ROUTING="+cfg.routing,
	)
	if raw, err := json.Marshal(cfg.bootstrap); err == nil {
		env = append(env, "KEPLER_BOOTSTRAP="+string(raw))
	}
	cmd.Env = env
	if cfg.session != "" {
		cmd.Env = append(cmd.Env, "KEPLER_SESSION="+cfg.session)
	}
	if cfg.resume {
		cmd.Env = append(cmd.Env, "KEPLER_RESUME=1")
	}
	if cfg.creds.UserID != "" {
		cmd.Env = append(cmd.Env, "KEPLER_USER_ID="+cfg.creds.UserID)
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

func resolveInkCommand() (string, []string, error) {
	if entry := os.Getenv("KEPLER_UI_ENTRY"); entry != "" {
		return "node", []string{entry}, nil
	}
	if execPath, err := os.Executable(); err == nil {
		packaged := filepath.Join(filepath.Dir(execPath), "ui", "main.js")
		if _, err := os.Stat(packaged); err == nil {
			return "node", []string{packaged}, nil
		}
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
	return "", nil, fmt.Errorf("interactive UI not found; build apps/cli (pnpm install && pnpm build) or set KEPLER_UI_ENTRY")
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
