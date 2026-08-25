package slackhome

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/connections"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

const refreshChannel = "slack:home:refresh"
const conversationModePrefix = "user:conversation_mode:"

type Publisher interface {
	PublishHome(context.Context, string, map[string]any) error
}

type Controller struct {
	Cfg         config.Config
	Access      safety.AccessPolicy
	Slack       Publisher
	Store       userprefs.Store
	Redis       *redisclient.Client
	Connections connections.Service
}

func (c Controller) Publish(ctx context.Context, userID string) error {
	if c.Slack == nil || userID == "" {
		return nil
	}
	return c.Slack.PublishHome(ctx, userID, c.View(userID))
}

func (c Controller) RequestRefresh(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	if c.Redis == nil {
		return c.Publish(ctx, userID)
	}
	return c.Redis.Publish(ctx, refreshChannel, userID)
}

func (c Controller) StartRefreshSubscriber(ctx context.Context) {
	if c.Redis == nil || c.Slack == nil {
		return
	}
	sub := c.Redis.Subscribe(ctx, refreshChannel)
	defer sub.Close()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			userID := strings.TrimSpace(msg.Payload)
			if userID == "" {
				continue
			}
			if err := c.Publish(context.Background(), userID); err != nil {
				log.Printf("publish home from refresh request failed user=%s: %v", userID, err)
			}
		}
	}
}

func (c Controller) ToggleWebSearch(ctx context.Context, userID string) {
	if c.Store == nil || userID == "" {
		return
	}
	_ = c.Store.SetWebSearchEnabled(ctx, userID, !c.WebSearchEnabled(userID))
	if err := c.RequestRefresh(context.Background(), userID); err != nil {
		log.Printf("refresh home after web search toggle failed: %v", err)
	}
}

func (c Controller) WebSearchEnabled(userID string) bool {
	if c.Store == nil {
		return true
	}
	settings, err := c.Store.GetSettings(context.Background(), userID)
	if err != nil {
		return true
	}
	return settings.WebSearchEnabled
}

func (c Controller) ConversationMode(userID string) string {
	if c.Redis == nil || strings.TrimSpace(userID) == "" {
		return "steer"
	}
	mode, err := c.Redis.Get(context.Background(), conversationModePrefix+userID)
	if err != nil || mode != "queue" {
		return "steer"
	}
	return mode
}

func (c Controller) ToggleConversationMode(ctx context.Context, userID string) {
	if c.Redis == nil || strings.TrimSpace(userID) == "" {
		return
	}
	mode := "queue"
	if c.ConversationMode(userID) == "queue" {
		mode = "steer"
	}
	if err := c.Redis.Set(ctx, conversationModePrefix+userID, mode, 0); err != nil {
		log.Printf("save conversation mode failed: %v", err)
		return
	}
	if err := c.RequestRefresh(context.Background(), userID); err != nil {
		log.Printf("refresh home after conversation mode toggle failed: %v", err)
	}
}

func (c Controller) View(userID string) map[string]any {
	allowed := c.Access.AllowsUser(userID)
	accessStatus := "Allowed"
	if !allowed {
		accessStatus = "Not allowlisted"
	}

	secondary := strings.TrimSpace(c.Cfg.LLM.SecondaryModel)
	if secondary == "" {
		secondary = c.Cfg.LLM.Model
	}
	webSearchOn := c.WebSearchEnabled(userID)
	webSearchStatus := "On"
	webSearchBtnStyle := "primary"
	if !webSearchOn {
		webSearchStatus = "Off"
		webSearchBtnStyle = ""
	}
	ruleCount := userprefs.CountByKind(context.Background(), c.Store, userID, userprefs.KindRule)
	skillCount := userprefs.CountByKind(context.Background(), c.Store, userID, userprefs.KindSkill)
	statusFields := []map[string]any{
		mrkdwnField("*Access*\n" + accessStatus),
		mrkdwnField("*Web Search*\n" + webSearchStatus),
		mrkdwnField(fmt.Sprintf("*Rules*\n%d active", ruleCount)),
		mrkdwnField(fmt.Sprintf("*Skills*\n%d active", skillCount)),
		mrkdwnField("*Primary Model*\n" + modelDisplayName(c.Cfg.LLM.Model)),
		mrkdwnField("*Explorer / Summary*\n" + explorerSummaryDisplayName(secondary, c.Cfg.LLM.MultimodalModel)),
	}
	blocks := []map[string]any{
		contextBlock("Mention the agent in a channel or use the Messages tab to start a private thread."),
		dividerBlock(),
		headerBlock(":signal_strength: Status"),
		sectionBlockWithFields("", statusFields...),
		dividerBlock(),
		headerBlock(":control_knobs: Controls"),
		actionsBlock(
			actionButton("manage_rules", "Manage Rules", "rule", ""),
			actionButton("manage_skills", "Manage Skills", "skill", ""),
			actionButton("toggle_web_search", "Web Search "+boolLabel(webSearchOn), "web_search", webSearchBtnStyle),
		),
	}
	blocks = append(blocks, c.connectionBlocks(userID)...)

	return map[string]any{
		"type":   "home",
		"blocks": blocks,
	}
}

