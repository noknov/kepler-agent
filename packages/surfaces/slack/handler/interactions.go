package slackhandler

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/gateway"
	"github.com/noknov/kepler-agent/packages/userprefs"
)

const (
	rulesCallbackID            = "user_rules_manage"
	skillsCallbackID           = "user_skills_manage"
	connectionChooseCallbackID = "connection_choose"
	connectionCallbackID       = "connection_add"
)

type connectionModalContext struct {
	Provider      string `json:"provider,omitempty"`
	Origin        string `json:"origin,omitempty"`
	ReturnContext string `json:"return_context,omitempty"`
}

func encodeConnectionModalContext(value connectionModalContext) string {
	if value.Provider == "" && value.Origin == "" && value.ReturnContext == "" {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func decodeConnectionModalContext(raw string) connectionModalContext {
	var value connectionModalContext
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &value) != nil {
		return value
	}
	return value
}

func connectionModalContextFromAction(raw string) connectionModalContext {
	value := decodeConnectionModalContext(raw)
	if value.Provider == "" && !strings.Contains(raw, "{") {
		// Compatibility with older connection cards whose value was the
		// provider id. New cards always use the structured value above.
		value.Provider = strings.TrimSpace(raw)
	}
	return value
}

func connectionAddModal(plugins []connections.Plugin) map[string]any {
	return connectionAddModalForContext(plugins, connectionModalContext{})
}

func connectionAddModalForContext(plugins []connections.Plugin, modalContext connectionModalContext) map[string]any {
	options := make([]map[string]any, 0, len(plugins))
	for _, plugin := range plugins {
		options = append(options, map[string]any{
			"text": plainText(plugin.Title), "value": plugin.ID,
		})
	}
	blocks := []map[string]any{
		{"type": "context", "elements": []map[string]any{{"type": "mrkdwn", "text": "Choose what you want to connect. You can add another connection of the same provider later."}}},
		inputBlock("connection_provider", "connection_provider", "Integration", map[string]any{
			"type": "static_select", "action_id": "connection_provider", "placeholder": plainText("Choose an integration"), "options": options,
		}, false, ""),
	}
	view := map[string]any{
		"type": "modal", "callback_id": connectionChooseCallbackID,
		"title": plainText("Add integration"), "submit": plainText("Continue"), "close": plainText("Cancel"), "blocks": blocks,
	}
	if metadata := encodeConnectionModalContext(modalContext); metadata != "" {
		view["private_metadata"] = metadata
	}
	return view
}

func connectionDetailsModal(plugin connections.Plugin) map[string]any {
	return connectionDetailsModalForContext(plugin, connectionModalContext{Provider: plugin.ID})
}

func connectionDetailsModalForContext(plugin connections.Plugin, modalContext connectionModalContext) map[string]any {
	blocks := []map[string]any{{"type": "context", "elements": []map[string]any{{"type": "mrkdwn", "text": "Connect " + plugin.Title + ". Optional settings are shown only when this integration supports them."}}}}
	blocks = append(blocks, inputBlock("connection_label", "connection_label", "Name", plainTextInput("connection_label", false), true, "Optional name for this connection, useful when you add more than one."))
	for _, field := range plugin.Fields {
		element := plainTextInput(field.ID, false)
		if field.Secret {
			element["is_password"] = true
		}
		if field.Placeholder != "" {
			element["placeholder"] = plainText(field.Placeholder)
		}
		blocks = append(blocks, inputBlock("connection_"+field.ID, field.ID, field.Label, element, !field.Required, field.Description))
	}
	view := map[string]any{
		"type": "modal", "callback_id": connectionCallbackID,
		"title": plainText("Connect " + plugin.Title), "submit": plainText("Continue"), "close": plainText("Cancel"), "blocks": blocks,
	}
	if metadata := encodeConnectionModalContext(modalContext); metadata != "" {
		view["private_metadata"] = metadata
	}
	return view
}

func validateConnectionLabel(label string) string {
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > 80 {
		return "Name must be 80 characters or fewer."
	}
	if strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return "Name contains unsupported control characters."
	}
	return ""
}

