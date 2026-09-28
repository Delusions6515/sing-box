package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/service/powerreport"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/test/bufconn"
)

type desktopConfigTestPlatform struct {
	daemonPlatform
	resetCount int
}

func (p *desktopConfigTestPlatform) PrepareOwner(peerIdentity) error { return nil }

func (p *desktopConfigTestPlatform) ResetPlatformOptions() error {
	p.resetCount++
	return nil
}

func (*desktopConfigTestPlatform) SetSystemProxyPreference(bool) {}

func TestDesktopStartServiceInvalidReloadKeepsRunningService(t *testing.T) {
	oldWorkingDirectory := workingDirectory
	workingDirectory = t.TempDir()
	t.Cleanup(func() { workingDirectory = oldWorkingDirectory })

	identity := peerIdentity{UserID: "script-test-user", SessionID: 7}
	ownerDirectory := userWorkingDirectory(identity.UserID)
	if err := os.MkdirAll(ownerDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	platform := new(desktopConfigTestPlatform)
	startedService := daemon.NewStartedService(daemon.ServiceOptions{
		Context:          include.Context(context.Background()),
		ConfigScriptHost: currentDesktopConfigScriptHost(),
	})
	defer startedService.Close()
	defer func() { _ = startedService.CloseService() }()
	d := &Daemon{
		ctx:                     include.Context(context.Background()),
		logger:                  log.StdLogger(),
		startedService:          startedService,
		powerManager:            powerreport.NewManager(),
		runtimeWorkingDirectory: ownerDirectory,
		platform:                platform,
	}
	service := &desktopService{daemon: d}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx = peer.NewContext(ctx, &peer.Peer{AuthInfo: &peerAuthInfo{identity: identity}})
		return handler(ctx, request)
	}))
	RegisterDesktopServiceServer(server, service)
	listener := bufconn.Listen(1024 * 1024)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	defer listener.Close()
	connection, err := grpc.NewClient(
		"passthrough:///desktop-script-reload",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	client := NewDesktopServiceClient(connection)

	initialConfig := fmt.Sprintf(`{"experimental":{"cache_file":{"path":%q}}}`, filepath.Join(t.TempDir(), "cache.db"))
	if _, err = client.StartService(context.Background(), &StartServiceRequest{ConfigContent: initialConfig}); err != nil {
		t.Fatalf("start initial service: %v", err)
	}
	oldInstance := startedService.Instance()
	if oldInstance == nil {
		t.Fatal("initial StartService did not retain a running instance")
	}
	initialResetCount := platform.resetCount
	initialOptions, err := loadStartOptions(identity.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if !initialOptions.WasRunning {
		t.Fatal("initial StartService did not persist WasRunning")
	}

	invalidConfig := `/* @starlark
result = {"inbounds": [{"type": "not-an-inbound"}]}
*/ {}`
	if _, err = client.StartService(context.Background(), &StartServiceRequest{ConfigContent: invalidConfig}); err == nil {
		t.Fatal("StartService accepted an invalid generated configuration")
	} else if !strings.Contains(err.Error(), "configuration generated from source containing @starlark comments at 1:1") {
		t.Fatalf("StartService generated-result error = %v, want script source context", err)
	}
	if current := startedService.Instance(); current != oldInstance {
		t.Fatalf("rejected reload replaced running instance: got %p, want %p", current, oldInstance)
	}
	if platform.resetCount != initialResetCount {
		t.Fatalf("platform reset count after rejected reload = %d, want unchanged %d", platform.resetCount, initialResetCount)
	}
	currentOptions, err := loadStartOptions(identity.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if !currentOptions.WasRunning {
		t.Fatal("rejected reload changed persisted WasRunning to false")
	}

	configPath := filepath.Join(ownerDirectory, serviceConfigFileName)
	if err = os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(configPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = client.StartService(context.Background(), &StartServiceRequest{ConfigContent: initialConfig}); err == nil {
		t.Fatal("StartService succeeded despite a config persistence failure")
	}
	if current := startedService.Instance(); current != nil {
		t.Fatalf("service remained active after config persistence failure: %p", current)
	}
	failedOptions, err := loadStartOptions(identity.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if failedOptions.WasRunning {
		t.Fatal("config persistence failure left WasRunning true")
	}
}

var _ adapter.PlatformInterface = (*desktopConfigTestPlatform)(nil)
