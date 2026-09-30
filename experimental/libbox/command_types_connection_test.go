package libbox

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConnectionsApplyWinnerDetailUpdate(t *testing.T) {
	connections := NewConnections()
	connections.ApplyEvents(&ConnectionEvents{events: []*ConnectionEvent{{
		Type: ConnectionEventNew, ID: "id",
		Connection: &Connection{ID: "id", Outbound: "sg-smart", OutboundType: "smart", chainList: []string{"sg-smart", "Proxy"}},
	}}})
	connections.ApplyEvents(&ConnectionEvents{events: []*ConnectionEvent{{
		Type: ConnectionEventUpdate, ID: "id", UplinkDelta: 5,
		Connection: &Connection{ID: "id", Outbound: "actual-node", OutboundType: "vless", chainList: []string{"actual-node", "sg-smart", "Proxy"}},
	}}})
	connection := connections.connectionMap["id"]
	require.Equal(t, "actual-node", connection.Outbound)
	require.Equal(t, "vless", connection.OutboundType)
	require.Equal(t, []string{"actual-node", "sg-smart", "Proxy"}, connection.chainList)
	require.EqualValues(t, 5, connection.UplinkTotal)
}
