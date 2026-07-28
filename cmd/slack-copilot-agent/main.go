package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	slackbot "github.com/noknov/slack-copilot-agent/packages/slackbot"
)

func main() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := slackbot.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
