package configscript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

const (
	DefaultMaxExecutionSteps uint64 = 1_000_000
	DefaultMaxOutputBytes           = 4 << 20
	maxJSONDepth                    = 128
)

type Host struct {
	OS     string
	Arch   string
	Client string
}

type Limits struct {
	MaxSteps       uint64
	MaxOutputBytes int
}

type omittedValue struct{}

func (omittedValue) String() string        { return "omit" }
func (omittedValue) Type() string          { return "omit" }
func (omittedValue) Freeze()               {}
func (omittedValue) Truth() starlark.Bool  { return starlark.True }
func (omittedValue) Hash() (uint32, error) { return 0x6f6d6974, nil }

var omit = omittedValue{}

// JSON encoders differ in whether invalid UTF-8 is replaced by a literal rune
// or an escaped one. Match the encoder used to publish generated configuration.
var invalidUTF8JSONSize = func() int {
	encoded, _ := json.Marshal("\xff")
	return len(encoded) - 2
}()

// Generate renders marked source, leaving ordinary JSONC for the caller to decode.
func Generate(ctx context.Context, content []byte, host Host) ([]byte, bool, error) {
	hasScripts, err := HasScripts(content)
	if err != nil || !hasScripts {
		return nil, hasScripts, err
	}
	source, err := Parse(content)
	if err != nil {
		return nil, true, err
	}
	value, err := Evaluate(ctx, source, host, Limits{})
	if err != nil {
		return nil, true, err
	}
	generated, err := json.Marshal(value)
	if err != nil {
		return nil, true, fmt.Errorf("encode generated configuration: %w", err)
	}
	return generated, true, nil
}

// Evaluate transforms children before parents at their original JSON paths.
// Execution steps are shared; each result conversion and the final tree have
// independent byte limits. Scripts receive a copied value and build identity.
func Evaluate(ctx context.Context, source *Source, host Host, limits Limits) (any, error) {
	if source == nil {
		return nil, errors.New("missing configuration source")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	maxSteps := limits.MaxSteps
	if maxSteps == 0 {
		maxSteps = DefaultMaxExecutionSteps
	}
	maxOutput := limits.MaxOutputBytes
	if maxOutput == 0 {
		maxOutput = DefaultMaxOutputBytes
	}
	if maxSteps < 1 || maxOutput < 1 {
		return nil, errors.New("script execution limits must be positive")
	}

	scripts := make(map[string]Script, len(source.Scripts))
	for _, script := range source.Scripts {
		scripts[pathIdentity(script.Path)] = script
	}
	steps := uint64(0)
	value, omitted, err := transformValue(ctx, source.Value, nil, scripts, host, maxSteps, &steps, maxOutput)
	if err != nil {
		return nil, err
	}
	if omitted {
		return nil, errors.New("root configuration cannot be omitted")
	}
	if size, sizeErr := jsonValueSize(value, maxOutput); sizeErr != nil {
		return nil, sizeErr
	} else if size > maxOutput {
		return nil, fmt.Errorf("generated configuration exceeds %d-byte limit", maxOutput)
	}
	return value, nil
}

func transformValue(ctx context.Context, value any, path Path, scripts map[string]Script, host Host, maxSteps uint64, steps *uint64, maxOutput int) (any, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	switch original := value.(type) {
	case map[string]any:
		transformed := make(map[string]any, len(original))
		keys := make([]string, 0, len(original))
		for key := range original {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childPath := appendPath(path, PathSegment{Key: key})
			child, omitted, err := transformValue(ctx, original[key], childPath, scripts, host, maxSteps, steps, maxOutput)
			if err != nil {
				return nil, false, err
			}
			if !omitted {
				transformed[key] = child
			}
		}
		value = transformed
	case []any:
		transformed := make([]any, 0, len(original))
		for index, item := range original {
			childPath := appendPath(path, PathSegment{Index: index, Array: true})
			child, omitted, err := transformValue(ctx, item, childPath, scripts, host, maxSteps, steps, maxOutput)
			if err != nil {
				return nil, false, err
			}
			if !omitted {
				transformed = append(transformed, child)
			}
		}
		value = transformed
	}
	script, exists := scripts[pathIdentity(path)]
	if !exists {
		return value, false, nil
	}
	if *steps >= maxSteps {
		return nil, false, scriptError(script, "configuration Starlark execution exceeded the step limit")
	}
	remaining := maxSteps - *steps
	starlarkValue, err := toStarlark(value, 0)
	if err != nil {
		return nil, false, scriptError(script, "convert attached JSON value", err)
	}
	globals, executedSteps, err := runScript(ctx, script, starlarkValue, host, remaining)
	*steps += executedSteps
	if err != nil {
		return nil, false, scriptError(script, "execute Starlark", err)
	}
	result, exists := globals["result"]
	if !exists {
		return nil, false, scriptError(script, "script must assign an explicit result")
	}
	if _, isOmitted := result.(omittedValue); isOmitted {
		return nil, true, nil
	}
	outputBudget := maxOutput
	converted, err := toJSONValue(result, 0, make(map[uintptr]bool), &outputBudget)
	if err != nil {
		return nil, false, scriptError(script, "convert script result to JSON", err)
	}
	return converted, false, nil
}

func runScript(ctx context.Context, script Script, value starlark.Value, host Host, maxSteps uint64) (starlark.StringDict, uint64, error) {
	thread := &starlark.Thread{Name: "sing-box configuration"}
	thread.SetMaxExecutionSteps(maxSteps)
	predeclared := starlark.StringDict{
		"value": value,
		"host": starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			"os":     starlark.String(host.OS),
			"arch":   starlark.String(host.Arch),
			"client": starlark.String(host.Client),
		}),
		"omit": omit,
		"print": starlark.NewBuiltin("print", func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			return nil, errors.New("print is disabled in configuration scripts")
		}),
	}
	finished := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			thread.Cancel("configuration script canceled: " + ctx.Err().Error())
		case <-finished:
		}
	}()
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{
		GlobalReassign:  true,
		TopLevelControl: true,
	}, thread, "<config>", script.Code, predeclared)
	close(finished)
	<-watcherDone
	return globals, thread.ExecutionSteps(), err
}

