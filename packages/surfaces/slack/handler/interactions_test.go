package slackhandler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/userprefs"
)

func TestParseAssetActionValue(t *testing.T) {
	kind, id, ok := parseAssetActionValue("rule:U1:rule:style")
	if !ok || kind != "rule" || id != "U1:rule:style" {
		t.Fatalf("parseAssetActionValue() = %q, %q, %v", kind, id, ok)
	}
}

func TestAssetModalShowsImmediateActivationAndActiveControls(t *testing.T) {
	modal := assetModal(userprefs.KindSkill, []userprefs.Asset{
		{ID: "U1:skill:active", Name: "active", Active: true},
		{ID: "U1:skill:disabled", Name: "disabled", Active: false},
	})
	raw, err := json.Marshal(modal)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"New uploads are enabled immediately",
		"built-in rules and skills remain available",
		"disable_asset",
		"enable_asset",
		"delete_asset",
		"Active",
		"Disabled",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("asset modal missing %q: %s", want, body)
		}
	}
}