func (c Controller) connectionBlocks(userID string) []map[string]any {
	if c.Connections.Store == nil || !c.connectionsSectionEnabled() {
		return nil
	}
	statusByProvider := map[string]connections.Connection{}
	if listed, err := c.Connections.Store.List(context.Background(), userID); err == nil {
		statusByProvider = connections.StatusMap(listed)
	}
	serverCreds := c.serverCredentialConnections()
	blocks := []map[string]any{
		dividerBlock(),
		headerBlock(":electric_plug: Connections"),
	}
	for _, plugin := range connections.Plugins() {
		if !c.Connections.ProviderOAuthEnabled(plugin.ID) {
			continue
		}
		if plugin.ID == connections.ProviderGitHub && c.serverGitHubCredentialsActive() {
			continue
		}
		status := "Not connected"
		account := ""
		if item, ok := statusByProvider[plugin.ID]; ok && item.Status == connections.StatusConnected {
			switch plugin.ID {
			case connections.ProviderNotion:
				if !c.Connections.NotionMCPConnected(context.Background(), userID) {
					status = "Invalid"
				} else {
					status = "Connected"
					account = item.Account
				}
			case connections.ProviderClickStack:
				if !c.Connections.ClickStackConnected(context.Background(), userID) {
					status = "Invalid"
				} else {
					status = "Connected"
					account = item.Account
				}
			default:
				status = "Connected"
				account = item.Account
			}
		}
		text := fmt.Sprintf("*%s*\n%s", plugin.Title, status)
		if account != "" {
			text += fmt.Sprintf(" (`%s`)", account)
		}
		authURL, err := c.Connections.StartURL(userID, plugin.ID)
		if err != nil || authURL == "" {
			blocks = append(blocks, sectionBlock(text))
			continue
		}
		buttonLabel := "Connect"
		buttonStyle := "primary"
		if status == "Connected" {
			buttonLabel = "Reconnect"
			buttonStyle = ""
		}
		blocks = append(blocks, sectionBlockWithAccessory(text, actionButtonURL(buttonLabel, authURL, buttonStyle)))
	}
	for _, title := range serverCreds {
		text := fmt.Sprintf("*%s*\nConnected (`server credentials`)", title)
		blocks = append(blocks, sectionBlock(text))
	}
	if len(blocks) <= 2 {
		return nil
	}
	return blocks
}

func (c Controller) connectionsSectionEnabled() bool {
	if c.Connections.Config.OAuthEnabled() {
		return true
	}
	return len(c.serverCredentialConnections()) > 0
}

func (c Controller) serverCredentialConnections() []string {
	var titles []string
	if c.localGCPCredentialsActive() {
		titles = append(titles, "Google Cloud")
	}
	if c.serverYouTrackCredentialsActive() {
		titles = append(titles, "YouTrack")
	}
	if c.serverGitHubCredentialsActive() {
		titles = append(titles, "GitHub")
	}
	return titles
}

func (c Controller) serverGitHubCredentialsActive() bool {
	return strings.TrimSpace(c.Cfg.Integrations.GitHub.Token) != ""
}

func (c Controller) serverYouTrackCredentialsActive() bool {
	return strings.TrimSpace(c.Cfg.Integrations.YouTrack.URL) != "" &&
		strings.TrimSpace(c.Cfg.Integrations.YouTrack.Token) != ""
}

func (c Controller) localGCPCredentialsActive() bool {
	if c.Connections.Config.GCPEnabled() {
		return false
	}
	return strings.TrimSpace(c.Cfg.Integrations.GCP.DefaultProject) != ""
}

func explorerSummaryDisplayName(secondaryModel, imageModel string) string {
	secondary := modelDisplayName(secondaryModel)
	imageModel = strings.TrimSpace(imageModel)
	if imageModel == "" {
		return secondary
	}
	image := modelDisplayName(imageModel)
	if image == secondary {
		return secondary
	}
	return image + " + " + secondary
}

func modelDisplayName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "Unknown"
	}
	if label, ok := modelDisplayNames[model]; ok {
		return label
	}
	return formatModelDisplayName(model)
}

