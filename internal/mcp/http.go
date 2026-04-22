package mcp

import (
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// corsMiddleware allows cross-origin requests from tools like the MCP Inspector.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// StartHTTPServer starts an MCP Streamable HTTP server.
func StartHTTPServer(addr string, srv *Server) error {
	// Explicitly disable Go 1.25's built-in Sec-Fetch-Site and Origin protections
	// since the MCP server is designed to be accessed cross-origin (e.g., from the Inspector).
	cop := http.NewCrossOriginProtection()
	cop.AddInsecureBypassPattern("/")

	opts := &mcp.StreamableHTTPOptions{
		DisableLocalhostProtection: true,
		CrossOriginProtection:      cop,
	}

	handler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		if req.URL.Path == "/sse" {
			return srv.MCPServer()
		}
		return nil
	}, opts)

	mux := http.NewServeMux()
	mux.Handle("/sse", corsMiddleware(handler))
	mux.Handle("/messages", corsMiddleware(handler)) // SDK uses this pattern for incoming messages

	httpServer := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	uri := fmt.Sprintf("http://%s/sse", addr)
	zap.S().Infow("starting MCP Streamable HTTP server", "addr", addr, "uri", uri)

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server failed: %w", err)
	}
	return nil
}
