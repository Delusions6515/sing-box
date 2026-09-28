package configscript

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWrapGeneratedConfigErrorListsSourcePositionsWithoutAttribution(t *testing.T) {
	content := []byte(`/* @starlark
result = value
*/
{
  "value":
  /* @starlark
  result = value
  */
  1
}`)
	cause := errors.New("invalid generated field")
	wrapped := WrapGeneratedConfigError(content, cause)
	if !errors.Is(wrapped, cause) {
		t.Fatalf("wrapped error %v does not preserve its cause", wrapped)
	}
	if !strings.Contains(wrapped.Error(), "configuration generated from source containing @starlark comments at 1:1, 6:3") {
		t.Fatalf("wrapped error = %v, want all marked source positions", wrapped)
	}
	if strings.Contains(wrapped.Error(), "script caused") {
		t.Fatalf("wrapped error assigns causality to one script: %v", wrapped)
	}
}

func TestParseBindsMarkedCommentsToOriginalValues(t *testing.T) {
	source := []byte(`/* @starlark
result = value
*/
{
  "name": /* @starlark
result = value
*/ "test",
  "items": [
    /*
     * @starlark
     * result = value
     */
    {"name": "first"}
  ]
}`)

	parsed, err := Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(parsed.Scripts), 3; got != want {
		t.Fatalf("got %d marked scripts, want %d", got, want)
	}
	wantPaths := []string{"", "name", "items[0]"}
	for i, want := range wantPaths {
		if got := parsed.Scripts[i].Path.String(); got != want {
			t.Errorf("script %d path = %q, want %q", i, got, want)
		}
		if got := parsed.Scripts[i].Code; got != "result = value" {
			t.Errorf("script %d code = %q, want unmodified script body", i, got)
		}
	}
}

func TestParseAcceptsTrailingCommasInScriptedJSONC(t *testing.T) {
	content := []byte(`/* @starlark
result = value
*/ {
  "inbounds": [
    /* @starlark
    result = value
    */ {"type": "mixed", "tag": "local",},
  ],
  "route": {"auto_detect_interface": true,},
  "literal": ",}",
}`)
	parsed, err := Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Scripts) != 2 {
		t.Fatalf("got %d scripts, want 2", len(parsed.Scripts))
	}
	got, err := json.Marshal(parsed.Value)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"inbounds":[{"tag":"local","type":"mixed"}],"literal":",}","route":{"auto_detect_interface":true}}`; string(got) != want {
		t.Fatalf("parsed config = %s, want %s", got, want)
	}
	if _, err := Parse([]byte(`/* @starlark
result = value
*/ {"items": [1,,]}`)); err == nil {
		t.Fatal("malformed JSON was accepted as a trailing comma")
	}
}

func TestParseIgnoresOrdinaryCommentsAndStringMarkers(t *testing.T) {
	source := []byte(`// @starlark
/* ordinary @starlark note */ {"text":"@starlark"}`)
	parsed, err := Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Scripts) != 0 {
		t.Fatalf("ordinary comment or string marker was treated as a script: %#v", parsed.Scripts)
	}
}

func TestParsePreservesStarlarkOperatorsAndRejectsInternalMarkers(t *testing.T) {
	parsed, err := Parse([]byte(`{
  "value": 3,
  /* @starlark
  result = value * 2
  */
  "doubled": 6
}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Scripts[0].Code; got != "result = value * 2" {
		t.Fatalf("script body = %q, want multiplication operator preserved", got)
	}

	if _, err = Parse([]byte(`{"value": { /* @starlark
result = value
*/ }}`)); err == nil {
		t.Fatal("expected a marker inside a JSON object to be rejected")
	}
}

func TestHasScriptsOnlyRecognizesMarkedBlockComments(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
		want bool
	}{
		{name: "marker", text: "/* @starlark\nresult = value\n*/ {}", want: true},
		{name: "ordinary comment", text: `/* @starlark is documentation */ {}`, want: false},
		{name: "line comment", text: "// @starlark\n{}", want: false},
		{name: "string", text: `{"text":"/* @starlark */"}`, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := HasScripts([]byte(test.text))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("HasScripts() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestParseKeepsDistinctKeysWithAmbiguousDisplayPathsSeparate(t *testing.T) {
	parsed, err := Parse([]byte(`{
  "a.b": /* @starlark
result = value
*/ 1,
  "a": {"b": /* @starlark
result = value
*/ 2}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Scripts) != 2 {
		t.Fatalf("got %d scripts, want 2", len(parsed.Scripts))
	}
}

func TestParseBoundsScriptsButNotOrdinaryJSON(t *testing.T) {
	oversizedScript := "/* @starlark\n" + strings.Repeat("#", MaxScriptBytes+1) + "\n*/ {}"
	if _, err := Parse([]byte(oversizedScript)); err == nil {
		t.Fatal("expected oversized script to be rejected")
	}

	largeJSON := `{"padding":"` + strings.Repeat("x", MaxSourceBytes) + `"}`
	if _, err := Parse([]byte(largeJSON)); err != nil {
		t.Fatalf("ordinary JSON above the script input limit was rejected: %v", err)
	}
}

func TestParseRejectsAmbiguousMarkedComments(t *testing.T) {
	for name, source := range map[string]string{
		"trailing": `{"name":"x"} /* @starlark
result = value
*/`,
		"duplicate": `/* @starlark
result = value
*/ /* @starlark
result = value
*/ {"name":"x"}`,
		"duplicate-key": `/* @starlark
result = value
*/ {"name":"x","name":"y"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(source)); err == nil {
				t.Fatal("expected ambiguous or duplicate input to fail")
			}
		})
	}
}
