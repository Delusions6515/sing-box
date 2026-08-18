//go:build !linux

package smart

import "syscall"

func readTCPLossRate(syscall.RawConn) (float64, bool) { return 0, false }
