package configoptions_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/common/configoptions"
	"github.com/sagernet/sing-box/common/configscript"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
)

func TestParsePreservesOrdinaryJSONCAndRegisteredOptions(t *testing.T) {
	source := []byte(`{
  // retained ordinary JSONC comment
  "inbounds": [{"type":"direct","tag":"in"}],
  "outbounds": [{"type":"direct","tag":"out"}],
}`)
	ctx := include.Context(context.Background())
	want, err := json.UnmarshalExtendedContext[option.Options](ctx, source)
	if err != nil {
		t.Fatalf("direct original decode: %v", err)
	}
	options, err := configoptions.Parse(ctx, source, configscript.Host{Client: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(options.RawMessage, want.RawMessage) {
		t.Fatalf("RawMessage = %q, want direct-decoder value %q", options.RawMessage, want.RawMessage)
	}
	if !reflect.DeepEqual(options.CommentsSet, want.CommentsSet) {
		t.Fatalf("CommentsSet = %#v, want direct-decoder value %#v", options.CommentsSet, want.CommentsSet)
	}
	if options.CommentsSet == nil || len(options.CommentsSet.Comments) == 0 {
		t.Fatalf("CommentsSet = %#v, want retained ordinary comments", options.CommentsSet)
	}
	if len(options.Inbounds) != 1 || options.Inbounds[0].Tag != "in" {
		t.Fatalf("inbounds = %#v, want registered direct inbound", options.Inbounds)
	}
	if _, ok := options.Inbounds[0].Options.(*option.DirectInboundOptions); !ok {
		t.Fatalf("inbound options type = %T, want *option.DirectInboundOptions", options.Inbounds[0].Options)
	}
	if len(options.Outbounds) != 1 || options.Outbounds[0].Tag != "out" {
		t.Fatalf("outbounds = %#v, want registered direct outbound", options.Outbounds)
	}
	if _, ok := options.Outbounds[0].Options.(*option.DirectOutboundOptions); !ok {
		t.Fatalf("outbound options type = %T, want *option.DirectOutboundOptions", options.Outbounds[0].Options)
	}
}

func TestParseUsesSuppliedHostAndSeparatesGeneratedResult(t *testing.T) {
	source := []byte(`/* @starlark
result = {"log": {"level": host.client}}
*/ {"log":{"level":"source"}}`)
	for _, client := range []string{"cli", "desktop", "sfa", "sfi", "sfm", "sft", "libbox"} {
		t.Run(client, func(t *testing.T) {
			options, err := configoptions.Parse(include.Context(context.Background()), source, configscript.Host{Client: client})
			if err != nil {
				t.Fatal(err)
			}
			if options.Log == nil || options.Log.Level != client {
				t.Fatalf("generated log level = %#v, want host.client %q", options.Log, client)
			}
			if bytes.Equal(options.RawMessage, source) || strings.Contains(string(options.RawMessage), "@starlark") {
				t.Fatalf("RawMessage = %q, want generated JSON separate from marked source", options.RawMessage)
			}
		})
	}
}

func TestParseGeneratedErrorsKeepSourcePositionsWithoutAttribution(t *testing.T) {
	source := []byte(`/* @starlark
result = {"unknown_field": True}
*/
{
  "log": {
    /* @starlark
    result = value
    */
    "level": "debug"
  }
}`)
	_, err := configoptions.Parse(include.Context(context.Background()), source, configscript.Host{Client: "cli"})
	if err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("Parse error = %v, want strict generated unknown-field error", err)
	}
	if !strings.Contains(err.Error(), "configuration generated from source containing @starlark comments at 1:1, 6:5") {
		t.Fatalf("Parse error = %v, want both source positions without script attribution", err)
	}
	if strings.Contains(err.Error(), "configuration script at") {
		t.Fatalf("generated decode error falsely attributes its cause to one script: %v", err)
	}

	_, err = configoptions.Parse(include.Context(context.Background()), []byte(`{"unknown_field":true}`), configscript.Host{Client: "cli"})
	if err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("ordinary decode error = %v, want unknown-field error", err)
	}
	if strings.Contains(err.Error(), "configuration generated from source") {
		t.Fatalf("ordinary decode error falsely claims generated source: %v", err)
	}
}

func TestParseGenerationErrorRetainsCauseAndScriptPosition(t *testing.T) {
	source := []byte(`/* @starlark
result = missing_name
*/ {}`)
	_, err := configoptions.Parse(include.Context(context.Background()), source, configscript.Host{Client: "cli"})
	if err == nil || !strings.Contains(err.Error(), "configuration script at 1:1") || !strings.Contains(err.Error(), "generate config") {
		t.Fatalf("Parse error = %v, want generation phase and script position", err)
	}
	if !strings.Contains(err.Error(), "missing_name") {
		t.Fatalf("Parse error = %v, want original Starlark cause", err)
	}
}

func TestParsePreservesCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(include.Context(context.Background()))
	cancel()
	source := []byte(`/* @starlark
result = value
*/ {}`)
	_, err := configoptions.Parse(ctx, source, configscript.Host{Client: "cli"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Parse error = %v, want caller context cancellation", err)
	}
}
