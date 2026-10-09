package configscript

import (
	"context"
	"strings"
	"testing"

	"github.com/sagernet/sing/common/json"
)

// This receiver delegates JSONC syntax and comment scanning to the dependency,
// independently of sourceDocument and the production detection lexer.
type commentOracle struct {
	comments *json.CommentSet
}

func (*commentOracle) UnmarshalJSONContext(ctx context.Context, content []byte) error {
	var value any
	return json.UnmarshalContext(ctx, content, &value)
}

func (o *commentOracle) Comments() *json.CommentSet { return o.comments }
func (o *commentOracle) SetComments(comments *json.CommentSet) {
	o.comments = comments
}

func dependencyHasMarkedComment(content []byte) (bool, error) {
	oracle, err := json.UnmarshalExtendedContext[commentOracle](context.Background(), content)
	if err != nil {
		return false, err
	}
	if oracle.comments != nil {
		for _, comment := range oracle.comments.Comments {
			if comment.Kind != json.CommentKindBlock {
				continue
			}
			// The first nonblank line must contain only the marker, optionally
			// preceded by the documented ASCII star decoration.
			for _, line := range strings.Split(comment.Text, "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				line = strings.TrimLeft(line, " \t")
				if strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "*\t") || line == "*" {
					line = line[1:]
				}
				if strings.TrimSpace(line) == "@starlark" {
					return true, nil
				}
				break
			}
		}
	}
	return false, nil
}

var detectionSeeds = []struct {
	content string
	marked  bool
}{
	{`/* @starlark
result = value
*/ {}`, true},
	{" \r\n/*\r\n * @starlark\r\n * result = value\r\n */ {}", true},
	{`{"key": /* @starlark
result = value
*/ 1}`, true},
	{`[
/* @starlark
result = value
*/ 1,]`, true},
	{`{"text":"/* @starlark */","escaped":"\\\"//"}`, false},
	{"// @starlark\r/* @starlark\nresult = value\n*/ {}", true},
	{"# @starlark\r\n{}", false},
	{`/* ordinary @starlark note */ {}`, false},
	{`/* @starlark is documentation */ {}`, false},
	{`{"key":1} /* @starlark
result = value
*/`, true}, // Detection does not imply valid script attachment.
	{`{"key": {/* @starlark
result = value
*/}}`, true},
	{`/* @starlark
result = value
*/ {"items":[1, /* ordinary */ // before closing
], # before closing
}`, true},
}

func TestHasScriptsMatchesDependencyComments(t *testing.T) {
	for _, seed := range detectionSeeds {
		want, err := dependencyHasMarkedComment([]byte(seed.content))
		if err != nil {
			t.Fatalf("oracle rejected regression seed %q: %v", seed.content, err)
		}
		if want != seed.marked {
			t.Fatalf("oracle detection for %q = %t, want %t", seed.content, want, seed.marked)
		}
		got, err := HasScripts([]byte(seed.content))
		if err != nil || got != want {
			t.Fatalf("HasScripts(%q) = %t, %v; dependency comments imply %t", seed.content, got, err, want)
		}
	}
}

func FuzzHasScriptsMatchesJSONCComments(f *testing.F) {
	for _, seed := range detectionSeeds {
		f.Add([]byte(seed.content))
	}
	f.Fuzz(func(t *testing.T, content []byte) {
		if len(content) > 8<<10 {
			t.Skip()
		}
		want, err := dependencyHasMarkedComment(content)
		if err != nil {
			return // Starlark adds attachment rules; compare only valid JSONC.
		}
		got, err := HasScripts(content)
		if err != nil || got != want {
			t.Fatalf("detection = %t, %v; dependency comments imply %t", got, err, want)
		}
	})
}

func FuzzParseDoesNotPanic(f *testing.F) {
	for _, seed := range detectionSeeds {
		f.Add([]byte(seed.content))
	}
	for _, content := range []string{"", "/*", `{"items":[1,,]}`, `"\\`, "\xff\x00", strings.Repeat("[", 256)} {
		f.Add([]byte(content))
	}
	f.Fuzz(func(t *testing.T, content []byte) {
		if len(content) > 8<<10 {
			t.Skip()
		}
		// Never evaluate fuzzed scripts: only source parsing is under test.
		_, _ = Parse(content)
	})
}
