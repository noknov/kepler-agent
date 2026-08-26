package messaging

import "testing"

func TestAttributionTextMentionsBot(t *testing.T) {
	got := (Attribution{BotUserID: "U-app", Name: "斗包"}).Text()
	if got != "Sent using <@U-app|斗包>" {
		t.Fatalf("unexpected attribution: %q", got)
	}
}

func TestAttributionTextUsesFooterOverride(t *testing.T) {
	got := (Attribution{Footer: "Custom footer"}).Text()
	if got != "Custom footer" {
		t.Fatalf("unexpected attribution: %q", got)
	}
}
