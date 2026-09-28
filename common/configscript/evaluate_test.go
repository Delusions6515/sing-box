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
