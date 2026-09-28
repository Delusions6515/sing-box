// Package badh2 keeps HTTP/2 library error TYPES from leaking out of the
// conns our transports build on top of HTTP/2 bodies (lx: SPEC 082).
package badh2

import (
	"errors"

	"golang.org/x/net/http2"
)

// An XHTTP / HTTP / gRPC-lite transport conn reads its download side from an
// HTTP/2 response body, so when the peer (a CDN, in issue #14) resets the
// stream x/net hands the conn an http2.StreamError VALUE. That type must not
// leave the conn. Whoever reads the outbound's conn may itself be an x/net
// HTTP/2 client speaking TLS over it — DoH with detour, a rule-set
// download_detour, a chained outbound — and for that client the error is
// fatal in the worst way: crypto/tls makes a non-net.Error read error sticky,
// x/net's readLoop type-asserts StreamError on every read error and
// `continue`s (it is meant for the framer's own per-stream errors), and the
// goroutine spins at 100% CPU with zero syscalls until the process exits. Two
// readLoops in exactly that state were in the goroutine dump behind issue #14.
//
// RemoteStreamError keeps the text — the "stream error: stream ID N;
// INTERNAL_ERROR; received from peer" line users report stays the same — and
// deliberately has no Unwrap: errors.As must not reach the original either.
type RemoteStreamError struct {
	text string
}

func (e *RemoteStreamError) Error() string {
	return e.text
}

// HideStreamError replaces an http2.StreamError (bare or wrapped) with a
// RemoteStreamError carrying the same text; every other error, io.EOF and nil
// included, is returned untouched.
func HideStreamError(err error) error {
	var streamError http2.StreamError
	if err != nil && errors.As(err, &streamError) {
		return &RemoteStreamError{text: err.Error()}
	}
	return err
}
