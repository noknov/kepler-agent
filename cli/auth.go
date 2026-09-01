package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/cloud"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

// DefaultAPIURL is the public gateway origin compiled in by kepler-agent-deploy.
// It is empty in source builds.
var DefaultAPIURL string

type credentials struct {
	Token  string `json:"token"`
	APIURL string `json:"api_url"`
	UserID string `json:"user_id,omitempty"`
}

func credentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", localConfigDir(), "credentials.json"), nil
}

func localConfigDir() string { return "kepler-agent" }

func loadCredentials() (credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return credentials{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return credentials{}, nil
	}
	if err != nil {
		return credentials{}, err
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return credentials{}, err
	}
	return creds, nil
}

func saveCredentials(creds credentials) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func resolveCredentials(apiURLFlag string) (credentials, error) {
	creds, err := loadCredentials()
	if err != nil {
		return credentials{}, err
	}
	if env := strings.TrimSpace(os.Getenv("KEPLER_TOKEN")); env != "" {
		creds.Token = env
	}
	if env := strings.TrimSpace(os.Getenv("KEPLER_API_URL")); env != "" {
		creds.APIURL = env
	}
	if strings.TrimSpace(apiURLFlag) != "" {
		creds.APIURL = strings.TrimRight(apiURLFlag, "/")
	}
	if creds.APIURL == "" {
		creds.APIURL = packagedAPIURL()
	}
	creds.APIURL = strings.TrimRight(strings.TrimSpace(creds.APIURL), "/")
	if creds.Token == "" || creds.APIURL == "" {
		return credentials{}, errors.New("not logged in. Run: kepler-agent login")
	}
	return creds, nil
}

func packagedAPIURL() string {
	if env := strings.TrimSpace(os.Getenv("KEPLER_API_URL")); env != "" {
		return strings.TrimRight(env, "/")
	}
	return strings.TrimRight(strings.TrimSpace(DefaultAPIURL), "/")
}

func requirePublicAPIURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid --api-url")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "0.0.0.0" {
		return errors.New("login must use the public gateway URL (ngrok), not localhost")
	}
	if parsed.Scheme != "https" {
		return errors.New("login --api-url must be https (the public ngrok gateway)")
	}
	return nil
}

func runLogin(args []string) error {
	fs := flagSet("login")
	apiURL := fs.String("api-url", packagedAPIURL(), "public Kepler gateway URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*apiURL = strings.TrimRight(strings.TrimSpace(*apiURL), "/")
	if *apiURL == "" {
		return errors.New("no public gateway URL: rebuild the CLI with kepler-agent-deploy/scripts/build-cli.sh, or pass --api-url")
	}
	if err := requirePublicAPIURL(*apiURL); err != nil {
		return err
	}

	start, err := http.NewRequest(http.MethodPost, *apiURL+"/cli/device", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(start)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("start login: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var device struct {
		DeviceCode string `json:"device_code"`
		LoginURL   string `json:"login_url"`
	}
	if err := json.Unmarshal(body, &device); err != nil {
		return err
	}
	if device.DeviceCode == "" || device.LoginURL == "" {
		return errors.New("gateway did not return a public login URL")
	}

	fmt.Printf("Open this URL to sign in with Slack:\n%s\n", device.LoginURL)
	_ = openBrowser(device.LoginURL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.New("timed out waiting for Slack login")
		case <-ticker.C:
			creds, done, err := pollLogin(ctx, *apiURL, device.DeviceCode)
			if err != nil {
				return err
			}
			if !done {
				continue
			}
			if err := saveCredentials(creds); err != nil {
				return err
			}
			fmt.Printf("Logged in as %s\n", creds.UserID)
			return nil
		}
	}
}

func pollLogin(ctx context.Context, apiURL, device string) (credentials, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/cli/device?device="+url.QueryEscape(device), nil)
	if err != nil {
		return credentials{}, false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return credentials{}, false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return credentials{}, false, err
	}
	if resp.StatusCode == http.StatusAccepted {
		return credentials{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return credentials{}, false, fmt.Errorf("login: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Status string `json:"status"`
		Token  string `json:"token"`
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return credentials{}, false, err
	}
	if payload.Status != "complete" || payload.Token == "" {
		return credentials{}, false, errors.New("login did not return a session")
	}
	return credentials{Token: payload.Token, APIURL: apiURL, UserID: payload.UserID}, true, nil
}

func runLogout() error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Println("Logged out.")
	return nil
}

func runWhoami() error {
	creds, err := resolveCredentials("")
	if err != nil {
		return err
	}
	fmt.Printf("api   %s\nuser  %s\n", creds.APIURL, creds.UserID)
	return nil
}

func fetchBootstrap(ctx context.Context, creds credentials) (cloud.Bootstrap, error) {
	return cloud.FetchBootstrap(ctx, creds.APIURL, creds.Token)
}

func applyBootstrap(config local.Config, info cloud.Bootstrap) local.Config {
	config.Provider = info.Provider
	config.Protocol = "kepler"
	config.Model = info.Model
	config.AnthropicFlavor = info.AnthropicFlavor
	config.ReasoningEffort = info.Thinking
	return config
}

func flagSet(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ContinueOnError)
}