func scriptError(script Script, message string, cause ...error) error {
	location := fmt.Sprintf("configuration script at %d:%d (%q): %s", script.Position.Line, script.Position.Column, script.Path.String(), message)
	if len(cause) != 0 && cause[0] != nil {
		return fmt.Errorf("%s: %w", location, cause[0])
	}
	return errors.New(location)
}

func toStarlark(value any, depth int) (starlark.Value, error) {
	if depth > maxJSONDepth {
		return nil, fmt.Errorf("JSON value exceeds %d nesting levels", maxJSONDepth)
	}
	switch value := value.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(value), nil
	case string:
		return starlark.String(value), nil
	case json.Number:
		if !strings.ContainsAny(string(value), ".eE") {
			integer, ok := new(big.Int).SetString(string(value), 10)
			if !ok {
				return nil, fmt.Errorf("invalid JSON integer %q", value)
			}
			return starlark.MakeBigInt(integer), nil
		}
		floating, err := strconv.ParseFloat(string(value), 64)
		if err != nil || math.IsInf(floating, 0) || math.IsNaN(floating) {
			return nil, fmt.Errorf("JSON number %q cannot be represented as a finite Starlark float", value)
		}
		return starlark.Float(floating), nil
	case map[string]any:
		dictionary := starlark.NewDict(len(value))
		for _, key := range sortedKeys(value) {
			item := value[key]
			converted, err := toStarlark(item, depth+1)
			if err != nil {
				return nil, err
			}
			if err = dictionary.SetKey(starlark.String(key), converted); err != nil {
				return nil, err
			}
		}
		return dictionary, nil
	case []any:
		list := make([]starlark.Value, len(value))
		for i, item := range value {
			converted, err := toStarlark(item, depth+1)
			if err != nil {
				return nil, err
			}
			list[i] = converted
		}
		return starlark.NewList(list), nil
	default:
		return nil, fmt.Errorf("unsupported JSON value %T", value)
	}
}

