package libbox

import (
	"context"
	"strings"
	"testing"
)

func TestLibboxClientIdentityUsesDistinctPlatformTargets(t *testing.T) {
	for target, test := range map[string]struct {
		android bool
		ios     bool
		tvos    bool
		darwin  bool
		client  string
	}{
		"android": {android: true, client: "sfa"},
		"ios":     {ios: true, client: "sfi"},
		"macos":   {darwin: true, client: "sfm"},
		"tvos":    {ios: true, tvos: true, client: "sft"},
	} {
		if got := libboxClientForTarget(test.android, test.ios, test.tvos, test.darwin); got != test.client {
			t.Errorf("identity for %s target = %q, want %q", target, got, test.client)
		}
	}
}

func TestLibboxConfigEntrypointsUseCurrentTargetAndRefuseScriptFormatting(t *testing.T) {
	host := currentLibboxConfigScriptHost()
	source := `/* @starlark
if host.client == "` + host.Client + `":
  result = {"log": {"level": "debug"}}
else:
  result = {"unknown_target": True}
*/ {}`
	options, err := parseConfig(context.Background(), source, host)
	if err != nil {
		t.Fatal(err)
	}
	if options.Log == nil || options.Log.Level != "debug" {
		t.Fatalf("parsed log options = %#v, want the current target branch", options.Log)
	}
	if err = CheckConfig(source); err != nil {
		t.Fatalf("CheckConfig rejected the current target branch: %v", err)
	}
	invalidSource := `/* @starlark
result = {"not_a_config_field": True}
*/ {}`
	if err = CheckConfig(invalidSource); err == nil || !strings.Contains(err.Error(), "configuration generated from source containing @starlark comments at 1:1") {
		t.Fatalf("CheckConfig generated-result error = %v, want non-attributing script source context", err)
	}
	hasTunInbound, err := HasTunInbound(source)
	if err != nil {
		t.Fatalf("HasTunInbound rejected generated config: %v", err)
	}
	if hasTunInbound {
		t.Fatal("HasTunInbound detected a TUN inbound in an empty configuration")
	}
	if _, err = FormatConfig(source); err == nil || !strings.Contains(err.Error(), "Starlark") {
		t.Fatalf("FormatConfig error = %v, want source-preservation refusal", err)
	}
}
