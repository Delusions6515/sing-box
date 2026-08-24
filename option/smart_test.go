package option

import (
	"context"
	"reflect"
	"testing"

	"github.com/sagernet/sing-box/schema"
	"github.com/sagernet/sing/common/json"
	"github.com/stretchr/testify/require"
)

func TestSmartASNSourceValidation(t *testing.T) {
	for name, options := range map[string]SmartOptions{
		"valid":              {ASN: SmartASNOptions{Repository: "example/rules", Branch: "stable", AssetPath: "sing/asn"}},
		"invalid repository": {ASN: SmartASNOptions{Repository: "https://example.com/rules"}},
		"invalid asset path": {ASN: SmartASNOptions{AssetPath: "../asn"}},
	} {
		t.Run(name, func(t *testing.T) {
			err := options.Validate()
			if name == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestSmartASNSourceJSON(t *testing.T) {
	var options SmartOptions
	require.NoError(t, json.Unmarshal([]byte(`{
  "asn": {
    "repository": "example/rules",
    "branch": "stable",
    "asset_path": "sing/asn"
  }
}`), &options))
	require.Equal(t, "example/rules", options.ASN.Repository)
	require.Equal(t, "stable", options.ASN.Branch)
	require.Equal(t, "sing/asn", options.ASN.AssetPath)
}

func TestSmartOptionsSchema(t *testing.T) {
	_, err := schema.Generate(context.Background(), reflect.TypeFor[SmartOptions]())
	require.NoError(t, err)
}
