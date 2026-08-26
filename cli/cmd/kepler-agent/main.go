package main

import (
	"fmt"
	"os"

	"github.com/noknov/kepler-agent/cli"
)

func main() {
	if err := cli.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
