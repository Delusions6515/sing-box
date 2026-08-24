package smart

import (
	"net"
	"syscall"
)

// TCPLossRate reports retransmission loss when the underlying platform and
// connection expose TCP_INFO. Unsupported transports return available=false.
func TCPLossRate(conn net.Conn) (rate float64, available bool) {
	for range 16 {
		if conn == nil {
			return 0, false
		}
		if syscallConn, ok := conn.(interface {
			SyscallConn() (syscall.RawConn, error)
		}); ok {
			rawConn, err := syscallConn.SyscallConn()
			if err != nil {
				return 0, false
			}
			return readTCPLossRate(rawConn)
		}
		upstream, ok := conn.(interface{ Upstream() any })
		if !ok {
			return 0, false
		}
		next, ok := upstream.Upstream().(net.Conn)
		if !ok || next == conn {
			return 0, false
		}
		conn = next
	}
	return 0, false
}