var modelDisplayNames = map[string]string{
	"ox-alpha-free":        "Ox Alpha",
	"ox-alpha":             "Ox Alpha",
	"mimo-v2.5":            "MiMo V2.5",
	"mimo-v2.5-free":       "MiMo V2.5",
	"mimo-v2.5-pro":        "MiMo V2.5 Pro",
	"gpt-5.6-luna":         "GPT-5.6 Luna",
	"glm-5.2":              "GLM 5.2",
	"kimi-k2.7-code":       "Kimi K2.7 Code",
	"kimi-k2.6":            "Kimi K2.6",
	"kimi/kimi-k2.7-code":  "Kimi K2.7 Code",
	"minimax-m3":           "MiniMax M3",
	"minimax-m3-free":      "MiniMax M3",
	"LongCat-2.0":          "LongCat 2.0",
	"deepseek-v4-flash":           "DeepSeek V4 Flash",
	"deepseek-v4-flash-vision-exp": "DeepSeek V4 Flash Vision Exp",
	"nemotron-3-ultra-free": "Nemotron 3 Ultra",
	"north-mini-code-free": "North Mini Code",
	"claude-sonnet-4-5-20250929": "Claude Sonnet 4.5",
	"gpt-4o-mini":          "GPT-4o Mini",
}

func formatModelDisplayName(model string) string {
	parts := strings.Split(model, "/")
	model = parts[len(parts)-1]
	for _, suffix := range []string{"-free", "-pro", "-flash"} {
		if strings.HasSuffix(model, suffix) && len(model) > len(suffix)+1 {
			model = strings.TrimSuffix(model, suffix)
			break
		}
	}
	segments := strings.Split(model, "-")
	for i, segment := range segments {
		segments[i] = formatModelSegment(segment)
	}
	return strings.Join(segments, " ")
}

func formatModelSegment(segment string) string {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return segment
	}
	if strings.HasPrefix(strings.ToLower(segment), "v") && len(segment) > 1 {
		return "V" + segment[1:]
	}
	switch strings.ToLower(segment) {
	case "mimo":
		return "MiMo"
	case "gpt":
		return "GPT"
	case "glm":
		return "GLM"
	case "kimi":
		return "Kimi"
	case "minimax":
		return "MiniMax"
	case "deepseek":
		return "DeepSeek"
	case "longcat":
		return "LongCat"
	case "nemotron":
		return "Nemotron"
	case "codex":
		return "Codex"
	case "ox":
		return "Ox"
	case "alpha":
		return "Alpha"
	case "luna":
		return "Luna"
	case "ultra":
		return "Ultra"
	case "mini":
		return "Mini"
	case "code":
		return "Code"
	case "north":
		return "North"
	}
	if segment[0] >= '0' && segment[0] <= '9' {
		return segment
	}
	if len(segment) == 1 {
		return strings.ToUpper(segment)
	}
	return strings.ToUpper(segment[:1]) + strings.ToLower(segment[1:])
}

func mrkdwnField(text string) map[string]any {
	return map[string]any{"type": "mrkdwn", "text": text}
}

func headerBlock(text string) map[string]any {
	return map[string]any{
		"type": "header",
		"text": map[string]any{
			"type":  "plain_text",
			"text":  text,
			"emoji": true,
		},
	}
}

func sectionBlockWithFields(text string, fields ...map[string]any) map[string]any {
	block := map[string]any{"type": "section"}
	if len(fields) > 0 {
		block["fields"] = fields
	}
	if text != "" {
		block["text"] = map[string]any{"type": "mrkdwn", "text": text}
	}
	return block
}

func boolLabel(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

func sectionBlock(text string) map[string]any {
	return sectionBlockWithFields(text)
}

func contextBlock(text string) map[string]any {
	return map[string]any{
		"type": "context",
		"elements": []map[string]any{{
			"type": "mrkdwn",
			"text": text,
		}},
	}
}

func sectionBlockWithAccessory(text string, accessory map[string]any) map[string]any {
	return map[string]any{
		"type":      "section",
		"text":      map[string]any{"type": "mrkdwn", "text": text},
		"accessory": accessory,
	}
}

func actionsBlock(elements ...map[string]any) map[string]any {
	return map[string]any{
		"type":     "actions",
		"elements": elements,
	}
}

func actionButton(actionID, label, value, style string) map[string]any {
	btn := map[string]any{
		"type":      "button",
		"action_id": actionID,
		"text": map[string]any{
			"type":  "plain_text",
			"text":  label,
			"emoji": true,
		},
		"value": value,
	}
	if style != "" {
		btn["style"] = style
	}
	return btn
}

func actionButtonURL(label, url, style string) map[string]any {
	btn := map[string]any{
		"type": "button",
		"text": map[string]any{
			"type":  "plain_text",
			"text":  label,
			"emoji": true,
		},
		"url": url,
	}
	if style != "" {
		btn["style"] = style
	}
	return btn
}

func dividerBlock() map[string]any {
	return map[string]any{"type": "divider"}
}
