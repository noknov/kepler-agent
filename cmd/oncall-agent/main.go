package main

import (
	"context"
	"log"
	"os"

	"github.com/wati/oncall-agent/internal/app"
)

func main() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	if err := app.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
