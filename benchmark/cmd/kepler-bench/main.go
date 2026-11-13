package main

import (
	"fmt"
	"os"

	"github.com/noknov/kepler-agent/benchmark"
)

func main() {
	if err := benchmark.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
