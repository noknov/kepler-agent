package slack

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

type WorkObjectRef struct {
	ID   string `json:"id"`
	Type string `json:"type,omitempty"`
}
type WorkObjectTitle struct {
	Text string `json:"text"`
}
type WorkObjectAttributes struct {
	Title                WorkObjectTitle `json:"title"`
	DisplayID            string          `json:"display_id,omitempty"`
	DisplayType          string          `json:"display_type,omitempty"`
	MetadataLastModified int64           `json:"metadata_last_modified,omitempty"`
}
type WorkObjectField struct {
	Value any `json:"value"`
}
type WorkObjectCustomField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
	Type  string `json:"type"`
}
type WorkObjectPayload struct {
	Attributes   WorkObjectAttributes       `json:"attributes"`
	Fields       map[string]WorkObjectField `json:"fields"`
	CustomFields []WorkObjectCustomField    `json:"custom_fields,omitempty"`
}
type WorkObject struct {
	AppUnfurlURL  string            `json:"app_unfurl_url"`
	URL           string            `json:"url"`
	ExternalRef   WorkObjectRef     `json:"external_ref"`
	EntityType    string            `json:"entity_type"`
	EntityPayload WorkObjectPayload `json:"entity_payload"`
}

// UnfurlWorkObject replaces one preview using a source-owned metadata snapshot.
// It does not subscribe to events, execute an investigation, or enable editing.
func (c *Client) UnfurlWorkObject(ctx context.Context, channel, messageTS string, object WorkObject) error {
	if strings.TrimSpace(channel) == "" || strings.TrimSpace(messageTS) == "" || object.ExternalRef.ID == "" || object.EntityPayload.Attributes.Title.Text == "" || object.EntityType == "" {
		return fmt.Errorf("a Work Object requires an explicit message target and complete source identity")
	}
	for index, raw := range []string{object.URL, object.AppUnfurlURL} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return fmt.Errorf("Work Object URLs must use HTTPS without credentials")
		}
		if index == 0 && (parsed.RawQuery != "" || parsed.Fragment != "") {
			return fmt.Errorf("Work Object source URL must be canonical without query or fragment")
		}
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	payload := map[string]any{"channel": channel, "ts": messageTS, "metadata": map[string]any{"entities": []WorkObject{object}}}
	if err := c.postJSONNonIdempotent(ctx, "chat.unfurl", payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return slackWriteError("chat.unfurl", out.Error)
	}
	return nil
}
