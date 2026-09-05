package v2rayxhttp

import (
	"errors"

	"golang.org/x/net/http2"
)

// lx: SPEC 082 — the download body is an HTTP/2 response body, so when the
// peer (a CDN, in issue #14) resets the stream x/net hands us an
// http2.StreamError VALUE. That type must not leave this conn. Whoever reads
// the outbound's conn may itself be an x/net HTTP/2 client speaking TLS over
// us — DoH with detour, a rule-set download_detour, a chained outbound — and
// for that client the error is fatal in the worst way: crypto/tls makes a
// non-net.Error read error sticky, x/net's readLoop type-asserts StreamError
// on every read error and `continue`s (it is meant for the framer's own
// per-stream errors), and the goroutine spins at 100% CPU with zero syscalls
// until the process exits. Two readLoops in exactly that state were in the
// goroutine dump behind issue #14.
//
// remoteStreamError keeps the text — the "stream error: stream ID N;
// INTERNAL_ERROR; received from peer" line users report stays the same — and
// deliberately has no Unwrap: errors.As must not reach the original either.
type remoteStreamError struct {
	text string
}

func (e *remoteStreamError) Error() string {
	return e.text
}

// hideStreamError replaces an http2.StreamError (bare or wrapped) with a
// remoteStreamError carrying the same text; every other error, io.EOF and nil
// included, is returned untouched.
func hideStreamError(err error) error {
	var streamError http2.StreamError
	if err != nil && errors.As(err, &streamError) {
		return &remoteStreamError{text: err.Error()}
	}
	return err
}
