package daemon

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"

	"github.com/stretchr/testify/require"
)

type connectionWinnerOutbound struct {
	adapter.Outbound
	tag, kind string
}

func (o *connectionWinnerOutbound) Tag() string  { return o.tag }
func (o *connectionWinnerOutbound) Type() string { return o.kind }

func TestBuildConnectionProtoUsesSmartConnectionWinner(t *testing.T) {
	selected := new(atomic.Pointer[adapter.Outbound])
	metadata := &trafficcontrol.TrackerMetadata{
		Metadata: adapter.InboundContext{SelectedOutbound: selected},
		Chain:    []string{"sg-smart", "Proxy"},
		Outbound: "sg-smart", OutboundType: "smart",
		Upload: new(atomic.Int64), Download: new(atomic.Int64),
	}
	var winner adapter.Outbound = &connectionWinnerOutbound{tag: "actual-node", kind: "vless"}
	selected.Store(&winner)
	connection := buildConnectionProto(metadata)
	require.Equal(t, []string{"actual-node", "sg-smart", "Proxy"}, connection.ChainList)
	require.Equal(t, "actual-node", connection.Outbound)
	require.Equal(t, "vless", connection.OutboundType)
}

func TestSmartConnectionWinnerEmitsDetailUpdate(t *testing.T) {
	manager := trafficcontrol.NewManager()
	selected := new(atomic.Pointer[adapter.Outbound])
	var smart adapter.Outbound = &connectionWinnerOutbound{tag: "sg-smart", kind: "smart"}
	flow := manager.RoutedFlow(context.Background(), adapter.InboundContext{
		OutboundChain: []adapter.Outbound{smart}, SelectedOutbound: selected,
	}, nil, smart)
	flow.AttachFlow(nil)
	defer flow.CloseFlow(0)

	snapshots := make(map[uuid.UUID]connectionSnapshot)
	service := new(StartedService)
	initial := service.buildTrafficUpdates(manager, snapshots)
	require.Len(t, initial, 1)
	require.Equal(t, ConnectionEventType_CONNECTION_EVENT_NEW, initial[0].Type)
	require.Equal(t, []string{"sg-smart"}, initial[0].Connection.ChainList)

	var winner adapter.Outbound = &connectionWinnerOutbound{tag: "actual-node", kind: "vless"}
	selected.Store(&winner)
	updates := service.buildTrafficUpdates(manager, snapshots)
	require.Len(t, updates, 1)
	require.Equal(t, ConnectionEventType_CONNECTION_EVENT_UPDATE, updates[0].Type)
	require.Equal(t, []string{"actual-node", "sg-smart"}, updates[0].Connection.ChainList)
	require.Empty(t, service.buildTrafficUpdates(manager, snapshots))
}

func TestBuildConnectionProtoUsesSniffHost(t *testing.T) {
	metadata := &trafficcontrol.TrackerMetadata{
		Metadata: adapter.InboundContext{
			SniffHost: "sniff.example.com",
		},
		Upload:   new(atomic.Int64),
		Download: new(atomic.Int64),
	}

	require.Equal(t, "sniff.example.com", buildConnectionProto(metadata).Domain)
}
