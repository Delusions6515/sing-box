//go:build !linux

package smart

import "syscall"

func readTCPStats(syscall.RawConn) (TCPStats, bool) { return TCPStats{}, false }
