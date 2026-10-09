package smart

import (
	"github.com/stretchr/testify/require"
	"net"
	"testing"
)

type cyclicTCPWrapper struct{ net.Conn }

func (c *cyclicTCPWrapper) Upstream() any { return c }

func TestTCPStatsDeltaDoesNotCountReusedSocketHistory(t *testing.T) {
	delta, ok := TCPStatsDelta(TCPStats{Sent: 1000, Retransmitted: 100}, TCPStats{Sent: 1100, Retransmitted: 101})
	require.True(t, ok)
	require.Equal(t, uint64(100), delta.Sent)
	require.Equal(t, uint64(1), delta.Retransmitted)
	_, ok = TCPStatsDelta(TCPStats{Sent: 1000}, TCPStats{Sent: 1})
	require.False(t, ok)
	_, ok = TCPStatsFor(&cyclicTCPWrapper{})
	require.False(t, ok)
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	_, ok = TCPStatsFor(local)
	require.False(t, ok)
}
