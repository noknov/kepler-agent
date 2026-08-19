package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/connections"
)

func runConnect(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: slack-copilot connect <provider>")
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case connections.ProviderSlack:
		return connectSlack()
	case connections.ProviderGitHub:
		return connectGitHub()
	case connections.ProviderClickStack:
		return connectClickStack()
	case connections.ProviderGCP:
		return connectGCP()
	case connections.ProviderNotion:
		return connectNotion()
	default:
		return fmt.Errorf("unsupported provider %q", args[0])
	}
}

func connectGitHub() error {
	cfg, err := config.LoadFor(config.ProfileCLI)
	if err != nil {
		return err
	}
	if !cfg.Connections.GitHubOAuthEnabled() {
		return errors.New("set GITHUB_OAUTH_CLIENT_ID and GITHUB_OAUTH_CLIENT_SECRET before connecting")
	}
	if strings.TrimSpace(cfg.Connections.EncryptionKey) == "" {
		return errors.New("set CONNECTIONS_ENCRYPTION_KEY before connecting")
	}
	stateDir, err := defaultConnectStateDir()
	if err != nil {
		return err
	}
	store, err := connections.NewFileStore(filepath.Join(stateDir, "connections.json"), cfg.Connections.EncryptionKey)
	if err != nil {
		return err
	}
	port := cfg.Connections.LocalOAuthPort
	if port <= 0 {
		port = 8765
	}
	service := connections.NewServiceFromConfig(store, cfg)
	service.Config.PublicBaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	startURL, err := service.StartURL(connections.LocalUserID, connections.ProviderGitHub)
	if err != nil {
		return err
	}

	done := make(chan error, 1)
	server := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           connections.NewHTTPHandler(service),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			done <- err
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	go waitForConnection(ctx, store, connections.ProviderGitHub, done)

	fmt.Printf("Open this URL to connect GitHub:\n%s\n", startURL)
	_ = openBrowser(startURL)

	select {
	case err := <-done:
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		if err != nil {
			return err
		}
		conn, getErr := store.Get(context.Background(), connections.LocalUserID, connections.ProviderGitHub)
		if getErr != nil {
			return getErr
		}
		if conn.Account != "" {
			fmt.Printf("Connected GitHub as %s.\n", conn.Account)
		} else {
			fmt.Println("Connected GitHub.")
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		return errors.New("timed out waiting for GitHub authorization")
	}
}

func connectSlack() error {
	cfg, err := config.LoadFor(config.ProfileCLI)
	if err != nil {
		return err
	}
	if !cfg.Connections.SlackOAuthEnabled() {
		return errors.New("set SLACK_OAUTH_CLIENT_ID and SLACK_OAUTH_CLIENT_SECRET before connecting")
	}
	if strings.TrimSpace(cfg.Connections.EncryptionKey) == "" {
		return errors.New("set CONNECTIONS_ENCRYPTION_KEY before connecting")
	}
	stateDir, err := defaultConnectStateDir()
	if err != nil {
		return err
	}
	store, err := connections.NewFileStore(filepath.Join(stateDir, "connections.json"), cfg.Connections.EncryptionKey)
	if err != nil {
		return err
	}
	port := cfg.Connections.LocalOAuthPort
	if port <= 0 {
		port = 8765
	}
	service := connections.NewServiceFromConfig(store, cfg)
	service.Config.PublicBaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	startURL, err := service.StartURL(connections.LocalUserID, connections.ProviderSlack)
	if err != nil {
		return err
	}

	done := make(chan error, 1)
	server := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           connections.NewHTTPHandler(service),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			done <- err
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	go waitForConnection(ctx, store, connections.ProviderSlack, done)

	fmt.Printf("Open this URL to connect Slack:\n%s\n", startURL)
	_ = openBrowser(startURL)

	select {
	case err := <-done:
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		if err != nil {
			return err
		}
		conn, getErr := store.Get(context.Background(), connections.LocalUserID, connections.ProviderSlack)
		if getErr != nil {
			return getErr
		}
		if conn.Account != "" {
			fmt.Printf("Connected Slack as %s.\n", conn.Account)
		} else {
			fmt.Println("Connected Slack.")
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		return errors.New("timed out waiting for Slack authorization")
	}
}

func connectClickStack() error {
	cfg, err := config.LoadFor(config.ProfileCLI)
	if err != nil {
		return err
	}
	serviceCfg := connections.NewServiceFromConfig(nil, cfg).Config
	if !serviceCfg.ClickStackEnabled() {
		return errors.New("set CLICKSTACK_SERVICE_ID and CONNECTIONS_PUBLIC_BASE_URL before connecting ClickStack")
	}
	if strings.TrimSpace(cfg.Connections.EncryptionKey) == "" {
		return errors.New("set CONNECTIONS_ENCRYPTION_KEY before connecting")
	}
	stateDir, err := defaultConnectStateDir()
	if err != nil {
		return err
	}
	store, err := connections.NewFileStore(filepath.Join(stateDir, "connections.json"), cfg.Connections.EncryptionKey)
	if err != nil {
		return err
	}
	port := cfg.Connections.LocalOAuthPort
	if port <= 0 {
		port = 8765
	}
	service := connections.NewServiceFromConfig(store, cfg)
	service.Config.PublicBaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	startURL, err := service.StartURL(connections.LocalUserID, connections.ProviderClickStack)
	if err != nil {
		return err
	}

	done := make(chan error, 1)
	server := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           connections.NewHTTPHandler(service),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			done <- err
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	go waitForConnection(ctx, store, connections.ProviderClickStack, done)

	fmt.Printf("Open this URL to connect ClickStack:\n%s\n", startURL)
	_ = openBrowser(startURL)

	select {
	case err := <-done:
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		if err != nil {
			return err
		}
		fmt.Println("Connected ClickStack.")
		return nil
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		return errors.New("timed out waiting for ClickStack authorization")
	}
}

func connectGCP() error {
	cfg, err := config.LoadFor(config.ProfileCLI)
	if err != nil {
		return err
	}
	serviceCfg := connections.NewServiceFromConfig(nil, cfg).Config
	if !serviceCfg.GCPEnabled() {
		return errors.New("set GCP_OAUTH_CLIENT_ID, GCP_OAUTH_CLIENT_SECRET, and CONNECTIONS_PUBLIC_BASE_URL before connecting GCP")
	}
	if strings.TrimSpace(cfg.Connections.EncryptionKey) == "" {
		return errors.New("set CONNECTIONS_ENCRYPTION_KEY before connecting")
	}
	stateDir, err := defaultConnectStateDir()
	if err != nil {
		return err
	}
	store, err := connections.NewFileStore(filepath.Join(stateDir, "connections.json"), cfg.Connections.EncryptionKey)
	if err != nil {
		return err
	}
	port := cfg.Connections.LocalOAuthPort
	if port <= 0 {
		port = 8765
	}
	service := connections.NewServiceFromConfig(store, cfg)
	service.Config.PublicBaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	startURL, err := service.StartURL(connections.LocalUserID, connections.ProviderGCP)
	if err != nil {
		return err
	}

	done := make(chan error, 1)
	server := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           connections.NewHTTPHandler(service),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			done <- err
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	go waitForConnection(ctx, store, connections.ProviderGCP, done)

	fmt.Printf("Open this URL to connect Google Cloud:\n%s\n", startURL)
	_ = openBrowser(startURL)

	select {
	case err := <-done:
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		if err != nil {
			return err
		}
		fmt.Println("Connected Google Cloud.")
		return nil
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		return errors.New("timed out waiting for Google Cloud authorization")
	}
}

func connectNotion() error {
	cfg, err := config.LoadFor(config.ProfileCLI)
	if err != nil {
		return err
	}
	serviceCfg := connections.NewServiceFromConfig(nil, cfg).Config
	if !serviceCfg.NotionEnabled() {
		return errors.New("set NOTION_OAUTH_CLIENT_ID, NOTION_OAUTH_CLIENT_SECRET, and CONNECTIONS_PUBLIC_BASE_URL before connecting Notion")
	}
	if strings.TrimSpace(cfg.Connections.EncryptionKey) == "" {
		return errors.New("set CONNECTIONS_ENCRYPTION_KEY before connecting")
	}
	stateDir, err := defaultConnectStateDir()
	if err != nil {
		return err
	}
	store, err := connections.NewFileStore(filepath.Join(stateDir, "connections.json"), cfg.Connections.EncryptionKey)
	if err != nil {
		return err
	}
	port := cfg.Connections.LocalOAuthPort
	if port <= 0 {
		port = 8765
	}
	service := connections.NewServiceFromConfig(store, cfg)
	service.Config.PublicBaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	startURL, err := service.StartURL(connections.LocalUserID, connections.ProviderNotion)
	if err != nil {
		return err
	}

	done := make(chan error, 1)
	server := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           connections.NewHTTPHandler(service),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			done <- err
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	go waitForConnection(ctx, store, connections.ProviderNotion, done)

	fmt.Printf("Open this URL to connect Notion:\n%s\n", startURL)
	_ = openBrowser(startURL)

	select {
	case err := <-done:
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		if err != nil {
			return err
		}
		fmt.Println("Connected Notion.")
		return nil
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		return errors.New("timed out waiting for Notion authorization")
	}
}

func waitForConnection(ctx context.Context, store connections.Store, provider string, done chan<- error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := store.Get(ctx, connections.LocalUserID, provider); err == nil {
				done <- nil
				return
			}
		}
	}
}

func defaultConnectStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".slack-copilot")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
