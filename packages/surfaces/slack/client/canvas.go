package slack

import (
	"context"
	"fmt"
)

type CanvasContent struct {
	Type     string `json:"type"`
	Markdown string `json:"markdown"`
}

// CreateCanvas creates a new standalone canvas and returns its ID.
func (c *Client) CreateCanvas(ctx context.Context, title string, content *CanvasContent) (string, error) {
	payload := map[string]any{}
	if title != "" {
		payload["title"] = title
	}
	if content != nil {
		payload["document_content"] = content
	}
	var out struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error,omitempty"`
		CanvasID string `json:"canvas_id,omitempty"`
	}
	if err := c.postJSON(ctx, "canvases.create", payload, &out); err != nil {
		return "", fmt.Errorf("slack canvases.create: %w", err)
	}
	if !out.OK {
		return "", fmt.Errorf("slack canvases.create failed: %s", out.Error)
	}
	return out.CanvasID, nil
}

// CreateChannelCanvas creates a canvas attached to a specific channel.
func (c *Client) CreateChannelCanvas(ctx context.Context, channelID, title string, content *CanvasContent) (string, error) {
	payload := map[string]any{
		"channel_id": channelID,
	}
	if title != "" {
		payload["title"] = title
	}
	if content != nil {
		payload["document_content"] = content
	}
	var out struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error,omitempty"`
		CanvasID string `json:"canvas_id,omitempty"`
	}
	if err := c.postJSON(ctx, "canvases.create", payload, &out); err != nil {
		return "", fmt.Errorf("slack canvases.create: %w", err)
	}
	if !out.OK {
		return "", fmt.Errorf("slack canvases.create failed: %s", out.Error)
	}
	return out.CanvasID, nil
}

type CanvasEditChange struct {
	Operation       string         `json:"operation"`
	SectionID       string         `json:"section_id,omitempty"`
	DocumentContent *CanvasContent `json:"document_content,omitempty"`
}

// EditCanvas applies a change to an existing canvas.
func (c *Client) EditCanvas(ctx context.Context, canvasID string, change CanvasEditChange) error {
	payload := map[string]any{
		"canvas_id": canvasID,
		"changes":   []CanvasEditChange{change},
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "canvases.edit", payload, &out); err != nil {
		return fmt.Errorf("slack canvases.edit: %w", err)
	}
	if !out.OK {
		return fmt.Errorf("slack canvases.edit failed: %s", out.Error)
	}
	return nil
}

// SetCanvasAccess sets user or channel-level access for a canvas.
func (c *Client) SetCanvasAccess(ctx context.Context, canvasID string, accessLevel string, channelIDs []string) error {
	payload := map[string]any{
		"canvas_id":    canvasID,
		"access_level": accessLevel,
	}
	if len(channelIDs) > 0 {
		payload["channel_ids"] = channelIDs
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "canvases.access.set", payload, &out); err != nil {
		return fmt.Errorf("slack canvases.access.set: %w", err)
	}
	if !out.OK {
		return fmt.Errorf("slack canvases.access.set failed: %s", out.Error)
	}
	return nil
}
