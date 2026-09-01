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
		"valid":                {ASN: SmartASNOptions{}},
		"valid URL":            {ASN: SmartASNOptions{URL: "https://example.com/GeoLite2-ASN.mmdb"}},
		"invalid interval":     {ASN: SmartASNOptions{UpdateInterval: -1}},
		"invalid asn interval": {ASN: SmartASNOptions{UpdateInterval: -1}},
	} {
		t.Run(name, func(t *testing.T) {
			err := options.Validate()
			if name == "invalid interval" || name == "invalid asn interval" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSmartASNSourceJSON(t *testing.T) {
	var options SmartOptions
	require.NoError(t, json.Unmarshal([]byte(`{
  "asn": {
    "path": "smart/asn/GeoLite2-ASN.mmdb",
    "url": "https://example.com/GeoLite2-ASN.mmdb"
  }
}`), &options))
	require.Equal(t, "smart/asn/GeoLite2-ASN.mmdb", options.ASN.Path)
	require.Equal(t, "https://example.com/GeoLite2-ASN.mmdb", options.ASN.URL)
}

func TestSmartOptionsSchema(t *testing.T) {
	_, err := schema.Generate(context.Background(), reflect.TypeFor[SmartOptions]())
	require.NoError(t, err)
}
