package transport

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
)

// DialProxy establishes a TCP connection to targetHost through an HTTP
// CONNECT proxy. This is the exception-path transport for crossing corporate
// firewalls and air-gap boundaries.
//
// The returned net.Conn is the raw TCP connection to the proxy, which has
// been upgraded to a tunnel after a successful CONNECT handshake. The caller
// feeds this into the membrane layer for mTLS + yamux.
//
// proxyURL must be a valid HTTP(S) URL (e.g., "http://proxy.corp:8080").
// targetHost is the host:port to tunnel to (e.g., "10.0.1.5:443").
func DialProxy(ctx context.Context, proxyURL, targetHost string) (net.Conn, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("transport: proxy: invalid proxy URL: %w", err)
	}

	proxyAddr := parsed.Host
	if proxyAddr == "" {
		return nil, fmt.Errorf("transport: proxy: missing host in proxy URL %q", proxyURL)
	}

	// Dial the proxy with context support.
	var dialer net.Dialer
	proxyConn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("transport: proxy: dial proxy %q: %w", proxyAddr, err)
	}

	// Send CONNECT request.
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+targetHost, nil)
	if err != nil {
		if cerr := proxyConn.Close(); cerr != nil {
			return nil, fmt.Errorf("transport: proxy: create CONNECT request: %w (close: %v)", err, cerr)
		}
		return nil, fmt.Errorf("transport: proxy: create CONNECT request: %w", err)
	}
	req.Host = targetHost

	if err := req.Write(proxyConn); err != nil {
		if cerr := proxyConn.Close(); cerr != nil {
			return nil, fmt.Errorf("transport: proxy: write CONNECT request: %w (close: %v)", err, cerr)
		}
		return nil, fmt.Errorf("transport: proxy: write CONNECT request: %w", err)
	}

	// Read the proxy's response.
	br := bufio.NewReader(proxyConn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		if cerr := proxyConn.Close(); cerr != nil {
			return nil, fmt.Errorf("transport: proxy: read CONNECT response: %w (close: %v)", err, cerr)
		}
		return nil, fmt.Errorf("transport: proxy: read CONNECT response: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return nil, fmt.Errorf("transport: proxy: close response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if cerr := proxyConn.Close(); cerr != nil {
			return nil, fmt.Errorf("transport: proxy: CONNECT to %q through %q failed: %d %s (close: %v)",
				targetHost, proxyAddr, resp.StatusCode, resp.Status, cerr)
		}
		return nil, fmt.Errorf("transport: proxy: CONNECT to %q through %q failed: %d %s",
			targetHost, proxyAddr, resp.StatusCode, resp.Status)
	}

	// If the bufio.Reader has buffered data beyond the HTTP response,
	// we need to wrap the connection so reads drain the buffer first.
	if br.Buffered() > 0 {
		return &bufferedConn{
			Conn:   proxyConn,
			reader: br,
		}, nil
	}

	return proxyConn, nil
}

// bufferedConn wraps a net.Conn with a bufio.Reader to drain any data
// that was buffered during HTTP response parsing.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}
