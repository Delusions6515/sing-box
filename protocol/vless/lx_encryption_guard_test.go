package vless

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/protocol/vless/encryption"
	M "github.com/sagernet/sing/common/metadata"
)

// newTestEncryption builds a real, initialized ClientInstance. An uninitialized
// one is useless here: its Handshake returns "uninitialized" immediately, so the
// handshake never reaches the blocking read these tests are about, and every
// assertion would pass for the wrong reason.
func newTestEncryption(t *testing.T) *encryption.ClientInstance {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate X25519 key: %v", err)
	}
	instance := &encryption.ClientInstance{}
	if err := instance.Init([][]byte{key.PublicKey().Bytes()}, xorModeNative, 0, ""); err != nil {
		t.Fatalf("init client encryption: %v", err)
	}
	return instance
}

// SPECS/TASKS/050-URLTEST_ZOMBIE_RUN_SURVIVES_RESTART
//
// The handshake ends in a blocking io.ReadFull for the server's reply. A node that
// accepts the connection and then says nothing parks that read forever, and nothing
// above can intervene: the conn has not been handed up yet, the caller is still
// inside DialContext. Before guardHandshake this was the last unbounded wait in the
// dial path — the write side had its deadline since the original task, the read side
// had nothing once the transport-level watchdog was removed by SPEC 077.

// silentConn accepts every write and blocks every read until closed, which is what a
// half-alive node looks like from the client: the TCP handshake succeeded, so the
// dial completed, and the peer then produces nothing.
type silentConn struct {
	net.Conn
	closed    chan struct{}
	closeOnce sync.Once
	closes    atomic.Int32
}

func newSilentConn() *silentConn {
	return &silentConn{closed: make(chan struct{})}
}

func (c *silentConn) Read(b []byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}

func (c *silentConn) Write(b []byte) (int, error) { return len(b), nil }

func (c *silentConn) Close() error {
	c.closes.Add(1)
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *silentConn) SetDeadline(t time.Time) error      { return nil }
func (c *silentConn) SetWriteDeadline(t time.Time) error { return nil }
func (c *silentConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *silentConn) LocalAddr() net.Addr                { return M.Socksaddr{} }
func (c *silentConn) RemoteAddr() net.Addr               { return M.Socksaddr{} }

// TestWrapEncryptionAbortsHandshakeOnContextCancel is the unit form of the stand in
// lx-test/zombie: a cancelled dial context must end the handshake instead of leaving
// the goroutine parked in the read.
func TestWrapEncryptionAbortsHandshakeOnContextCancel(t *testing.T) {
	dialer := &vlessDialer{encryption: newTestEncryption(t)}
	conn := newSilentConn()

	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		_, err := dialer.wrapEncryption(ctx, conn)
		returned <- err
	}()

	// Let the handshake write its padding and reach the read it cannot escape.
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-returned:
		t.Fatalf("handshake returned before the context was cancelled: %v", err)
	default:
	}

	cancel()
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("wrapEncryption reported success although the guard aborted the handshake")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wrapEncryption stayed parked in the handshake read after the dial context was cancelled — " +
			"this is the zombie that outlives box.Close")
	}
	if conn.closes.Load() == 0 {
		t.Fatal("the guard did not close the conn; closing it is the only lever that reaches a parked read")
	}
}

// TestWrapEncryptionGuardStopsBeforeReturn pins the boundary against SPEC 077: the
// guard must not outlive the handshake. A pooled consumer (the DNS transport pool)
// cancels the dial context the moment DialContext returns, by the net.Dialer
// contract — a guard still listening then would close a healthy conn, which is the
// regression SPEC 077 removed the transport-level watchdog for.
func TestWrapEncryptionGuardStopsBeforeReturn(t *testing.T) {
	dialer := &vlessDialer{encryption: newTestEncryption(t)}
	conn := newSilentConn()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The handshake fails on its own: closing the conn up front makes the read
	// return immediately, so wrapEncryption returns while the context is still live.
	conn.Close()
	closesAfterSetup := conn.closes.Load()
	_, err := dialer.wrapEncryption(ctx, conn)
	if err == nil {
		t.Fatal("expected the handshake to fail on a closed conn")
	}

	cancel()
	time.Sleep(100 * time.Millisecond)
	if conn.closes.Load() != closesAfterSetup+1 {
		t.Fatal("the conn was closed again after wrapEncryption returned: the guard outlived the handshake " +
			"and would break every pooled consumer (SPEC 077 §2)")
	}
}

// TestWrapEncryptionWithoutCancellableContextRunsNoGuard: a context that can never
// be cancelled needs no guard goroutine, and the handshake must still work.
func TestWrapEncryptionWithoutCancellableContextRunsNoGuard(t *testing.T) {
	dialer := &vlessDialer{encryption: newTestEncryption(t)}
	conn := newSilentConn()
	conn.Close()

	before := runtimeGoroutines()
	_, err := dialer.wrapEncryption(context.Background(), conn)
	if err == nil {
		t.Fatal("expected the handshake to fail on a closed conn")
	}
	if after := runtimeGoroutines(); after > before {
		t.Fatalf("guard goroutine started for an uncancellable context: %d → %d", before, after)
	}
}

func runtimeGoroutines() int {
	return runtime.NumGoroutine()
}
