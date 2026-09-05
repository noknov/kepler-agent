package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/noknov/kepler-agent/packages/appserver"
)

func main() {
	schemaPath := flag.String("schema", "docs/app-server.schema.json", "JSON schema output path")
	typescriptPath := flag.String("typescript", "apps/cli/src/generated/appServerProtocol.ts", "TypeScript output path")
	flag.Parse()
	schema, err := appserver.GenerateJSONSchema()
	if err != nil {
		fatal(err)
	}
	if err := write(*schemaPath, append(schema, '\n')); err != nil {
		fatal(err)
	}
	if err := write(*typescriptPath, appserver.GenerateTypeScript()); err != nil {
		fatal(err)
	}
}

func write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
