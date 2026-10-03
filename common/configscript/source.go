package configscript

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sagernet/sing/common/json"
)

const (
	MaxSourceBytes = 4 << 20
	MaxScriptBytes = 256 << 10
)

type PathSegment struct {
	Key   string
	Index int
	Array bool
}

type Path []PathSegment

func (p Path) String() string {
	var result strings.Builder
	for _, segment := range p {
		if segment.Array {
			fmt.Fprintf(&result, "[%d]", segment.Index)
		} else {
			if result.Len() > 0 {
				result.WriteByte('.')
			}
			result.WriteString(segment.Key)
		}
	}
	return result.String()
}

type Position struct {
	Line   int
	Column int
}

type Script struct {
	Path     Path
	Code     string
	Position Position
}

type Source struct {
	Value   any
	Scripts []Script
}

type sourceDocument struct {
	value    any
	comments *json.CommentSet
	dups     []duplicateKey
	spans    []valueSpan
}

type valueSpan struct {
	path  Path
	start int64
	end   int64
}

type duplicateKey struct {
	path   Path
	key    string
	offset int64
}

func (d *sourceDocument) UnmarshalJSONContext(_ context.Context, content []byte) error {
	// The surrounding JSONC decoder accepts trailing commas, but encoding/json
	// does not. Blank them without shifting the offsets used to bind comments.
	content = blankTrailingCommas(content)
	decoder := stdjson.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	value, err := decodeValue(decoder, content, nil, &d.dups, &d.spans)
	if err != nil {
		return err
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	d.value = value
	return nil
}

func blankTrailingCommas(content []byte) []byte {
	var normalized []byte
	for i := 0; i < len(content); i++ {
		if content[i] == '"' {
			i = skipJSONString(content, i) - 1
			continue
		}
		if content[i] != ',' {
			continue
		}
		j := i + 1
		for j < len(content) && (content[j] == ' ' || content[j] == '\t' || content[j] == '\n' || content[j] == '\r') {
			j++
		}
		if j < len(content) && (content[j] == '}' || content[j] == ']') {
			if normalized == nil {
				normalized = bytes.Clone(content)
			}
			normalized[i] = ' '
		}
	}
	if normalized != nil {
		return normalized
	}
	return content
}

func (d *sourceDocument) Comments() *json.CommentSet {
	return d.comments
}

func (d *sourceDocument) SetComments(comments *json.CommentSet) {
	d.comments = comments
}

func HasScripts(content []byte) (bool, error) {
	for i := 0; i < len(content); {
		switch content[i] {
		case '"':
			i = skipJSONString(content, i)
		case '#':
			i = skipLineComment(content, i+1)
		case '/':
			if i+1 >= len(content) {
				i++
				continue
			}
			switch content[i+1] {
			case '/':
				i = skipLineComment(content, i+2)
			case '*':
				start := i + 2
				i = start
				for i+1 < len(content) && (content[i] != '*' || content[i+1] != '/') {
					i++
				}
				if i+1 >= len(content) {
					position := offsetPosition(content, int64(start-2))
					return false, fmt.Errorf("unterminated JSON block comment at %d:%d", position.Line, position.Column)
				}
				if _, marked := scriptFromComment(string(content[start:i])); marked {
					return true, nil
				}
				i += 2
			default:
				i++
			}
		default:
			i++
		}
	}
	return false, nil
}

func Parse(content []byte) (*Source, error) {
	hasScripts, err := HasScripts(content)
	if err != nil {
		return nil, err
	}
	if hasScripts && len(content) > MaxSourceBytes {
		return nil, fmt.Errorf("configuration exceeds %d-byte limit", MaxSourceBytes)
	}
	document, err := json.UnmarshalExtendedContext[sourceDocument](context.Background(), content)
	if err != nil {
		return nil, err
	}
	source := &Source{Value: document.value}
	if document.comments == nil {
		return source, nil
	}
	rootStart := sourceValueOffset(content, document.comments)
	seen := make(map[string]Position)
	for _, comment := range document.comments.Comments {
		if comment.Kind != json.CommentKindBlock {
			continue
		}
		code, marked := scriptFromComment(comment.Text)
		if !marked {
			continue
		}
		position := Position{Line: comment.Start.Line, Column: comment.Start.Column}
		if comment.Placement != json.CommentPlacementLeading && comment.Placement != json.CommentPlacementInner {
			return nil, sourceError(position, "@starlark comment must be attached to a JSON value")
		}
		path := make(Path, len(comment.Path))
		for i, segment := range comment.Path {
			path[i] = PathSegment{Key: segment.Key, Index: segment.Index, Array: segment.Kind == json.CommentPathIndex}
		}
		if _, ok := valueAt(document.value, path); !ok {
			return nil, sourceError(position, "@starlark comment is not attached to a JSON value")
		}
		if comment.Placement == json.CommentPlacementInner && !commentPrecedesValue(document.spans, path, int64(comment.End.Offset)-rootStart) {
			return nil, sourceError(position, "@starlark comment is inside a JSON value")
		}
		if len(code) > MaxScriptBytes {
			return nil, sourceError(position, fmt.Sprintf("Starlark script exceeds %d-byte limit", MaxScriptBytes))
		}
		key := pathIdentity(path)
		if previous, exists := seen[key]; exists {
			return nil, sourceError(position, fmt.Sprintf("multiple @starlark comments attach to %q (first at %d:%d)", path.String(), previous.Line, previous.Column))
		}
		seen[key] = position
		source.Scripts = append(source.Scripts, Script{Path: path, Code: code, Position: position})
	}
	if len(source.Scripts) > 0 {
		for _, duplicate := range document.dups {
			for _, script := range source.Scripts {
				if pathHasPrefix(duplicate.path, script.Path) {
					position := offsetPosition(content, rootStart+duplicate.offset)
					return nil, sourceError(position, fmt.Sprintf("duplicate JSON key %q in @starlark scope", duplicate.key))
				}
			}
		}
	}
	return source, nil
}

// WrapGeneratedConfigError adds every marked source position as context without
// attributing a later decode or validation error to a particular script.
func WrapGeneratedConfigError(content []byte, cause error) error {
	if cause == nil {
		return nil
	}
	source, err := Parse(content)
	if err != nil || len(source.Scripts) == 0 {
		return cause
	}
	const maxPositions = 8
	positions := make([]string, 0, min(len(source.Scripts), maxPositions)+1)
	for index, script := range source.Scripts {
		if index == maxPositions {
			positions = append(positions, fmt.Sprintf("and %d more", len(source.Scripts)-maxPositions))
			break
		}
		positions = append(positions, fmt.Sprintf("%d:%d", script.Position.Line, script.Position.Column))
	}
	return fmt.Errorf("configuration generated from source containing @starlark comments at %s: %w", strings.Join(positions, ", "), cause)
}

func decodeValue(decoder *stdjson.Decoder, content []byte, path Path, duplicates *[]duplicateKey, spans *[]valueSpan) (any, error) {
	start := skipJSONSeparators(content, decoder.InputOffset())
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(stdjson.Delim)
	if !isDelimiter {
		*spans = append(*spans, valueSpan{path: clonePath(path), start: start, end: decoder.InputOffset()})
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return nil, keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("expected JSON object key, got %T", keyToken)
			}
			if _, exists := keys[key]; exists {
				*duplicates = append(*duplicates, duplicateKey{path: clonePath(path), key: key, offset: decoder.InputOffset()})
			}
			keys[key] = struct{}{}
			childPath := appendPath(path, PathSegment{Key: key})
			value, valueErr := decodeValue(decoder, content, childPath, duplicates, spans)
			if valueErr != nil {
				return nil, valueErr
			}
			object[key] = value
		}
		if _, err = decoder.Token(); err != nil {
			return nil, err
		}
		*spans = append(*spans, valueSpan{path: clonePath(path), start: start, end: decoder.InputOffset()})
		return object, nil
	case '[':
		var array []any
		for decoder.More() {
			childPath := appendPath(path, PathSegment{Index: len(array), Array: true})
			value, valueErr := decodeValue(decoder, content, childPath, duplicates, spans)
			if valueErr != nil {
				return nil, valueErr
			}
			array = append(array, value)
		}
		if _, err = decoder.Token(); err != nil {
			return nil, err
		}
		*spans = append(*spans, valueSpan{path: clonePath(path), start: start, end: decoder.InputOffset()})
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func scriptFromComment(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	markerLine := -1
	for i, line := range lines {
		if strings.TrimSpace(stripStarDecoration(line)) == "@starlark" {
			markerLine = i
			break
		}
		if strings.TrimSpace(line) != "" {
			return "", false
		}
	}
	if markerLine < 0 {
		return "", false
	}
	body := lines[markerLine+1:]
	decorated := false
	nonEmpty := 0
	allDecorated := true
	for _, line := range body {
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonEmpty++
		if _, ok := starDecoration(line); ok {
			decorated = true
		} else {
			allDecorated = false
		}
	}
	if !allDecorated {
		decorated = false
	}
	if decorated && nonEmpty > 0 {
		for i, line := range body {
			if stripped, ok := starDecoration(line); ok {
				body[i] = stripped
			}
		}
	}
	for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		body = body[1:]
	}
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	return strings.Join(dedent(body), "\n"), true
}

