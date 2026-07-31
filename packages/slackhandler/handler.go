package slackhandler

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/conversation"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/runs"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/slack"
	"github.com/noknov/slack-copilot-agent/packages/slackgateway"
	"github.com/noknov/slack-copilot-agent/packages/slackhome"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

type Handler struct {
	Cfg       config.Config
	Slack     *slack.Client
	Access    safety.AccessPolicy
	Conv      *conversation.Service
	Prompt    safety.PromptPolicy
	Metrics   *observability.Recorder
	Runs      runs.Store
	Home      slackhome.Controller
	UserPrefs userprefs.Store
}

func (h *Handler) Handle(ctx context.Context, eventID string, ev slack.Event) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic while handling Slack event %s: %v", eventID, recovered)
		}
	}()
	switch ev.Type {
	case "app_home_opened":
		h.handleAppHome(ctx, ev)
		return nil
	case "app_mention":
		return h.handleMention(ctx, eventID, ev)
	case "message":
		return h.handleMessage(ctx, eventID, ev)
	case "file_shared":
		return h.handleFileShared(ctx, eventID, ev)
	case "reaction_added":
		if ev.Item.Type == "message" {
			if h.Metrics != nil {
				h.Metrics.Reaction(ev.Reaction)
			}
			h.recordReactionFeedback(ctx, ev)
		}
	}
	return nil
}

func (h *Handler) WebSearchPreference(userID string) bool {
	return h.Home.WebSearchEnabled(userID)
}

func (h *Handler) ToggleWebSearch(userID string) {
	go h.Home.ToggleWebSearch(context.Background(), userID)
}

func (h *Handler) HandleInteraction(ctx context.Context, interaction slackgateway.Interaction) {
	if interaction.UserID == "" {
		return
	}
	switch interaction.Type {
	case "block_actions":
		h.handleBlockActions(ctx, interaction)
	case "view_submission":
		h.handleViewSubmission(ctx, interaction)
	}
}

func (h *Handler) handleBlockActions(ctx context.Context, interaction slackgateway.Interaction) {
	for _, action := range interaction.Actions {
		switch action.ActionID {
		case "toggle_user_setting":
			if action.Value == "web_search" {
				h.Home.ToggleWebSearch(ctx, interaction.UserID)
			}
		case "manage_rules":
			h.openAssetModal(ctx, interaction.TriggerID, interaction.UserID, userprefs.KindRule)
		case "manage_skills":
			h.openAssetModal(ctx, interaction.TriggerID, interaction.UserID, userprefs.KindSkill)
		case "delete_asset":
			h.deleteAsset(ctx, interaction, action.Value)
		}
	}
}

func (h *Handler) openAssetModal(ctx context.Context, triggerID, userID string, kind userprefs.AssetKind) {
	if h.Slack == nil || h.UserPrefs == nil || triggerID == "" {
		return
	}
	assets := h.listAssets(ctx, userID, kind)
	if err := h.Slack.OpenView(ctx, triggerID, assetModal(kind, assets)); err != nil {
		log.Printf("open %s modal failed: %v", kind, err)
		if userID != "" {
			_, _ = h.Slack.PostMessage(ctx, userID, "", fmt.Sprintf("Couldn't open %s manager: %v", kind, err))
		}
	}
}

func (h *Handler) deleteAsset(ctx context.Context, interaction slackgateway.Interaction, value string) {
	kind, id, ok := parseAssetActionValue(value)
	if !ok || h.UserPrefs == nil {
		return
	}
	if err := h.UserPrefs.DeleteAsset(ctx, interaction.UserID, kind, id); err != nil {
		log.Printf("delete %s failed user=%s id=%s err=%v", kind, interaction.UserID, id, err)
		return
	}
	assets := h.listAssets(ctx, interaction.UserID, kind)
	if h.Slack != nil && interaction.View.ID != "" {
		if err := h.Slack.UpdateView(ctx, interaction.View.ID, assetModal(kind, assets)); err != nil {
			log.Printf("refresh %s modal failed user=%s err=%v", kind, interaction.UserID, err)
		}
	}
	if err := h.Home.Publish(context.Background(), interaction.UserID); err != nil {
		log.Printf("publish home after %s delete failed: %v", kind, err)
	}
}