func toJSONValue(value starlark.Value, depth int, ancestors map[uintptr]bool, budget *int) (any, error) {
	if depth > maxJSONDepth {
		return nil, fmt.Errorf("Starlark result exceeds %d nesting levels", maxJSONDepth)
	}
	switch value := value.(type) {
	case starlark.NoneType:
		if err := consumeOutput(budget, 4); err != nil {
			return nil, err
		}
		return nil, nil
	case starlark.Bool:
		if value {
			if err := consumeOutput(budget, 4); err != nil {
				return nil, err
			}
			return true, nil
		}
		if err := consumeOutput(budget, 5); err != nil {
			return nil, err
		}
		return false, nil
	case starlark.String:
		text := string(value)
		if err := consumeOutput(budget, jsonStringSize(text)); err != nil {
			return nil, err
		}
		return text, nil
	case starlark.Int:
		number := value.String()
		if err := consumeOutput(budget, len(number)); err != nil {
			return nil, err
		}
		return json.Number(number), nil
	case starlark.Float:
		floating := float64(value)
		if math.IsInf(floating, 0) || math.IsNaN(floating) {
			return nil, errors.New("non-finite floating point value is not valid JSON")
		}
		number := strconv.FormatFloat(floating, 'g', -1, 64)
		if err := consumeOutput(budget, len(number)); err != nil {
			return nil, err
		}
		return json.Number(number), nil
	case *starlark.List:
		return convertSequence(value, value.Len(), value.Index, depth, ancestors, budget)
	case starlark.Tuple:
		return convertSequence(value, len(value), func(index int) starlark.Value { return value[index] }, depth, ancestors, budget)
	case *starlark.Dict:
		pointer := reflectPointer(value)
		if ancestors[pointer] {
			return nil, errors.New("cyclic Starlark value cannot be encoded as JSON")
		}
		ancestors[pointer] = true
		defer delete(ancestors, pointer)
		if err := consumeOutput(budget, 2); err != nil {
			return nil, err
		}
		object := make(map[string]any, value.Len())
		for i, item := range value.Items() {
			if i > 0 {
				if err := consumeOutput(budget, 1); err != nil {
					return nil, err
				}
			}
			key, ok := item[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("JSON object key must be a string, got %s", item[0].Type())
			}
			keyText := string(key)
			if _, exists := object[keyText]; exists {
				return nil, fmt.Errorf("duplicate JSON object key %q", keyText)
			}
			if err := consumeOutput(budget, jsonStringSize(keyText)+1); err != nil {
				return nil, err
			}
			converted, err := toJSONValue(item[1], depth+1, ancestors, budget)
			if err != nil {
				return nil, err
			}
			object[keyText] = converted
		}
		return object, nil
	case omittedValue:
		return nil, errors.New("omit is only valid as a script's top-level result")
	default:
		return nil, fmt.Errorf("Starlark value of type %s is not JSON-compatible", value.Type())
	}
}

func convertSequence(sequence any, length int, at func(int) starlark.Value, depth int, ancestors map[uintptr]bool, budget *int) ([]any, error) {
	pointer := reflectPointer(sequence)
	if ancestors[pointer] {
		return nil, errors.New("cyclic Starlark value cannot be encoded as JSON")
	}
	ancestors[pointer] = true
	defer delete(ancestors, pointer)
	if err := consumeOutput(budget, 2); err != nil {
		return nil, err
	}
	array := make([]any, 0, length)
	for i := 0; i < length; i++ {
		if i > 0 {
			if err := consumeOutput(budget, 1); err != nil {
				return nil, err
			}
		}
		item, err := toJSONValue(at(i), depth+1, ancestors, budget)
		if err != nil {
			return nil, err
		}
		array = append(array, item)
	}
	return array, nil
}

func reflectPointer(value any) uintptr {
	return reflect.ValueOf(value).Pointer()
}

func consumeOutput(budget *int, count int) error {
	if count > *budget {
		return errors.New("script outputs exceed the configured byte limit")
	}
	*budget -= count
	return nil
}

func jsonStringSize(text string) int {
	size := 2
	for len(text) > 0 {
		r, width := utf8.DecodeRuneInString(text)
		if r == utf8.RuneError && width == 1 {
			size += invalidUTF8JSONSize
			text = text[width:]
			continue
		}
		if r == '"' || r == '\\' || r == '\b' || r == '\f' || r == '\n' || r == '\r' || r == '\t' {
			size += 2
		} else if r < 0x20 || r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029' {
			size += 6
		} else {
			size += width
		}
		text = text[width:]
	}
	return size
}

func jsonValueSize(value any, max int) (int, error) {
	size := 0
	var add func(any, int) error
	add = func(value any, depth int) error {
		if depth > maxJSONDepth {
			return fmt.Errorf("generated configuration exceeds %d nesting levels", maxJSONDepth)
		}
		switch value := value.(type) {
		case nil:
			size += 4
		case bool:
			if value {
				size += 4
			} else {
				size += 5
			}
		case string:
			size += jsonStringSize(value)
		case json.Number:
			if !json.Valid([]byte(value)) {
				return fmt.Errorf("invalid JSON number %q", value)
			}
			size += len(value)
		case []any:
			size += 2
			for i, item := range value {
				if i > 0 {
					size++
				}
				if err := add(item, depth+1); err != nil {
					return err
				}
			}
		case map[string]any:
			size += 2
			for i, key := range sortedKeys(value) {
				if i > 0 {
					size++
				}
				size += jsonStringSize(key) + 1
				if err := add(value[key], depth+1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unsupported generated JSON value %T", value)
		}
		if size > max {
			return fmt.Errorf("generated configuration exceeds %d-byte limit", max)
		}
		return nil
	}
	if err := add(value, 0); err != nil {
		return size, err
	}
	return size, nil
}

func sortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