func stripStarDecoration(line string) string {
	stripped, ok := starDecoration(line)
	if ok {
		return stripped
	}
	return line
}

func starDecoration(line string) (string, bool) {
	indent := len(line) - len(strings.TrimLeft(line, " \t"))
	content := line[indent:]
	if len(content) == 0 || content[0] != '*' || (len(content) > 1 && content[1] != ' ' && content[1] != '\t') {
		return "", false
	}
	content = content[1:]
	if len(content) > 0 && (content[0] == ' ' || content[0] == '\t') {
		content = content[1:]
	}
	return content, true
}

func dedent(lines []string) []string {
	indent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		count := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent < 0 || count < indent {
			indent = count
		}
	}
	if indent <= 0 {
		return lines
	}
	result := make([]string, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			result[i] = ""
		} else {
			result[i] = line[indent:]
		}
	}
	return result
}

func sourceError(position Position, message string) error {
	return fmt.Errorf("%d:%d: %s", position.Line, position.Column, message)
}

func sourceValueOffset(content []byte, comments *json.CommentSet) int64 {
	clean := append([]byte(nil), content...)
	if comments != nil {
		for _, comment := range comments.Comments {
			start, end := comment.Start.Offset, comment.End.Offset
			if start < 0 {
				start = 0
			}
			if end > len(clean) {
				end = len(clean)
			}
			for i := start; i < end; i++ {
				if clean[i] != '\n' && clean[i] != '\r' {
					clean[i] = ' '
				}
			}
		}
	}
	for i, char := range clean {
		if char != ' ' && char != '\t' && char != '\n' && char != '\r' {
			return int64(i)
		}
	}
	return int64(len(clean))
}

