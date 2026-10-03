package daemon_test

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/common/configscript"
	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/include"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestFailedScriptReloadPreservesStartedService(t *testing.T) {
	ctx := include.Context(context.Background())
	startedService := daemon.NewStartedService(daemon.ServiceOptions{
		Context:          ctx,
		ConfigScriptHost: configscript.Host{OS: "linux", Arch: "amd64", Client: "desktop"},
	})
	defer startedService.Close()
	defer func() { _ = startedService.CloseService() }()
	initialConfig := fmt.Sprintf(`{"experimental":{"cache_file":{"path":%q}}}`, filepath.Join(t.TempDir(), "cache.db"))
	if err := startedService.StartOrReloadService(context.Background(), initialConfig, nil); err != nil {
		t.Fatalf("start initial service: %v", err)
	}
	oldInstance := startedService.Instance()

	server := daemon.NewServer(startedService, "")
	listener := bufconn.Listen(1024 * 1024)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	defer listener.Close()
	connection, err := grpc.NewClient(
		"passthrough:///sing-box-config-script-reload",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	client := daemon.NewStartedServiceClient(connection)

	source := `/* @starlark
if host.client == "desktop":
  result = {"inbounds": [{"type": "not-an-inbound"}]}
else:
  result = {}
*/ {}`
	if err = startedService.StartOrReloadService(context.Background(), source, nil); err == nil {
		t.Fatal("reload with invalid generated config unexpectedly succeeded")
	}
	if instance := startedService.Instance(); instance != oldInstance {
		t.Fatalf("failed preflight replaced the running instance: got %p, want %p", instance, oldInstance)
	}
	stream, err := client.SubscribeServiceStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	status, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != daemon.ServiceStatus_STARTED {
		t.Fatalf("service status after rejected reload = %s, want STARTED", status.Status)
	}
}

func TestStartedServiceUsesItsConfiguredScriptIdentity(t *testing.T) {
	host := configscript.Host{OS: "linux", Arch: "amd64", Client: "desktop"}
	ctx := include.Context(context.Background())
	service := daemon.NewStartedService(daemon.ServiceOptions{
		Context:          ctx,
		ConfigScriptHost: host,
	})
	defer service.Close()

	source := `/* @starlark
if host.client == "desktop":
  result = {}
else:
  result = {"unknown_target": True}
*/ {}`
	if err := service.CheckConfig(context.Background(), source); err != nil {
		t.Fatalf("CheckConfig rejected configured target branch: %v", err)
	}
	invalidSource := `/* @starlark
result = {"not_a_config_field": True}
*/ {}`
	if err := service.CheckConfig(context.Background(), invalidSource); err == nil || !strings.Contains(err.Error(), "configuration generated from source containing @starlark comments at 1:1") {
		t.Fatalf("CheckConfig generated-result error = %v, want non-attributing script source context", err)
	}
	if _, err := service.FormatConfig(context.Background(), source); err == nil || !strings.Contains(err.Error(), "Starlark") {
		t.Fatalf("FormatConfig error = %v, want source-preservation refusal", err)
	}
}
