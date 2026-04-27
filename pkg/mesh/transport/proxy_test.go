package transport

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Proxy Test Helpers ---

// newTestProxy creates an HTTP proxy server that handles CONNECT requests.
// The handler parameter controls the behavior:
//   - nil: default behavior (200 OK, bidirectional tunnel)
//   - custom: override to return errors, stall, etc.
func newTestProxy(t testing.TB, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodConnect {
				http.Error(w, "only CONNECT supported", http.StatusMethodNotAllowed)
				return
			}

			// Dial the target.
			targetConn, err := net.Dial("tcp", r.Host)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}

			// Hijack the connection to get the raw net.Conn.
			hj, ok := w.(http.Hijacker)
			if !ok {
				if cerr := targetConn.Close(); cerr != nil {
					// log omitted in test helper
					return
				}
				http.Error(w, "hijacking not supported", http.StatusInternalServerError)
				return
			}
			clientConn, clientBuf, err := hj.Hijack()
			if err != nil {
				if cerr := targetConn.Close(); cerr != nil {
					// expected in test
					return
				}
				return
			}

			// Send 200 Connection Established, then flush.
			if _, err := clientBuf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
				return
			}
			if err := clientBuf.Flush(); err != nil {
				return
			}

			// Bidirectional copy. Close both when either direction finishes
			// to prevent goroutine leaks in tests.
			go func() {
				if _, err := io.Copy(targetConn, clientConn); err != nil {
					// expected on close
					return
				}
				if err := targetConn.Close(); err != nil {
					// expected in test
					return
				}
				if err := clientConn.Close(); err != nil {
					// expected in test
					return
				}
			}()
			go func() {
				if _, err := io.Copy(clientConn, targetConn); err != nil {
					// expected on close
					return
				}
				if err := clientConn.Close(); err != nil {
					// expected in test
					return
				}
				if err := targetConn.Close(); err != nil {
					// expected in test
					return
				}
			}()
		}
	}
	return httptest.NewServer(handler)
}

// newEchoServer creates a TCP server that echoes back whatever is sent to it.
func newEchoServer(t testing.TB) (addr string, cleanup func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create echo server: %v", err)
	}

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() {
					if err := c.Close(); err != nil {
						// expected in test
						return
					}
				}()
				if _, err := io.Copy(c, c); err != nil {
					// expected on close
					return
				}
			}()
		}
	}()

	return ln.Addr().String(), func() {
		if err := ln.Close(); err != nil {
			t.Logf("cleanup: close echo server: %v", err)
		}
	}
}

// --- Happy path: successful CONNECT tunnel ---

