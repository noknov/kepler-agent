package connections

import (
	"strings"
)

type NotionOAuthConfig struct {
	ClientID     string
	ClientSecret string
}

func (c NotionOAuthConfig) Enabled() bool {
	return strings.TrimSpace(c.ClientID) != "" && strings.TrimSpace(c.ClientSecret) != ""
}
