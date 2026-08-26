package cli

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/noknov/kepler-agent/packages/profiles/local"
)

//go:embed config.example.toml
var defaultConfig []byte

func runConfig(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: kepler-agent config <init|show>")
	}
	switch args[0] {
	case "show":
		path, err := local.DefaultConfigPath()
		if err != nil {
			return err
		}
		fmt.Println(path)
		return nil
	case "init":
		flags := flag.NewFlagSet("config init", flag.ContinueOnError)
		path := flags.String("path", "", "configuration file path")
		force := flags.Bool("force", false, "overwrite an existing config")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *path == "" {
			var err error
			*path, err = local.DefaultConfigPath()
			if err != nil {
				return err
			}
		}
		if _, err := os.Stat(*path); err == nil && !*force {
			return fmt.Errorf("config already exists: %s (use --force to replace it)", *path)
		}
		if err := os.MkdirAll(filepath.Dir(*path), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(*path, defaultConfig, 0600); err != nil {
			return err
		}
		fmt.Printf("Created %s\nSet the selected profile's API key environment variable, then run: kepler-agent --profile deepseek --cwd .\n", *path)
		return nil
	default:
		return fmt.Errorf("unknown config command %q (use init or show)", args[0])
	}
}
