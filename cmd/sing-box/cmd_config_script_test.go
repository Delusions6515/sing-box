package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/include"
)

const cliScriptConfig = `/* @starlark
if host.client == "cli":
  result = {"log": {"level": "debug"}}
else:
  result = {"log": {"level": "info"}}
*/
{"log":{"level":"warn"}}`

func setConfigInput(t *testing.T, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPaths, oldDirectories, oldContext := configPaths, configDirectories, globalCtx
	configPaths = []string{path}
	configDirectories = nil
	globalCtx = context.Background()
	t.Cleanup(func() {
		configPaths, configDirectories, globalCtx = oldPaths, oldDirectories, oldContext
	})
	return path
}

func TestCLIReadAndCheckReturnsValidatedSnapshot(t *testing.T) {
	path := setConfigInput(t, cliScriptConfig)
	globalCtx = include.Context(globalCtx)
	candidate, _, err := readConfigAndCheck()
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Log == nil || candidate.Log.Level != "debug" {
		t.Fatalf("checked candidate level = %#v, want generated debug", candidate.Log)
	}
	if err = os.WriteFile(path, []byte(`{"log":{"level":"error"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if candidate.Log.Level != "debug" {
		t.Fatalf("checked candidate changed after source update: level = %q", candidate.Log.Level)
	}
}

func TestCLIRenderRejectsInvalidGeneratedConfiguration(t *testing.T) {
	setConfigInput(t, `/* @starlark
result = {"log": {"level": "bogus"}}
*/ {}`)
	globalCtx = include.Context(globalCtx)
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	renderErr := render()
	writerCloseErr := writer.Close()
	os.Stdout = oldStdout
	if writerCloseErr != nil {
		t.Fatal(writerCloseErr)
	}
	output, readErr := io.ReadAll(reader)
	readerCloseErr := reader.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if readerCloseErr != nil {
		t.Fatal(readerCloseErr)
	}
	if renderErr == nil || !strings.Contains(renderErr.Error(), "bogus") || !strings.Contains(renderErr.Error(), "configuration generated from source containing @starlark comments at 1:1") {
		t.Fatalf("render error = %v, want generated log-level validation error with script context", renderErr)
	}
	if len(output) != 0 {
		t.Fatalf("render printed an invalid configuration: %s", output)
	}
}

func TestCLIConfigReadersAndRenderUseGeneratedCLIValue(t *testing.T) {
	path := setConfigInput(t, cliScriptConfig)
	globalCtx = include.Context(globalCtx)
	entry, err := readConfigAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if entry.options.Log == nil || entry.options.Log.Level != "debug" {
		t.Fatalf("readConfigAt log options = %#v, want CLI-generated debug", entry.options.Log)
	}
	merged, err := readConfigAndMerge()
	if err != nil {
		t.Fatal(err)
	}
	if merged.Log == nil || merged.Log.Level != "debug" {
		t.Fatalf("merged log options = %#v, want CLI-generated debug", merged.Log)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	renderErr := render()
	writerCloseErr := writer.Close()
	os.Stdout = oldStdout
	if writerCloseErr != nil {
		t.Fatal(writerCloseErr)
	}
	output, readErr := io.ReadAll(reader)
	readerCloseErr := reader.Close()
	if renderErr != nil {
		t.Fatal(renderErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	if readerCloseErr != nil {
		t.Fatal(readerCloseErr)
	}
	var rendered map[string]any
	if err = json.Unmarshal(output, &rendered); err != nil {
		t.Fatalf("render output is not JSON: %v (%s)", err, output)
	}
	if got := rendered["log"].(map[string]any)["level"]; got != "debug" {
		t.Fatalf("rendered log level = %v, want debug", got)
	}
}

func TestCLIFormatRefusesToRewriteScriptSources(t *testing.T) {
	path := setConfigInput(t, cliScriptConfig)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oldWrite := commandFormatFlagWrite
	t.Cleanup(func() { commandFormatFlagWrite = oldWrite })
	for _, write := range []bool{false, true} {
		commandFormatFlagWrite = write
		err = format()
		if err == nil || !strings.Contains(err.Error(), "Starlark") {
			t.Fatalf("format(write=%t) error = %v, want an explicit Starlark-source refusal", write, err)
		}
		after, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(after) != string(before) {
			t.Fatalf("format(write=%t) changed the source", write)
		}
	}
}

func TestCLIMergeWritesGeneratedJSONNotScriptSource(t *testing.T) {
	setConfigInput(t, cliScriptConfig)
	outputPath := filepath.Join(t.TempDir(), "merged.json")
	if err := merge(outputPath); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output), "@starlark") {
		t.Fatalf("merged output contains an executable script: %s", output)
	}
	var merged map[string]any
	if err = json.Unmarshal(output, &merged); err != nil {
		t.Fatal(err)
	}
	if got := merged["log"].(map[string]any)["level"]; got != "debug" {
		t.Fatalf("merged log level = %v, want debug", got)
	}
}

func TestCLIDirectoryMergeGeneratesScriptedFilesIndividually(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "01.json"), []byte(cliScriptConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "02.json"), []byte(`{"log":{"timestamp":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPaths, oldDirectories, oldContext := configPaths, configDirectories, globalCtx
	configPaths = nil
	configDirectories = []string{directory}
	globalCtx = context.Background()
	t.Cleanup(func() {
		configPaths, configDirectories, globalCtx = oldPaths, oldDirectories, oldContext
	})
	merged, err := readConfigAndMerge()
	if err != nil {
		t.Fatal(err)
	}
	if merged.Log == nil || merged.Log.Level != "debug" || !merged.Log.Timestamp {
		t.Fatalf("merged directory log options = %#v, want script and ordinary fields", merged.Log)
	}
}

func TestCLIFormatStillFormatsOrdinaryJSON(t *testing.T) {
	path := setConfigInput(t, `{"log":{"level":"debug"}}`)
	oldWrite := commandFormatFlagWrite
	commandFormatFlagWrite = true
	t.Cleanup(func() { commandFormatFlagWrite = oldWrite })
	if err := format(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), `"log": {`) {
		t.Fatalf("ordinary config was not formatted: %s", content)
	}
}

func TestCLIRejectsInvalidGeneratedOptionsWithSourceLocation(t *testing.T) {
	path := setConfigInput(t, `/* @starlark
result = {"not_a_config_field": True}
*/ {}`)
	_, err := readConfigAt(path)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "configuration generated from source containing @starlark comments at 1:1") {
		t.Fatalf("read error = %v, want file and non-attributing script source context", err)
	}
}