func connectionAuthModal(plugin connections.Plugin, authURL string) map[string]any {
	return map[string]any{
		"type":        "modal",
		"callback_id": connectionCallbackID,
		"title":       plainText("Connect " + plugin.Title),
		"close":       plainText("Close"),
		"blocks": []map[string]any{
			{"type": "context", "elements": []map[string]any{{"type": "mrkdwn", "text": "Your connection is ready. Open the secure authorization page in your browser."}}},
			{"type": "actions", "elements": []map[string]any{{"type": "button", "action_id": "connection_open", "text": plainText("Open authorization page"), "url": authURL, "style": "primary"}}},
		},
	}
}

func assetModal(kind userprefs.AssetKind, existing []userprefs.Asset) map[string]any {
	title := "Manage rules"
	contentLabel := "Rule text"
	hint := "Upload or paste Markdown, MDC, text, or JSON. Uploaded files with the same name replace older entries."
	blocks := []map[string]any{
		{
			"type": "context",
			"elements": []map[string]any{{
				"type": "mrkdwn",
				"text": "This manager controls your custom items; built-in rules and skills remain available. New uploads are enabled immediately. Disable an item to keep it saved without adding it to agent context.",
			}},
		},
		inputBlock("asset_files", "asset_files", "Upload files", map[string]any{
			"type":      "file_input",
			"action_id": "asset_files",
			"max_files": 5,
		}, true, hint),
		inputBlock("asset_name", "asset_name", "Name", plainTextInput("asset_name", false), true, "Required only when pasting text."),
	}
	if kind == userprefs.KindSkill {
		title = "Manage skills"
		contentLabel = "Skill text"
		blocks = append(blocks, inputBlock("asset_description", "asset_description", "Description", plainTextInput("asset_description", false), true, "Short description shown before the skill is loaded."))
	}
	blocks = append(blocks, inputBlock("asset_content", "asset_content", contentLabel, plainTextInput("asset_content", true), true, "Optional when uploading files."))
	blocks = append(blocks, existingAssetBlocks(kind, existing)...)
	callbackID := rulesCallbackID
	if kind == userprefs.KindSkill {
		callbackID = skillsCallbackID
	}
	return map[string]any{
		"type":        "modal",
		"callback_id": callbackID,
		"title":       plainText(title),
		"submit":      plainText("Save"),
		"close":       plainText("Cancel"),
		"blocks":      blocks,
	}
}

func existingAssetBlocks(kind userprefs.AssetKind, assets []userprefs.Asset) []map[string]any {
	blocks := []map[string]any{
		{
			"type": "divider",
		},
		{
			"type": "section",
			"text": map[string]any{"type": "mrkdwn", "text": "*Existing*"},
		},
	}
	if len(assets) == 0 {
		return append(blocks, map[string]any{
			"type": "context",
			"elements": []map[string]any{{
				"type": "mrkdwn",
				"text": "No saved items yet.",
			}},
		})
	}
	for _, asset := range assets {
		text := "*" + asset.Name + "*"
		if asset.Description != "" {
			text += "\n" + asset.Description
		}
		status := "Active"
		actionID := "disable_asset"
		buttonLabel := "Disable"
		buttonStyle := ""
		if !asset.Active {
			status = "Disabled"
			actionID = "enable_asset"
			buttonLabel = "Enable"
			buttonStyle = "primary"
		}
		text += "\n_" + status + "_"
		value := fmt.Sprintf("%s:%s", kind, asset.ID)
		blocks = append(blocks, map[string]any{
			"type": "section",
			"text": map[string]any{
				"type": "mrkdwn",
				"text": text,
			},
		}, map[string]any{
			"type": "actions",
			"elements": []map[string]any{
				assetActionButton(actionID, buttonLabel, value, buttonStyle),
				assetActionButton("delete_asset", "Delete", value, "danger"),
			},
		})
	}
	return blocks
}

