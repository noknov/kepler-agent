package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	observabilitysvc "github.com/noknov/slack-copilot-agent/observability"
)

func main() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := observabilitysvc.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
