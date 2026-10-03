package configscript

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGenerateOnlyTransformsMarkedSource(t *testing.T) {
	ordinary, scripted, err := Generate(context.Background(), []byte(`{"value":1}`), Host{Client: "cli"})
	if err != nil || scripted || ordinary != nil {
		t.Fatalf("ordinary source generation = (%q, %t, %v), want (nil, false, nil)", ordinary, scripted, err)
	}
	generated, scripted, err := Generate(context.Background(), []byte(`/* @starlark
result = {"value": value["value"] + 1}
*/ {"value":1}`), Host{Client: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if !scripted || string(generated) != `{"value":2}` {
		t.Fatalf("generated result = (%s, %t), want {value:2}, true", generated, scripted)
	}
}

func TestEvaluateRunsChildrenBeforeParentAtOriginalPaths(t *testing.T) {
	parsed, err := Parse([]byte(`/* @starlark
result = {"entries": [value["entries"][1], value["entries"][0]]}
*/
{"entries":[
  /* @starlark
  value["name"] = "updated"
  result = value
  */
  {"name":"first"},
  {"name":"second"}
]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Evaluate(context.Background(), parsed, Host{OS: "linux", Arch: "amd64", Client: "cli"}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"entries":[{"name":"second"},{"name":"updated"}]}`; string(encoded) != want {
		t.Fatalf("generated value = %s, want %s", encoded, want)
	}
}

func TestEvaluateDistinguishesOmissionFromJSONNull(t *testing.T) {
	parsed, err := Parse([]byte(`[
/* @starlark
result = omit
*/ 1,
/* @starlark
result = None
*/ 2
]`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Evaluate(context.Background(), parsed, Host{}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	array, ok := got.([]any)
	if !ok || len(array) != 1 || array[0] != nil {
		t.Fatalf("generated value = %#v, want one JSON null item", got)
	}

	for name, source := range map[string]string{
		"missing result": `/* @starlark
value
*/ {}`,
		"root omission": `/* @starlark
result = omit
*/ {}`,
	} {
		t.Run(name, func(t *testing.T) {
			parsed, parseErr := Parse([]byte(source))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if _, evalErr := Evaluate(context.Background(), parsed, Host{}, Limits{}); evalErr == nil {
				t.Fatal("expected invalid script result to fail")
			}
		})
	}
}

func TestEvaluateRestrictsHostCapabilitiesAndExecution(t *testing.T) {
	for name, code := range map[string]string{
		"load":  `load("other.star", "value")`,
		"print": "print(\"not allowed\")\nresult = value",
		"env":   `result = env["HOME"]`,
		"file":  `result = open("/etc/passwd")`,
	} {
		t.Run(name, func(t *testing.T) {
			parsed, err := Parse([]byte("/* @starlark\n" + code + "\n*/ {}"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Evaluate(context.Background(), parsed, Host{}, Limits{}); err == nil {
				t.Fatal("expected unavailable capability to be rejected")
			}
		})
	}

	parsed, err := Parse([]byte(`/* @starlark
for i in range(10000):
  pass
result = value
*/ {}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Evaluate(context.Background(), parsed, Host{}, Limits{MaxSteps: 100}); err == nil {
		t.Fatal("expected the execution step limit to interrupt the script")
	}
}

func TestEvaluateEnforcesOutputLimitAndRejectsNonFiniteNumbers(t *testing.T) {
	parsed, err := Parse([]byte(`/* @starlark
result = "x" * 1024
*/ null`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Evaluate(context.Background(), parsed, Host{}, Limits{MaxOutputBytes: 100}); err == nil {
		t.Fatal("expected oversized output to fail")
	}

	parsed, err = Parse([]byte(`/* @starlark
result = float("inf")
*/ null`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Evaluate(context.Background(), parsed, Host{}, Limits{}); err == nil || !strings.Contains(err.Error(), "non-finite") {
		t.Fatalf("expected non-finite result error, got %v", err)
	}
}

func TestEvaluateUsesIndependentConversionBudgets(t *testing.T) {
	parsed, err := Parse([]byte(`/* @starlark
result = value
*/ {
/* @starlark
result = value
*/ "parent": {
/* @starlark
result = value
*/ "child": "payload"}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"parent":{"child":"payload"}}`
	got, err := Evaluate(context.Background(), parsed, Host{}, Limits{MaxOutputBytes: len(want)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil || string(encoded) != want {
		t.Fatalf("generated value = %s, %v; want %s", encoded, err, want)
	}
}

func TestEvaluateChecksFinalAggregateAndEachResult(t *testing.T) {
	for name, source := range map[string]string{
		"final aggregate": `{"a": /* @starlark
result = "12345678"
*/ null, "b": /* @starlark
result = "12345678"
*/ null}`,
		"oversized child before parent pruning": `/* @starlark
result = {}
*/ {"a": /* @starlark
result = "x" * 32
*/ null}`,
	} {
		t.Run(name, func(t *testing.T) {
			parsed, err := Parse([]byte(source))
			if err != nil {
				t.Fatal(err)
			}
			_, err = Evaluate(context.Background(), parsed, Host{}, Limits{MaxOutputBytes: 24})
			if err == nil {
				t.Fatal("expected output limit error")
			}
			if name == "oversized child before parent pruning" && !strings.Contains(err.Error(), "3:10") {
				t.Fatalf("expected child's source position, got %v", err)
			}
		})
	}
}

func TestOutputStringSizeMatchesJSONEncoding(t *testing.T) {
	for _, text := range []string{"plain", "\"\\\b\f\n\r\t\x00", "<>&\u2028\u2029", "中文", "\xff\xfe", "\ufffd"} {
		encoded, err := json.Marshal(text)
		if err != nil {
			t.Fatal(err)
		}
		if got := jsonStringSize(text); got != len(encoded) {
			t.Errorf("jsonStringSize(%q) = %d, want encoded size %d", text, got, len(encoded))
		}
		parsed, err := Parse([]byte("/* @starlark\nresult = value\n*/ null"))
		if err != nil {
			t.Fatal(err)
		}
		// Starlark strings can contain arbitrary bytes, unlike decoded JSON text.
		parsed.Value = text
		for _, limit := range []int{len(encoded), len(encoded) - 1} {
			got, evalErr := Evaluate(context.Background(), parsed, Host{}, Limits{MaxOutputBytes: limit})
			if limit < len(encoded) {
				if evalErr == nil {
					t.Errorf("text %q accepted one byte below its encoded size", text)
				}
			} else if evalErr != nil {
				t.Errorf("text %q rejected at its encoded size: %v", text, evalErr)
			} else if actual, marshalErr := json.Marshal(got); marshalErr != nil || string(actual) != string(encoded) {
				t.Errorf("generated string = %q, %v; want %q", actual, marshalErr, encoded)
			}
		}
	}
}

func TestEvaluateAccumulatesStepsAcrossScripts(t *testing.T) {
	code := "/* @starlark\nfor i in range(100):\n  pass\nresult = value\n*/ null"
	for _, source := range []string{code, "[\n" + code + ",\n" + code + ",\n" + code + "\n]"} {
		parsed, err := Parse([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		_, err = Evaluate(context.Background(), parsed, Host{}, Limits{MaxSteps: 1000})
		if source == code && err != nil {
			t.Fatalf("individual script exceeded budget: %v", err)
		}
		if source != code && (err == nil || !strings.Contains(err.Error(), "step")) {
			t.Fatalf("expected cumulative step limit error, got %v", err)
		}
	}
}

func TestEvaluateRejectsInvalidJSONValuesAndHostMutation(t *testing.T) {
	for name, code := range map[string]string{
		"non-string key": `result = {1: "value"}`,
		"cycle":          "value = []\nvalue.append(value)\nresult = value",
		"host mutation":  "host.os = value\nresult = value",
	} {
		t.Run(name, func(t *testing.T) {
			parsed, err := Parse([]byte("/* @starlark\n" + code + "\n*/ {}"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Evaluate(context.Background(), parsed, Host{OS: "linux"}, Limits{}); err == nil {
				t.Fatal("expected invalid value or host mutation to fail")
			}
		})
	}
}

func TestEvaluateCancelsRunningScript(t *testing.T) {
	parsed, err := Parse([]byte(`/* @starlark
for i in range(1000000000):
  pass
result = value
*/ {}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Evaluate(ctx, parsed, Host{}, Limits{MaxSteps: 10_000_000_000})
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err = <-done:
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("expected cancellation error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("script did not stop promptly after cancellation")
	}
}

func TestEvaluateMeasuresBoundedStringAmplification(t *testing.T) {
	parsed, err := Parse([]byte(`/* @starlark
result = "x" * 16777216
*/ null`))
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err = Evaluate(context.Background(), parsed, Host{}, Limits{MaxOutputBytes: 128}); err == nil {
		t.Fatal("expected expanded string to exceed the output limit")
	}
	runtime.ReadMemStats(&after)
	t.Logf("bounded 16 MiB string amplification allocated %d bytes", after.TotalAlloc-before.TotalAlloc)
	if status, readErr := os.ReadFile("/proc/self/status"); readErr == nil {
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "VmHWM:") {
				t.Logf("process peak RSS: %s", strings.TrimSpace(strings.TrimPrefix(line, "VmHWM:")))
			}
		}
	}
}

func TestEvaluateProvidesReadOnlyBuildIdentityAndExactIntegers(t *testing.T) {
	parsed, err := Parse([]byte(`/* @starlark
result = {"client": host.client, "os": host.os, "arch": host.arch, "large": value["large"] + 1}
*/ {"large": 123456789012345678901234567890}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Evaluate(context.Background(), parsed, Host{OS: "darwin", Arch: "arm64", Client: "sfm"}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"arch":"arm64","client":"sfm","large":123456789012345678901234567891,"os":"darwin"}`; string(encoded) != want {
		t.Fatalf("generated value = %s, want %s", encoded, want)
	}
}