func assetActionButton(actionID, label, value, style string) map[string]any {
	button := map[string]any{
		"type":      "button",
		"action_id": actionID,
		"text":      plainText(label),
		"value":     value,
	}
	if style != "" {
		button["style"] = style
	}
	return button
}

func inputBlock(blockID, actionID, label string, element map[string]any, optional bool, hint string) map[string]any {
	element["action_id"] = actionID
	block := map[string]any{
		"type":     "input",
		"block_id": blockID,
		"label":    plainText(label),
		"element":  element,
		"optional": optional,
	}
	if strings.TrimSpace(hint) != "" {
		block["hint"] = plainText(hint)
	}
	return block
}

func plainTextInput(actionID string, multiline bool) map[string]any {
	return map[string]any{
		"type":      "plain_text_input",
		"action_id": actionID,
		"multiline": multiline,
	}
}

func plainText(text string) map[string]any {
	return map[string]any{"type": "plain_text", "text": text, "emoji": true}
}

func assetKindFromCallback(callbackID string) (userprefs.AssetKind, bool) {
	switch callbackID {
	case rulesCallbackID:
		return userprefs.KindRule, true
	case skillsCallbackID:
		return userprefs.KindSkill, true
	default:
		return "", false
	}
}

func parseAssetActionValue(value string) (userprefs.AssetKind, string, bool) {
	kindText, id, ok := strings.Cut(value, ":")
	if !ok || strings.TrimSpace(id) == "" {
		return "", "", false
	}
	kind := userprefs.AssetKind(kindText)
	if kind != userprefs.KindRule && kind != userprefs.KindSkill {
		return "", "", false
	}
	return kind, id, true
}

func selectedFiles(state map[string]map[string]slackgateway.InteractionValue) []slack.File {
	var out []slack.File
	for _, actions := range state {
		for _, value := range actions {
			out = append(out, value.SelectedFiles...)
		}
	}
	return out
}

func submittedTextAsset(state map[string]map[string]slackgateway.InteractionValue) (content, name string) {
	return stateValue(state, "asset_content", "asset_content"), stateValue(state, "asset_name", "asset_name")
}

func submittedDescription(state map[string]map[string]slackgateway.InteractionValue) string {
	return stateValue(state, "asset_description", "asset_description")
}

func stateValue(state map[string]map[string]slackgateway.InteractionValue, blockID, actionID string) string {
	if actions, ok := state[blockID]; ok {
		if value, ok := actions[actionID]; ok {
			return strings.TrimSpace(value.Value)
		}
	}
	return ""
}

func stateSelectedValues(state map[string]map[string]slackgateway.InteractionValue, blockID, actionID string) []string {
	if actions, ok := state[blockID]; ok {
		if value, ok := actions[actionID]; ok {
			return append([]string(nil), value.SelectedValues...)
		}
	}
	return nil
}

func mergeSlackFile(primary, fallback slack.File) slack.File {
	if primary.ID == "" {
		primary.ID = fallback.ID
	}
	if primary.Name == "" {
		primary.Name = fallback.Name
	}
	if primary.Title == "" {
		primary.Title = fallback.Title
	}
	if primary.Mimetype == "" {
		primary.Mimetype = fallback.Mimetype
	}
	if primary.Filetype == "" {
		primary.Filetype = fallback.Filetype
	}
	if primary.Size == 0 {
		primary.Size = fallback.Size
	}
	if primary.URLPrivate == "" {
		primary.URLPrivate = fallback.URLPrivate
	}
	if primary.URLPrivateDownload == "" {
		primary.URLPrivateDownload = fallback.URLPrivateDownload
	}
	return primary
}
