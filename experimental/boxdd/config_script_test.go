package main

import (
	"runtime"
	"testing"
)

func TestDesktopConfigScriptIdentity(t *testing.T) {
	host := currentDesktopConfigScriptHost()
	if host.OS != runtime.GOOS || host.Arch != runtime.GOARCH || host.Client != "desktop" {
		t.Fatalf("desktop script identity = %#v, want %s/%s desktop", host, runtime.GOOS, runtime.GOARCH)
	}
}
