package v2rayhttp

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
func TestHTTP2ConnHidesStreamError(t *testing.T) {
	var target http2.StreamError

	conn := NewLateHTTPConn(io.Discard)
	conn.Setup(resettingReader{}, nil)
	_, err := conn.Read(make([]byte, 16))
	if err == nil || errors.As(err, &target) {
		t.Fatalf("body read leaked StreamError: %v", err)
	}

	late := NewLateHTTPConn(io.Discard)
	late.Setup(nil, http2.StreamError{StreamID: 3, Code: http2.ErrCodeInternal})
	_, err = late.Read(make([]byte, 16))
	if err == nil || errors.As(err, &target) {
		t.Fatalf("late setup leaked StreamError: %v", err)
	}
}
