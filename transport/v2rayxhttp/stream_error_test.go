package v2rayxhttp

import (
	"context"
	stdtls "crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/net/http2"
)

// lx: SPEC 082 — issue #14. See common/badh2 for why the http2.StreamError
// type must not leave an XHTTP conn.

// resettingBody is a download body whose every Read is a peer stream reset —
// what x/net's response body returns after RST_STREAM(INTERNAL_ERROR). It
// counts the reads so a spinning consumer is visible.
type resettingBody struct {
	reads atomic.Int64
}

func (b *resettingBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	return 0, http2.StreamError{StreamID: 21, Code: http2.ErrCodeInternal, Cause: errors.New("received from peer")}
}

func (b *resettingBody) Close() error { return nil }

// TestStreamErrorDoesNotSpinConsumerReadLoop is the field failure of issue #14
// in miniature: an x/net HTTP/2 client (what DoH with detour, a rule-set
// download_detour or a chained outbound put on top of us) whose transport
// connection IS an XHTTP stream conn, and the CDN resets that stream. Without
// badh2.HideStreamError the consumer's readLoop sees http2.StreamError, `continue`s
// forever and never lets RoundTrip fail: the body is read millions of times
// and the request only ends with the context. With it the readLoop exits on
// the first read error and RoundTrip fails at once.
func TestStreamErrorDoesNotSpinConsumerReadLoop(t *testing.T) {
	body := &resettingBody{}
	uploadReader, uploadWriter := io.Pipe()
	conn := newStreamConn(uploadReader, uploadWriter, M.ParseSocksaddr("example.com:443"), nil)
	conn.setupReader(body, nil)
	go io.Copy(io.Discard, uploadReader) // the consumer's preface and frames go nowhere
	defer conn.Close()

	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(context.Context, string, string, *stdtls.Config) (net.Conn, error) {
			return conn, nil
		},
	}
	defer transport.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	_, err = transport.RoundTrip(request)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("RoundTrip succeeded over a dead conn")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RoundTrip only ended with the context after %s: the consumer readLoop is spinning (body reads: %d)", elapsed, body.reads.Load())
	}
	if reads := body.reads.Load(); reads > 16 {
		t.Fatalf("body read %d times: the consumer readLoop kept reading after the reset", reads)
	}
	var target http2.StreamError
	if errors.As(err, &target) {
		t.Fatalf("StreamError type reached the consumer: %v", err)
	}
}