func offsetPosition(content []byte, offset int64) Position {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(content)) {
		offset = int64(len(content))
	}
	prefix := content[:offset]
	line := bytes.Count(prefix, []byte{'\n'}) + 1
	column := len(prefix) - bytes.LastIndex(prefix, []byte{'\n'})
	return Position{Line: line, Column: column}
}

func valueAt(root any, path Path) (any, bool) {
	value := root
	for _, segment := range path {
		if segment.Array {
			array, ok := value.([]any)
			if !ok || segment.Index < 0 || segment.Index >= len(array) {
				return nil, false
			}
			value = array[segment.Index]
		} else {
			object, ok := value.(map[string]any)
			if !ok {
				return nil, false
			}
			var exists bool
			value, exists = object[segment.Key]
			if !exists {
				return nil, false
			}
		}
	}
	return value, true
}

func pathHasPrefix(path, prefix Path) bool {
	if len(path) < len(prefix) {
		return false
	}
	for i := range prefix {
		if path[i] != prefix[i] {
			return false
		}
	}
	return true
}

func commentPrecedesValue(spans []valueSpan, path Path, commentEnd int64) bool {
	for _, span := range spans {
		if pathEqual(span.path, path) && span.start >= commentEnd {
			return true
		}
	}
	return false
}

func pathEqual(left, right Path) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func skipJSONSeparators(content []byte, offset int64) int64 {
	for offset < int64(len(content)) {
		switch content[offset] {
		case ' ', '\t', '\n', '\r', ',', ':':
			offset++
		default:
			return offset
		}
	}
	return offset
}

func appendPath(path Path, segment PathSegment) Path {
	return append(clonePath(path), segment)
}

func clonePath(path Path) Path {
	return append(Path(nil), path...)
}

func pathIdentity(path Path) string {
	var identity strings.Builder
	for _, segment := range path {
		if segment.Array {
			fmt.Fprintf(&identity, "i%d;", segment.Index)
		} else {
			fmt.Fprintf(&identity, "k%d:", len(segment.Key))
			identity.WriteString(segment.Key)
		}
	}
	return identity.String()
}

func skipJSONString(content []byte, start int) int {
	for i := start + 1; i < len(content); i++ {
		if content[i] == '\\' {
			i++
			continue
		}
		if content[i] == '"' {
			return i + 1
		}
	}
	return len(content)
}

func skipLineComment(content []byte, start int) int {
	for start < len(content) && content[start] != '\n' && content[start] != '\r' {
		start++
	}
	return start
}
