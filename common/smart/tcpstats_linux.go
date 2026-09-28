//go:build linux

package smart

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func readTCPLossRate(rawConn syscall.RawConn) (float64, bool) {
	var info *unix.TCPInfo
	err := rawConn.Control(func(fd uintptr) {
		if fd <= 2 {
			return
		}
		info, _ = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
	})
	if err != nil || info == nil || info.Segs_out == 0 {
		return 0, false
	}
	rate := float64(info.Total_retrans) / float64(info.Segs_out)
	if rate > 1 {
		rate = 1
	}
	return rate, true
}
