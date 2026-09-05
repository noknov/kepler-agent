package llm

import (
	"net/http"
	"strings"
)

// setOpenCodeSessionHeader attaches the stable agent session identifier that
// OpenCode Go uses for request routing and prompt-cache affinity.
func setOpenCodeSessionHeader(httpReq *http.Request, provider string, metadata map[string]string) {
	if strings.TrimSpace(provider) != "opencode-go" {
		return
	}
	if sessionID := strings.TrimSpace(metadata["session_id"]); sessionID != "" {
		httpReq.Header.Set("x-opencode-session", sessionID)
	}
}