func (h *Handler) handleViewSubmission(ctx context.Context, interaction slackgateway.Interaction) {
	kind, ok := assetKindFromCallback(interaction.View.CallbackID)
	if !ok || h.UserPrefs == nil || h.Slack == nil {
		return
	}
	var saved int
	for _, file := range selectedFiles(interaction.View.State) {
		if file.ID == "" || !userprefs.AllowedUploadFile(file) {
			continue
		}
		info, err := h.Slack.FileInfo(ctx, file.ID)
		if err != nil {
			log.Printf("load uploaded %s file info failed user=%s file=%s err=%v", kind, interaction.UserID, file.ID, err)
			continue
		}
		file = mergeSlackFile(file, info)
		if !userprefs.AllowedUploadFile(file) || file.Size > userprefs.MaxUploadBytes {
			log.Printf("skip uploaded %s file user=%s file=%s: unsupported type or too large", kind, interaction.UserID, file.ID)
			continue
		}
		data, err := h.Slack.DownloadFile(ctx, file, userprefs.MaxUploadBytes)
		if err != nil {
			log.Printf("download uploaded %s file failed user=%s file=%s err=%v", kind, interaction.UserID, file.ID, err)
			continue
		}
		asset, err := userprefs.BuildAsset(kind, interaction.UserID, file, data)
		if err != nil {
			log.Printf("parse uploaded %s file failed user=%s file=%s err=%v", kind, interaction.UserID, file.ID, err)
			continue
		}
		if _, err := h.UserPrefs.UpsertAsset(ctx, asset); err != nil {
			log.Printf("save uploaded %s file failed user=%s file=%s err=%v", kind, interaction.UserID, file.ID, err)
			continue
		}
		saved++
	}
	if text, name := submittedTextAsset(interaction.View.State); text != "" && name != "" {
		asset := userprefs.Asset{
			UserID:      interaction.UserID,
			Kind:        kind,
			Name:        name,
			Description: submittedDescription(interaction.View.State),
			Content:     text,
			Active:      true,
		}
		if _, err := h.UserPrefs.UpsertAsset(ctx, asset); err != nil {
			log.Printf("save pasted %s failed user=%s err=%v", kind, interaction.UserID, err)
		} else {
			saved++
		}
	}
	if saved > 0 {
		if err := h.Home.Publish(context.Background(), interaction.UserID); err != nil {
			log.Printf("publish home after %s upload failed: %v", kind, err)
		}
	}
}

func (h *Handler) listAssets(ctx context.Context, userID string, kind userprefs.AssetKind) []userprefs.Asset {
	if h.UserPrefs == nil {
		return nil
	}
	assets, err := h.UserPrefs.ListAssets(ctx, userID, kind)
	if err != nil {
		log.Printf("list %s assets failed user=%s err=%v", kind, userID, err)
		return nil
	}
	return assets
}

func (h *Handler) handleAppHome(ctx context.Context, ev slack.Event) {
	if ev.User == "" {
		return
	}
	if ev.Tab != "" && ev.Tab != "home" {
		return
	}
	if err := h.Home.Publish(ctx, ev.User); err != nil {
		log.Printf("publish home failed: %v", err)
	}
}

