package group

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type smartConn struct {
	counter   *bufio.CounterConn
	start     time.Time
	upload    atomic.Int64
	download  atomic.Int64
	failed    atomic.Bool
	firstByte atomic.Int64
	onClose   func(bool, int64, int64, time.Duration, time.Duration)
	once      sync.Once
}

func newSmartConn(conn net.Conn, onClose func(bool, int64, int64, time.Duration, time.Duration)) *smartConn {
	result := &smartConn{start: time.Now(), onClose: onClose}
	result.counter = bufio.NewInt64CounterConn(conn, []*atomic.Int64{&result.download}, []*atomic.Int64{&result.upload})
	return result
}

func (c *smartConn) Read(buffer []byte) (int, error) {
	n, err := c.counter.Read(buffer)
	if n > 0 {
		c.recordFirstByte()
	}
	c.recordError(err)
	return n, err
}

func (c *smartConn) Write(buffer []byte) (int, error) {
	n, err := c.counter.Write(buffer)
	c.recordError(err)
	return n, err
}

func (c *smartConn) ReadBuffer(buffer *buf.Buffer) error {
	err := c.counter.ReadBuffer(buffer)
	if buffer.Len() > 0 {
		c.recordFirstByte()
	}
	c.recordError(err)
	return err
}

func (c *smartConn) WriteBuffer(buffer *buf.Buffer) error {
	err := c.counter.WriteBuffer(buffer)
	c.recordError(err)
	return err
}

func (c *smartConn) UpstreamWriter() any {
	return c.counter
}

func (c *smartConn) LocalAddr() net.Addr {
	return c.counter.LocalAddr()
}

func (c *smartConn) RemoteAddr() net.Addr {
	return c.counter.RemoteAddr()
}

func (c *smartConn) SetDeadline(t time.Time) error {
	return c.counter.SetDeadline(t)
}

func (c *smartConn) SetReadDeadline(t time.Time) error {
	return c.counter.SetReadDeadline(t)
}

func (c *smartConn) SetWriteDeadline(t time.Time) error {
	return c.counter.SetWriteDeadline(t)
}

func (c *smartConn) recordError(err error) {
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		c.failed.Store(true)
	}
}

func (c *smartConn) recordFirstByte() {
	if c.firstByte.Load() == 0 {
		c.firstByte.CompareAndSwap(0, time.Since(c.start).Nanoseconds())
	}
}

func (c *smartConn) Close() error {
	err := c.counter.Close()
	c.once.Do(func() {
		c.onClose(!c.failed.Load(), c.upload.Load(), c.download.Load(), time.Duration(c.firstByte.Load()), time.Since(c.start))
	})
	return err
}

type smartPacketConn struct {
	net.PacketConn
	packetConn N.PacketConn
	start      time.Time
	upload     atomic.Int64
	download   atomic.Int64
	failed     atomic.Bool
	firstByte  atomic.Int64
	onClose    func(bool, int64, int64, time.Duration, time.Duration)
	once       sync.Once
}

func newSmartPacketConn(conn net.PacketConn, packetConn N.PacketConn, onClose func(bool, int64, int64, time.Duration, time.Duration)) *smartPacketConn {
	return &smartPacketConn{PacketConn: conn, packetConn: packetConn, start: time.Now(), onClose: onClose}
}

func (c *smartPacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(buffer)
	if n > 0 {
		c.download.Add(int64(n))
		c.recordFirstByte()
	}
	c.recordError(err)
	return n, addr, err
}

func (c *smartPacketConn) WriteTo(buffer []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(buffer, addr)
	if n > 0 {
		c.upload.Add(int64(n))
	}
	c.recordError(err)
	return n, err
}

func (c *smartPacketConn) UpstreamWriter() any {
	return c.packetConn
}

func (c *smartPacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	destination, err := c.packetConn.ReadPacket(buffer)
	if buffer.Len() > 0 {
		c.download.Add(int64(buffer.Len()))
		c.recordFirstByte()
	}
	c.recordError(err)
	return destination, err
}

func (c *smartPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	dataLen := buffer.Len()
	err := c.packetConn.WritePacket(buffer, destination)
	if err == nil && dataLen > 0 {
		c.upload.Add(int64(dataLen))
	}
	c.recordError(err)
	return err
}

func (c *smartPacketConn) CreatePacketBatchWriter() (N.PacketBatchWriter, bool) {
	writer, created := bufio.CreatePacketBatchWriter(c.packetConn)
	if !created {
		return nil, false
	}
	return &smartPacketBatchWriter{writer: writer, conn: c}, true
}

func (c *smartPacketConn) CreateConnectedPacketBatchWriter() (N.ConnectedPacketBatchWriter, bool) {
	writer, created := bufio.CreateConnectedPacketBatchWriter(c.packetConn)
	if !created {
		return nil, false
	}
	return &smartConnectedPacketBatchWriter{writer: writer, conn: c}, true
}

func (c *smartPacketConn) CreatePacketBatchReadWaiter() (N.PacketBatchReadWaiter, bool) {
	reader, created := bufio.CreatePacketBatchReadWaiter(c.packetConn)
	if !created {
		return nil, false
	}
	return &smartPacketBatchReadWaiter{reader: reader, conn: c}, true
}

