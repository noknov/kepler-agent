package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/noknov/kepler-agent/packages/llm"
)

func runQoder(args []string) error {
	if len(args) != 1 || args[0] != "models" {
		return fmt.Errorf("usage: kepler-agent qoder models (operator QODER_PERSONAL_ACCESS_TOKEN required)")
	}
	baseURL := os.Getenv("QODER_BASE_URL")
	if baseURL == "" {
		baseURL = llm.QoderBaseURL
	}
	client, err := llm.NewQoderClient(baseURL, os.Getenv("QODER_PERSONAL_ACCESS_TOKEN"), time.Minute)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	models, err := client.Models(ctx)
	if err != nil {
		return err
	}
	for _, model := range models {
		fmt.Fprintf(os.Stdout, "%s\t%s\n", model.Key, model.Name)
	}
	return nil
}