func (h *Handler) recordReactionFeedback(ctx context.Context, ev slack.Event) {
	if h.Runs == nil || ev.Item.Channel == "" || ev.Item.TS == "" || ev.Reaction == "" {
		return
	}
	_, ok, err := h.Runs.AddFeedbackForMessage(ctx, ev.Item.Channel, ev.Item.TS, runs.Feedback{
		Source:    "slack_reaction",
		Value:     ev.Reaction,
		UserID:    ev.User,
		Channel:   ev.Item.Channel,
		MessageTS: ev.Item.TS,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		log.Printf("record reaction feedback failed channel=%s ts=%s reaction=%s err=%v", ev.Item.Channel, ev.Item.TS, ev.Reaction, err)
		return
	}
	if !ok {
		log.Printf("reaction feedback had no matching run channel=%s ts=%s reaction=%s", ev.Item.Channel, ev.Item.TS, ev.Reaction)
	}
}

func (h *Handler) handleMention(ctx context.Context, eventID string, ev slack.Event) error {
	if !IsChannelMention(ev) {
		return nil
	}
	if ev.User == "" || ev.User == h.Cfg.Slack.BotUserID || ev.BotID != "" {
		return nil
	}
	threadTS := ev.ConversationThreadTS()
	if !h.Access.IsAllowed(ev.User, ev.Channel) {
		if h.Metrics != nil {
			h.Metrics.Denied()
		}
		_, _ = h.Slack.PostMessage(ctx, ev.Channel, threadTS, "<@"+ev.User+"> Sorry, you don't have permission to use this bot here.")
		return nil
	}
	text := h.Prompt.CleanUserText(h.Cfg.Slack.BotUserID, ev.Text)
	text, parts := h.attachSlackFiles(ctx, text, ev.Files)
	if text == "" {
		text = prompts.AppMessage("empty_mention", "")
	}
	if !h.Conv.HandleMention(ctx, conversation.Request{
		EventID:      eventID,
		UserID:       ev.User,
		Channel:      ev.Channel,
		ThreadTS:     threadTS,
		Text:         text,
		ContentParts: parts,
	}) {
		return fmt.Errorf("conversation did not accept app_mention event")
	}
	return nil
}

func (h *Handler) handleMessage(ctx context.Context, eventID string, ev slack.Event) error {
	if IsAppDM(ev) {
		return h.handleDirectMessage(ctx, eventID, ev)
	}
	return h.handleChannelReply(ctx, eventID, ev)
}

func (h *Handler) handleFileShared(ctx context.Context, eventID string, ev slack.Event) error {
	userID := FirstNonEmpty(ev.User, ev.UserID)
	channelID := FirstNonEmpty(ev.Channel, ev.ChannelID)
	if userID == "" || userID == h.Cfg.Slack.BotUserID || channelID == "" {
		return nil
	}
	if !IsDMChannel(channelID) {
		return nil
	}
	file := ev.File
	if file.ID == "" {
		file.ID = ev.FileID
	}
	if file.ID == "" {
		return nil
	}
	if ev.ConversationThreadTS() == "" {
		log.Printf("skip slack file_shared %s: no message timestamp; waiting for message.file_share event", file.ID)
		return nil
	}
	if !h.Access.AllowsUser(userID) {
		if h.Metrics != nil {
			h.Metrics.Denied()
		}
		_, _ = h.Slack.PostMessage(ctx, channelID, ev.ConversationThreadTS(), "<@"+userID+"> Sorry, you don't have permission to use this bot.")
		return nil
	}
	text, parts := h.attachSlackFiles(ctx, "", []slack.File{file})
	if text == "" {
		text = prompts.AppMessage("empty_dm_with_file", "")
	}
	if !h.Conv.HandleMention(ctx, conversation.Request{
		EventID:      eventID,
		UserID:       userID,
		Channel:      channelID,
		ThreadTS:     ev.ConversationThreadTS(),
		Text:         text,
		ContentParts: parts,
	}) {
		return fmt.Errorf("conversation did not accept file_shared event")
	}
	return nil
}

func (h *Handler) handleDirectMessage(ctx context.Context, eventID string, ev slack.Event) error {
	if !IsUserMessageSubtype(ev.Subtype) || ev.BotID != "" || ev.User == "" || ev.User == h.Cfg.Slack.BotUserID {
		return nil
	}
	if !h.Access.AllowsUser(ev.User) {
		if h.Metrics != nil {
			h.Metrics.Denied()
		}
		_, _ = h.Slack.PostMessage(ctx, ev.Channel, ev.ConversationThreadTS(), "<@"+ev.User+"> Sorry, you don't have permission to use this bot.")
		return nil
	}
	if IsThreadReply(ev) {
		text, parts := h.attachSlackFiles(ctx, strings.TrimSpace(ev.Text), ev.Files)
		if text != "" && h.Conv.HandleReply(ctx, conversation.Request{
			EventID:      eventID,
			UserID:       ev.User,
			Channel:      ev.Channel,
			ThreadTS:     ev.ThreadTS,
			Text:         text,
			ContentParts: parts,
		}) {
			return nil
		}
	}
	text := strings.TrimSpace(ev.Text)
	text, parts := h.attachSlackFiles(ctx, text, ev.Files)
	if text == "" {
		text = prompts.AppMessage("empty_dm", "")
	}
	if !h.Conv.HandleMention(ctx, conversation.Request{
		EventID:      eventID,
		UserID:       ev.User,
		Channel:      ev.Channel,
		ThreadTS:     ev.ConversationThreadTS(),
		Text:         text,
		ContentParts: parts,
	}) {
		return fmt.Errorf("conversation did not accept direct message event")
	}
	return nil
}

func (h *Handler) handleChannelReply(ctx context.Context, eventID string, ev slack.Event) error {
	if !IsThreadReply(ev) || !IsUserMessageSubtype(ev.Subtype) || ev.BotID != "" || ev.User == "" || ev.User == h.Cfg.Slack.BotUserID {
		return nil
	}
	if h.Cfg.Slack.BotUserID != "" && strings.Contains(ev.Text, "<@"+h.Cfg.Slack.BotUserID+">") {
		return nil
	}
	if !h.Access.IsAllowed(ev.User, ev.Channel) {
		if h.Metrics != nil {
			h.Metrics.Denied()
		}
		return nil
	}
	text, parts := h.attachSlackFiles(ctx, strings.TrimSpace(ev.Text), ev.Files)
	if text == "" {
		return nil
	}
	_ = h.Conv.HandleReply(ctx, conversation.Request{
		EventID:      eventID,
		UserID:       ev.User,
		Channel:      ev.Channel,
		ThreadTS:     ev.ThreadTS,
		Text:         text,
		ContentParts: parts,
	})
	return nil
}

func IsAppDM(ev slack.Event) bool {
	return ev.ChannelType == "im" || IsDMChannel(ev.Channel)
}

func IsChannelMention(ev slack.Event) bool {
	return ev.Type == "app_mention" && !IsAppDM(ev)
}

func IsDMChannel(channel string) bool {
	return strings.HasPrefix(channel, "D")
}

func IsUserMessageSubtype(subtype string) bool {
	return subtype == "" || subtype == "file_share"
}

func IsThreadReply(ev slack.Event) bool {
	return ev.ThreadTS != "" && ev.ThreadTS != ev.TS
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
