package slackhandler

import (
	"fmt"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/gateway"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

const (
	rulesCallbackID  = "user_rules_manage"
	skillsCallbackID = "user_skills_manage"
)

func assetModal(kind userprefs.AssetKind, existing []userprefs.Asset) map[string]any {
	title := "Manage rules"
	contentLabel := "Rule text"
	hint := "Upload or paste Markdown, MDC, text, or JSON. Uploaded files with the same name replace older entries."
	blocks := []map[string]any{
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
		blocks = append(blocks, map[string]any{
			"type": "section",
			"text": map[string]any{
				"type": "mrkdwn",
				"text": text,
			},
			"accessory": map[string]any{
				"type":      "button",
				"action_id": "delete_asset",
				"text":      plainText("Delete"),
				"value":     fmt.Sprintf("%s:%s", kind, asset.ID),
				"style":     "danger",
			},
		})
	}
	return blocks
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