func (c *smartPacketConn) CreateConnectedPacketBatchReadWaiter() (N.ConnectedPacketBatchReadWaiter, bool) {
	reader, created := bufio.CreateConnectedPacketBatchReadWaiter(c.packetConn)
	if !created {
		return nil, false
	}
	return &smartConnectedPacketBatchReadWaiter{reader: reader, conn: c}, true
}

func (c *smartPacketConn) recordReadBuffers(buffers []*buf.Buffer) {
	for _, buffer := range buffers {
		if buffer.Len() > 0 {
			c.download.Add(int64(buffer.Len()))
			c.recordFirstByte()
		}
	}
}

func (c *smartPacketConn) recordError(err error) {
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		c.failed.Store(true)
	}
}

func (c *smartPacketConn) recordFirstByte() {
	if c.firstByte.Load() == 0 {
		c.firstByte.CompareAndSwap(0, time.Since(c.start).Nanoseconds())
	}
}

func (c *smartPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(func() {
		c.onClose(!c.failed.Load(), c.upload.Load(), c.download.Load(), time.Duration(c.firstByte.Load()), time.Since(c.start))
	})
	return err
}

type smartPacketBatchWriter struct {
	writer N.PacketBatchWriter
	conn   *smartPacketConn
}

func (w *smartPacketBatchWriter) WritePacketBatch(buffers []*buf.Buffer, destinations []M.Socksaddr) error {
	dataLen := int64(buf.LenMulti(buffers))
	err := w.writer.WritePacketBatch(buffers, destinations)
	if err == nil {
		w.conn.upload.Add(dataLen)
	}
	w.conn.recordError(err)
	return err
}

type smartConnectedPacketBatchWriter struct {
	writer N.ConnectedPacketBatchWriter
	conn   *smartPacketConn
}

func (w *smartConnectedPacketBatchWriter) WriteConnectedPacketBatch(buffers []*buf.Buffer) error {
	dataLen := int64(buf.LenMulti(buffers))
	err := w.writer.WriteConnectedPacketBatch(buffers)
	if err == nil {
		w.conn.upload.Add(dataLen)
	}
	w.conn.recordError(err)
	return err
}

type smartPacketBatchReadWaiter struct {
	reader N.PacketBatchReadWaiter
	conn   *smartPacketConn
}

func (w *smartPacketBatchReadWaiter) InitializeReadWaiter(options N.ReadWaitOptions) bool {
	return w.reader.InitializeReadWaiter(options)
}

func (w *smartPacketBatchReadWaiter) WaitReadPackets() ([]*buf.Buffer, []M.Socksaddr, error) {
	buffers, destinations, err := w.reader.WaitReadPackets()
	w.conn.recordReadBuffers(buffers)
	w.conn.recordError(err)
	return buffers, destinations, err
}

type smartConnectedPacketBatchReadWaiter struct {
	reader N.ConnectedPacketBatchReadWaiter
	conn   *smartPacketConn
}

func (w *smartConnectedPacketBatchReadWaiter) InitializeReadWaiter(options N.ReadWaitOptions) bool {
	return w.reader.InitializeReadWaiter(options)
}

func (w *smartConnectedPacketBatchReadWaiter) WaitReadConnectedPackets() ([]*buf.Buffer, M.Socksaddr, error) {
	buffers, destination, err := w.reader.WaitReadConnectedPackets()
	w.conn.recordReadBuffers(buffers)
	w.conn.recordError(err)
	return buffers, destination, err
}

type smartPlainPacketConn struct {
	net.PacketConn
	start     time.Time
	upload    atomic.Int64
	download  atomic.Int64
	failed    atomic.Bool
	firstByte atomic.Int64
	onClose   func(bool, int64, int64, time.Duration, time.Duration)
	once      sync.Once
}

func newSmartPlainPacketConn(conn net.PacketConn, onClose func(bool, int64, int64, time.Duration, time.Duration)) *smartPlainPacketConn {
	return &smartPlainPacketConn{PacketConn: conn, start: time.Now(), onClose: onClose}
}

func (c *smartPlainPacketConn) UpstreamWriter() any {
	return c.PacketConn
}

func (c *smartPlainPacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(buffer)
	if n > 0 {
		c.download.Add(int64(n))
		c.recordFirstByte()
	}
	c.recordError(err)
	return n, addr, err
}

func (c *smartPlainPacketConn) recordFirstByte() {
	if c.firstByte.Load() == 0 {
		c.firstByte.CompareAndSwap(0, time.Since(c.start).Nanoseconds())
	}
}

func (c *smartPlainPacketConn) WriteTo(buffer []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(buffer, addr)
	if n > 0 {
		c.upload.Add(int64(n))
	}
	c.recordError(err)
	return n, err
}

func (c *smartPlainPacketConn) recordError(err error) {
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		c.failed.Store(true)
	}
}

func (c *smartPlainPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(func() {
		c.onClose(!c.failed.Load(), c.upload.Load(), c.download.Load(), time.Duration(c.firstByte.Load()), time.Since(c.start))
	})
	return err
}