func TestDialProxy_Success(t *testing.T) {
	t.Parallel()

	echoAddr, echoCleanup := newEchoServer(t)
	defer echoCleanup()

	proxy := newTestProxy(t, nil)
	defer proxy.Close()

	ctx := context.Background()
	conn, err := DialProxy(ctx, proxy.URL, echoAddr)
	if err != nil {
		t.Fatalf("DialProxy error: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	// Verify it satisfies net.Conn.
	var _ = conn

	// Verify bidirectional data through the tunnel.
	payload := []byte("hello through proxy")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	buf := make([]byte, len(payload))
	n, err := io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Read %d bytes, want %d", n, len(payload))
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("Read %q, want %q", buf, payload)
	}
}

// --- Proxy returns non-200 ---

func TestDialProxy_ProxyRejectsConnect(t *testing.T) {
	t.Parallel()

	proxy := newTestProxy(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	defer proxy.Close()

	ctx := context.Background()
	conn, err := DialProxy(ctx, proxy.URL, "unreachable:1234")
	if err == nil {
		if cerr := conn.Close(); cerr != nil {
			t.Logf("close conn: %v", cerr)
		}
		t.Fatal("DialProxy should fail when proxy returns 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should contain status code 403, got: %v", err)
	}
}

// --- Context cancellation ---

func TestDialProxy_ContextCanceled(t *testing.T) {
	t.Parallel()

	// Proxy that stalls forever.
	proxy := newTestProxy(t, func(w http.ResponseWriter, r *http.Request) {
		// Block until test is done.
		<-r.Context().Done()
	})
	defer proxy.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := DialProxy(ctx, proxy.URL, "target:443")
	if err == nil {
		t.Fatal("DialProxy should fail on canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

// --- Invalid proxy URL ---

func TestDialProxy_InvalidProxyURL(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, err := DialProxy(ctx, "://not-a-url", "target:443")
	if err == nil {
		t.Fatal("DialProxy should fail on invalid proxy URL")
	}
}

// --- Proxy connection refused ---

func TestDialProxy_ProxyConnectionRefused(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// Use a port that's definitely not listening.
	_, err := DialProxy(ctx, "http://127.0.0.1:1", "target:443")
	if err == nil {
		t.Fatal("DialProxy should fail when proxy is unreachable")
	}
}

// --- Target unreachable through proxy ---

func TestDialProxy_TargetUnreachable(t *testing.T) {
	t.Parallel()

	proxy := newTestProxy(t, nil)
	defer proxy.Close()

	ctx := context.Background()
	// Use a local port that will be refused (much faster than unreachable IP).
	_, err := DialProxy(ctx, proxy.URL, "127.0.0.1:1")
	if err == nil {
		t.Fatal("DialProxy should fail when target is unreachable through proxy")
	}
}

// --- Large payload through tunnel ---

func TestDialProxy_LargePayload(t *testing.T) {
	t.Parallel()

	echoAddr, echoCleanup := newEchoServer(t)
	defer echoCleanup()

	proxy := newTestProxy(t, nil)
	defer proxy.Close()

	ctx := context.Background()
	conn, err := DialProxy(ctx, proxy.URL, echoAddr)
	if err != nil {
		t.Fatalf("DialProxy error: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	const size = 256 * 1024 // 256KB through the tunnel
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	// Write in a goroutine since echo will buffer.
	errCh := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		errCh <- err
	}()

	received := make([]byte, size)
	_, err = io.ReadFull(conn, received)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("Write error: %v", err)
	}

	if !bytes.Equal(received, payload) {
		t.Fatal("payload mismatch through tunnel")
	}
}

// --- Returned conn has reasonable addresses ---

func TestDialProxy_ConnAddresses(t *testing.T) {
	t.Parallel()

	echoAddr, echoCleanup := newEchoServer(t)
	defer echoCleanup()

	proxy := newTestProxy(t, nil)
	defer proxy.Close()

	ctx := context.Background()
	conn, err := DialProxy(ctx, proxy.URL, echoAddr)
	if err != nil {
		t.Fatalf("DialProxy error: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	// The connection should have a local and remote address.
	if conn.LocalAddr() == nil {
		t.Error("LocalAddr should not be nil")
	}
	if conn.RemoteAddr() == nil {
		t.Error("RemoteAddr should not be nil")
	}
}

// --- Proxy response parsing edge case: extra headers ---

func TestDialProxy_ProxyWithExtraHeaders(t *testing.T) {
	t.Parallel()

	echoAddr, echoCleanup := newEchoServer(t)
	defer echoCleanup()

	proxy := newTestProxy(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "only CONNECT", http.StatusMethodNotAllowed)
			return
		}

		targetConn, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		// Hijack and send a raw response with extra headers.
		hj, ok := w.(http.Hijacker)
		if !ok {
			if cerr := targetConn.Close(); cerr != nil {
				// expected in test
				return
			}
			return
		}
		clientConn, clientBuf, err := hj.Hijack()
		if err != nil {
			if cerr := targetConn.Close(); cerr != nil {
				// expected in test
				return
			}
			return
		}

		// Write raw HTTP response with extra headers.
		if _, err := clientBuf.WriteString("HTTP/1.1 200 Connection Established\r\n"); err != nil {
			return
		}
		if _, err := clientBuf.WriteString("X-Proxy-Agent: test-proxy/1.0\r\n"); err != nil {
			return
		}
		if _, err := clientBuf.WriteString("\r\n"); err != nil {
			return
		}
		if err := clientBuf.Flush(); err != nil {
			return
		}

		// Bidirectional copy — close both on completion.
		go func() {
			if _, err := io.Copy(targetConn, clientConn); err != nil {
				// expected on close
				return
			}
			if err := targetConn.Close(); err != nil {
				// expected in test
				return
			}
			if err := clientConn.Close(); err != nil {
				// expected in test
				return
			}
		}()
		go func() {
			if _, err := io.Copy(clientConn, targetConn); err != nil {
				// expected on close
				return
			}
			if err := clientConn.Close(); err != nil {
				// expected in test
				return
			}
			if err := targetConn.Close(); err != nil {
				// expected in test
				return
			}
		}()
	})
	defer proxy.Close()

	ctx := context.Background()
	conn, err := DialProxy(ctx, proxy.URL, echoAddr)
	if err != nil {
		t.Fatalf("DialProxy error: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	// Should still work despite extra headers in CONNECT response.
	payload := []byte("still works")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	buf := make([]byte, len(payload))
	_, err = io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("got %q, want %q", buf, payload)
	}
}

// Ensure bufio.Reader reference is used (import guard).
var _ = bufio.NewReader
