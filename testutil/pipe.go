package testutil

import (
	"io"
	"net"
	"sync"
	"time"
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type pipeBuffer struct {
	mu            sync.Mutex
	cond          *sync.Cond
	buf           []byte
	head          int
	tail          int
	count         int
	size          int
	closed        bool
	readDeadline  time.Time
	writeDeadline time.Time
	readTimer     *time.Timer
	writeTimer    *time.Timer
}

func newPipeBuffer(size int) *pipeBuffer {
	pb := &pipeBuffer{
		buf:  make([]byte, size),
		size: size,
	}
	pb.cond = sync.NewCond(&pb.mu)
	return pb
}

func (pb *pipeBuffer) Write(p []byte) (int, error) {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	totalWritten := 0
	for len(p) > 0 {
		if pb.closed {
			return totalWritten, io.ErrClosedPipe
		}

		if !pb.writeDeadline.IsZero() && time.Now().After(pb.writeDeadline) {
			return totalWritten, timeoutError{}
		}

		// Wait until there is space
		if pb.count == pb.size {
			pb.cond.Wait()
			continue
		}

		// Calculate available space
		spaceAtTail := pb.size - pb.tail
		if spaceAtTail > pb.size-pb.count {
			spaceAtTail = pb.size - pb.count
		}

		toWrite := len(p)
		if toWrite > spaceAtTail {
			toWrite = spaceAtTail
		}

		copy(pb.buf[pb.tail:], p[:toWrite])
		pb.tail = (pb.tail + toWrite) % pb.size
		pb.count += toWrite
		totalWritten += toWrite
		p = p[toWrite:]

		pb.cond.Broadcast()
	}

	return totalWritten, nil
}

func (pb *pipeBuffer) Read(p []byte) (int, error) {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	for {
		if pb.count > 0 {
			// Calculate readable bytes
			readableAtHead := pb.size - pb.head
			if readableAtHead > pb.count {
				readableAtHead = pb.count
			}

			toRead := len(p)
			if toRead > readableAtHead {
				toRead = readableAtHead
			}

			copy(p[:toRead], pb.buf[pb.head:])
			pb.head = (pb.head + toRead) % pb.size
			pb.count -= toRead

			pb.cond.Broadcast()
			return toRead, nil
		}

		if pb.closed {
			return 0, io.EOF
		}

		if !pb.readDeadline.IsZero() && time.Now().After(pb.readDeadline) {
			return 0, timeoutError{}
		}

		pb.cond.Wait()
	}
}

func (pb *pipeBuffer) Close() error {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	pb.closed = true
	pb.cond.Broadcast()
	return nil
}

func (pb *pipeBuffer) SetReadDeadline(t time.Time) error {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	pb.readDeadline = t
	if pb.readTimer != nil {
		pb.readTimer.Stop()
	}
	if !t.IsZero() {
		d := time.Until(t)
		if d <= 0 {
			pb.cond.Broadcast()
		} else {
			pb.readTimer = time.AfterFunc(d, func() {
				pb.mu.Lock()
				pb.cond.Broadcast()
				pb.mu.Unlock()
			})
		}
	}
	return nil
}

func (pb *pipeBuffer) SetWriteDeadline(t time.Time) error {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	pb.writeDeadline = t
	if pb.writeTimer != nil {
		pb.writeTimer.Stop()
	}
	if !t.IsZero() {
		d := time.Until(t)
		if d <= 0 {
			pb.cond.Broadcast()
		} else {
			pb.writeTimer = time.AfterFunc(d, func() {
				pb.mu.Lock()
				pb.cond.Broadcast()
				pb.mu.Unlock()
			})
		}
	}
	return nil
}

// pipeAddr implements net.Addr for BufferedConn
type pipeAddr struct{}

func (pipeAddr) Network() string { return "buffered-pipe" }
func (pipeAddr) String() string  { return "buffered-pipe" }

// BufferedConn implements net.Conn using two pipeBuffers
type BufferedConn struct {
	readBuf  *pipeBuffer
	writeBuf *pipeBuffer
}

func (c *BufferedConn) Read(b []byte) (n int, err error) {
	return c.readBuf.Read(b)
}

func (c *BufferedConn) Write(b []byte) (n int, err error) {
	return c.writeBuf.Write(b)
}

func (c *BufferedConn) Close() error {
	err1 := c.readBuf.Close()
	err2 := c.writeBuf.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func (c *BufferedConn) LocalAddr() net.Addr {
	return pipeAddr{}
}

func (c *BufferedConn) RemoteAddr() net.Addr {
	return pipeAddr{}
}

func (c *BufferedConn) SetDeadline(t time.Time) error {
	err1 := c.SetReadDeadline(t)
	err2 := c.SetWriteDeadline(t)
	if err1 != nil {
		return err1
	}
	return err2
}

func (c *BufferedConn) SetReadDeadline(t time.Time) error {
	return c.readBuf.SetReadDeadline(t)
}

func (c *BufferedConn) SetWriteDeadline(t time.Time) error {
	return c.writeBuf.SetWriteDeadline(t)
}

// BufferedPipe creates a synchronous, in-memory, full-duplex network connection pair.
// Unlike net.Pipe(), which is strictly unbuffered and forces aggressive context
// switching between readers and writers, BufferedPipe is backed by a circular
// byte buffer of the specified size. This allows benchmarking throughput and
// algorithm performance without OS syscall or Go scheduler skew.
func BufferedPipe(size int) (net.Conn, net.Conn) {
	b1 := newPipeBuffer(size)
	b2 := newPipeBuffer(size)

	c1 := &BufferedConn{readBuf: b1, writeBuf: b2}
	c2 := &BufferedConn{readBuf: b2, writeBuf: b1}

	return c1, c2
}
