package main

import (
	"context"
	"fmt"
	"os"

	"github.com/noknov/slack-copilot-agent/benchmarks/benchcli"
)

func main() {
	if err := benchcli.Run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
