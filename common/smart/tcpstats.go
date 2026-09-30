package smart

import (
	"net"
	"syscall"
)

type TCPStats struct {
	Sent          uint64
	Retransmitted uint64
}

func packetLoss(sent, retransmitted uint64) float64 {
	if sent == 0 {
		return 0
	}
	return min(1, float64(retransmitted)/float64(sent))
}

// TCPStatsFor samples a bounded chain of explicit connection wrappers. Missing
// TCP_INFO is unavailable, not evidence of a loss-free connection.
func TCPStatsFor(conn net.Conn) (TCPStats, bool) {
	for range 16 {
		if conn == nil {
			return TCPStats{}, false
		}
		if sc, ok := conn.(interface {
			SyscallConn() (syscall.RawConn, error)
		}); ok {
			raw, err := sc.SyscallConn()
			if err != nil {
				return TCPStats{}, false
			}
			return readTCPStats(raw)
		}
		var next net.Conn
		if upstream, ok := conn.(interface{ Upstream() any }); ok {
			next, _ = upstream.Upstream().(net.Conn)
		}
		if next == nil {
			if wrapper, ok := conn.(interface{ NetConn() net.Conn }); ok {
				next = wrapper.NetConn()
			}
		}
		if next == nil || next == conn {
			return TCPStats{}, false
		}
		conn = next
	}
	return TCPStats{}, false
}

// TCPStatsDelta excludes the socket's history before the logical connection.
// A reset/wrap is unavailable rather than an enormous unsigned delta.
func TCPStatsDelta(before, after TCPStats) (TCPStats, bool) {
	if after.Sent < before.Sent || after.Retransmitted < before.Retransmitted {
		return TCPStats{}, false
	}
	result := TCPStats{Sent: after.Sent - before.Sent, Retransmitted: after.Retransmitted - before.Retransmitted}
	return result, result.Sent > 0
}

func TCPLossRate(conn net.Conn) (float64, bool) {
	stats, ok := TCPStatsFor(conn)
	return packetLoss(stats.Sent, stats.Retransmitted), ok
}
