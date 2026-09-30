//go:build linux

package smart

import (
	"golang.org/x/sys/unix"
	"syscall"
)

func readTCPStats(raw syscall.RawConn) (TCPStats, bool) {
	var info *unix.TCPInfo
	err := raw.Control(func(fd uintptr) { info, _ = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO) })
	if err != nil || info == nil || info.Segs_out == 0 {
		return TCPStats{}, false
	}
	return TCPStats{Sent: uint64(info.Segs_out), Retransmitted: uint64(info.Total_retrans)}, true
}
