package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/noknov/slack-copilot-agent/appserver"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := appserver.Run(ctx, os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
