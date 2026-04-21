package mcp

import (
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// StartHTTPServer starts an MCP SSE HTTP server.
func StartHTTPServer(addr string, srv *Server) error {
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
		Addr:    addr,
		Handler: mux,
	}

	zap.S().Infow("starting MCP SSE server", "addr", addr)

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server failed: %w", err)
	}
	return nil
}
