package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/noknov/kepler-agent/worker"
)

func main() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := worker.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
