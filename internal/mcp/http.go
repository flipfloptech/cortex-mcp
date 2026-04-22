package mcp

import (
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// corsMiddleware allows cross-origin requests from tools like the MCP Inspector.
// The Streamable HTTP protocol requires the browser to send MCP-specific headers
// (Mcp-Session-Id, Mcp-Protocol-Version) which must be explicitly permitted.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID")
		w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// StartHTTPServer starts an MCP Streamable HTTP server.
//
// The handler is mounted at /mcp — a single endpoint that handles GET (SSE stream),
// POST (JSON-RPC messages), and DELETE (session teardown) per the MCP Streamable
// HTTP specification.
func StartHTTPServer(addr string, srv *Server) error {
	// Disable Go 1.25's built-in CrossOriginProtection (Sec-Fetch-Site enforcement)
	// which unconditionally rejects browser-based inspector POST requests.
	// Our corsMiddleware handles CORS permissioning instead.
	cop := http.NewCrossOriginProtection()
	cop.AddInsecureBypassPattern("/mcp")

	handler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return srv.MCPServer()
	}, &mcp.StreamableHTTPOptions{
		DisableLocalhostProtection: true,
		CrossOriginProtection:      cop,
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", corsMiddleware(handler))

	httpServer := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	uri := fmt.Sprintf("http://%s/mcp", addr)
	zap.S().Infow("starting MCP Streamable HTTP server", "addr", addr, "uri", uri)

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server failed: %w", err)
	}
	return nil
}
