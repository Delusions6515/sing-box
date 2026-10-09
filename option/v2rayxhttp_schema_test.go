package option

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"testing"

	"github.com/sagernet/sing-box/schema"

	"github.com/stretchr/testify/require"
)

func TestXHTTPTransportSchemaIncludesDownloadSettings(t *testing.T) {
	content, err := schema.Generate(context.Background(), reflect.TypeFor[V2RayTransportOptions]())
	require.NoError(t, err)
	require.Contains(t, string(content), `"const": "xhttp"`)
	require.Contains(t, string(content), `"download_settings"`)
	require.Contains(t, string(content), `"session_placement"`)
}

func TestXHTTPRangeSchemaMatchesScalarAndStringEncoding(t *testing.T) {
	content, err := schema.Generate(context.Background(), reflect.TypeFor[XHTTPRange]())
	require.NoError(t, err)
	var node schema.Node
	require.NoError(t, json.Unmarshal(content, &node))
	require.Len(t, node.AnyOf, 2)
	require.Equal(t, "integer", node.AnyOf[0].Type)
	require.EqualValues(t, -2147483648, *node.AnyOf[0].Minimum)
	require.EqualValues(t, 2147483647, *node.AnyOf[0].Maximum)
	pattern := regexp.MustCompile(node.AnyOf[1].Pattern)
	for _, value := range []string{"100", "100-1000", "-3--1", " +2 - -1 ", ""} {
		require.True(t, pattern.MatchString(value), value)
	}
	require.False(t, pattern.MatchString("abc"))
}
