package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/infra/http1"
)

func ProviderBaseURL(apiURL, protocol string) string {
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if strings.EqualFold(protocol, "anthropic") {
		return apiURL
	}
	return apiURL + "/v1"
}

func FetchBootstrap(ctx context.Context, apiURL, token string) (Bootstrap, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiURL, "/")+"/cli/bootstrap", nil)
	if err != nil {
		return Bootstrap{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("ngrok-skip-browser-warning", "true")
	resp, err := http1.Standard(30 * time.Second).Do(req)
	if err != nil {
		return Bootstrap{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Bootstrap{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Bootstrap{}, fmt.Errorf("bootstrap: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var info Bootstrap
	if err := json.Unmarshal(body, &info); err != nil {
		return Bootstrap{}, err
	}
	if info.Model == "" || info.Protocol == "" {
		return Bootstrap{}, fmt.Errorf("bootstrap did not return model and protocol")
	}
	return info, nil
}
