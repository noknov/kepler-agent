// Command kepler is the terminal entrypoint for Kepler's local product.
// The native desktop entrypoint lives separately in apps/desktop.
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
