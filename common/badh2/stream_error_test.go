package badh2

import (
	"errors"
	"io"
	"testing"

	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/net/http2"
)

func TestHideStreamError(t *testing.T) {
	peerReset := http2.StreamError{StreamID: 21, Code: http2.ErrCodeInternal, Cause: errors.New("received from peer")}

	hidden := HideStreamError(peerReset)
	if hidden.Error() != peerReset.Error() {
		t.Fatalf("text changed: %q != %q", hidden.Error(), peerReset.Error())
	}
	if _, isStreamError := hidden.(http2.StreamError); isStreamError {
		t.Fatal("bare StreamError leaked through")
	}
	var target http2.StreamError
	if errors.As(hidden, &target) {
		t.Fatal("errors.As reaches the StreamError through the hidden error")
	}

	wrapped := HideStreamError(E.Cause(peerReset, "download"))
	if errors.As(wrapped, &target) {
		t.Fatal("wrapped StreamError leaked through")
	}
	if wrapped.Error() != "download: "+peerReset.Error() {
		t.Fatalf("wrapped text changed: %q", wrapped.Error())
	}

	if HideStreamError(io.EOF) != io.EOF {
		t.Fatal("io.EOF must pass through untouched")
	}
	if HideStreamError(nil) != nil {
		t.Fatal("nil must pass through untouched")
	}
	other := errors.New("plain")
	if HideStreamError(other) != other {
		t.Fatal("unrelated errors must pass through untouched")
	}
}
