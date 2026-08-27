package slackhandler

import "testing"

func TestParseAssetActionValue(t *testing.T) {
	kind, id, ok := parseAssetActionValue("rule:U1:rule:style")
	if !ok || kind != "rule" || id != "U1:rule:style" {
		t.Fatalf("parseAssetActionValue() = %q, %q, %v", kind, id, ok)
	}
}
