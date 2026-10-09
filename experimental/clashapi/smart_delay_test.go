package clashapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/group"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
	"github.com/stretchr/testify/require"
)

type manualSmartLeaf struct {
	historyTestOutbound
	calls atomic.Int32
}

func (o *manualSmartLeaf) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	o.calls.Add(1)
	return (&net.Dialer{}).DialContext(ctx, network, destination.String())
}

func TestManualLeafDelayDoesNotProbeSmartGroup(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { time.Sleep(3 * time.Millisecond); w.WriteHeader(204) }))
	defer endpoint.Close()
	leaf := &manualSmartLeaf{historyTestOutbound: historyTestOutbound{plainOutbound: plainOutbound{tag: "leaf"}}}
	manager := &smartOutboundManager{outbounds: []adapter.Outbound{leaf}}
	history := urltest.NewHistoryStorage()
	ctx := service.ContextWith[adapter.OutboundManager](context.Background(), manager)
	ctx = service.ContextWithPtr(ctx, history)
	ctx = filemanager.WithDefault(ctx, t.TempDir(), "", os.Getuid(), os.Getgid())
	outbound, err := group.NewSmart(ctx, nil, log.NewNOPFactory().NewLogger("test"), "smart", option.SmartOutboundOptions{GroupCommonOption: option.GroupCommonOption{Outbounds: []string{"leaf"}}, URL: endpoint.URL})
	require.NoError(t, err)
	smart := outbound.(*group.Smart)
	scope := adapter.NewScope(ctx, log.NewNOPFactory().NewLogger("test"))
	t.Cleanup(func() { require.NoError(t, scope.Close()) })
	require.NoError(t, scope.Start("smart", smart, adapter.StartStateInitialize))
	require.NoError(t, scope.Start("smart", smart, adapter.StartStateStart))
	manager.outbounds = append(manager.outbounds, smart)
	_, updateGroup := any(smart).(adapter.URLTestGroup)
	require.False(t, updateGroup)
	request := httptest.NewRequest(http.MethodGet, "/proxies/leaf/delay?timeout=1000&url="+url.QueryEscape(endpoint.URL), nil)
	request = request.WithContext(context.WithValue(request.Context(), CtxKeyProxy, leaf))
	response := httptest.NewRecorder()
	getProxyDelay(&Server{ctx: ctx, outbound: manager, urlTestHistory: history})(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.EqualValues(t, 1, leaf.calls.Load())
}

func TestSmartGroupDelayUsesItsConfiguredProbe(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { time.Sleep(3 * time.Millisecond); w.WriteHeader(204) }))
	defer endpoint.Close()
	leaf := &manualSmartLeaf{historyTestOutbound: historyTestOutbound{plainOutbound: plainOutbound{tag: "leaf"}}}
	manager := &smartOutboundManager{outbounds: []adapter.Outbound{leaf}}
	history := urltest.NewHistoryStorage()
	ctx := service.ContextWith[adapter.OutboundManager](context.Background(), manager)
	ctx = service.ContextWithPtr(ctx, history)
	ctx = filemanager.WithDefault(ctx, t.TempDir(), "", os.Getuid(), os.Getgid())
	outbound, err := group.NewSmart(ctx, nil, log.NewNOPFactory().NewLogger("test"), "smart", option.SmartOutboundOptions{GroupCommonOption: option.GroupCommonOption{Outbounds: []string{"leaf"}}, URL: endpoint.URL})
	require.NoError(t, err)
	smart := outbound.(*group.Smart)
	scope := adapter.NewScope(ctx, log.NewNOPFactory().NewLogger("test"))
	t.Cleanup(func() { require.NoError(t, scope.Close()) })
	require.NoError(t, scope.Start("smart", smart, adapter.StartStateInitialize))
	require.NoError(t, scope.Start("smart", smart, adapter.StartStateStart))
	request := httptest.NewRequest(http.MethodGet, "/group/smart/delay?timeout=1000", nil)
	request = request.WithContext(context.WithValue(request.Context(), CtxKeyProxy, smart))
	response := httptest.NewRecorder()
	getGroupDelay(&Server{ctx: ctx, outbound: manager, urlTestHistory: history})(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.EqualValues(t, 1, leaf.calls.Load())
}
