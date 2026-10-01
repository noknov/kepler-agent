package cli

import (
	"context"
	"flag"
	"fmt"
	"github.com/noknov/kepler-agent/packages/llm/chatgpt"
	"os"
	"time"
)

func runChatGPT(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kepler-agent chatgpt login|models|status [--credentials-file PATH]")
	}
	flags := flag.NewFlagSet("chatgpt "+args[0], flag.ContinueOnError)
	path := flags.String("credentials-file", chatgpt.DefaultPath(), "operator ChatGPT session file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected ChatGPT command arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	switch args[0] {
	case "login":
		return chatgpt.Login(ctx, *path, os.Stdout)
	case "models":
		return chatgpt.Models(ctx, *path, os.Stdout)
	case "status":
		s, err := chatgpt.Load(*path)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "ChatGPT account: %s\nAccess token expires: %s (renewed automatically on use)\n", s.Email, s.ExpiresAt.Format(time.RFC3339))
		return nil
	default:
		return fmt.Errorf("unknown ChatGPT command %q", args[0])
	}
}
