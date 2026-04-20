package mcp

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// StartHTTPServer starts an MCP SSE HTTP server with mTLS protection.
func StartHTTPServer(addr string, srv *Server, cert tls.Certificate, caPool *x509.CertPool) error {
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}

	handler := mcp.NewSSEHandler(func(req *http.Request) *mcp.Server {
		if req.URL.Path == "/mcp" {
			return srv.MCPServer()
		}
		return nil
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/messages", handler) // SDK uses this pattern for incoming messages

	httpServer := &http.Server{
		Addr:      addr,
		Handler:   mux,
		TLSConfig: tlsConfig,
	}

	zap.S().Infow("starting mTLS MCP SSE server", "addr", addr)

	// Since we pass TLSConfig directly to the server, we can use empty strings for cert/key files
	if err := httpServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server failed: %w", err)
	}
	return nil
}
