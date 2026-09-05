package v2raygrpclite

import (
	"errors"
	"io"
	"testing"

	"golang.org/x/net/http2"
)

type resettingReader struct{}

func (resettingReader) Read([]byte) (int, error) {
	return 0, http2.StreamError{StreamID: 21, Code: http2.ErrCodeInternal, Cause: errors.New("received from peer")}
}

// lx: SPEC 082 — issue #14: the HTTP/2 library's StreamError type must not
// leave the conn, neither from a body read nor from a failed late setup.
func TestGunConnHidesStreamError(t *testing.T) {
	var target http2.StreamError

	conn := newLateGunConn(io.Discard)
	conn.setup(resettingReader{}, nil)
	_, err := conn.Read(make([]byte, 16))
	if err == nil || errors.As(err, &target) {
		t.Fatalf("body read leaked StreamError: %v", err)
	}

	late := newLateGunConn(io.Discard)
	late.setup(nil, http2.StreamError{StreamID: 3, Code: http2.ErrCodeInternal})
	_, err = late.Read(make([]byte, 16))
	if err == nil || errors.As(err, &target) {
		t.Fatalf("late setup leaked StreamError: %v", err)
	}
}
