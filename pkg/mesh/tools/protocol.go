// Package tools provides the tool wire protocol for remote invocation
// over mesh streams. The protocol uses length-prefixed protobuf framing:
//
//	[4 bytes big-endian length][protobuf payload]
//
// Request: ToolRequest protobuf (name + args_json)
// Response: ToolResponse protobuf (is_error + content_json)
//
// This is the same framing pattern used by the control plane codec,
// adapted for the simpler request/response tool invocation pattern.
package tools

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"

	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
	"google.golang.org/protobuf/proto"
)

// maxToolFrameSize is the maximum size of a tool protocol frame (1MB).
// Tool arguments and results should be small JSON payloads; anything
// larger than this is rejected to prevent resource exhaustion.
const maxToolFrameSize = 1 << 20 // 1 MiB

// ServeToolListener accepts connections from a net.Listener and serves
// tool invocations on each connection. Each connection is handled in
// a separate goroutine. The server stops when the context is canceled.
func ServeToolListener(ctx context.Context, lis net.Listener, registry *Registry) {
	go func() {
		<-ctx.Done()
		if err := lis.Close(); err != nil {
			slog.Debug("tools: close listener", "error", err)
		}
	}()

	for {
		conn, err := lis.Accept()
		if err != nil {
			// Check if context was canceled (expected shutdown).
			if ctx.Err() != nil {
				return
			}
			slog.Debug("tools: accept", "error", err)
			return
		}
		go func() {
			if err := ServeToolConn(ctx, conn, registry); err != nil {
				slog.Debug("tools: serve conn", "error", err)
			}
		}()
	}
}

// ServeToolConn serves tool invocations on a single connection.
// It reads ToolRequest messages, dispatches them to the registry,
// and writes ToolResponse messages back. The connection is NOT closed
// by this function — the caller owns connection lifecycle.
//
// Returns nil on clean EOF or context cancellation. Returns an error
// only on protocol violations or unexpected I/O failures.
func ServeToolConn(ctx context.Context, conn net.Conn, registry *Registry) error {
	for {
		// Check context before blocking on read.
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Read request.
		reqData, err := readToolFrame(conn)
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("tools: serve: read request: %w", err)
		}

		var req pb.ToolRequest
		if err := proto.Unmarshal(reqData, &req); err != nil {
			return fmt.Errorf("tools: serve: unmarshal request: %w", err)
		}

		// Dispatch to local registry.
		resp := dispatchLocal(ctx, registry, &req)

		// Write response.
		respData, err := proto.Marshal(resp)
		if err != nil {
			return fmt.Errorf("tools: serve: marshal response: %w", err)
		}

		if err := writeToolFrame(conn, respData); err != nil {
			return fmt.Errorf("tools: serve: write response: %w", err)
		}
	}
}

// dispatchLocal invokes a tool from the request on the local registry
// and returns a ToolResponse. Errors are captured in the response,
// not returned as Go errors.
func dispatchLocal(ctx context.Context, registry *Registry, req *pb.ToolRequest) *pb.ToolResponse {
	result, err := registry.InvokeLocal(ctx, req.Name, json.RawMessage(req.ArgsJson))
	if err != nil {
		errMsg := fmt.Sprintf("tool %q: %v", req.Name, err)
		return &pb.ToolResponse{
			IsError:     true,
			ContentJson: []byte(fmt.Sprintf("%q", errMsg)),
		}
	}
	return &pb.ToolResponse{
		IsError:     result.IsError,
		ContentJson: result.Content,
	}
}

// DialInvoke sends a tool invocation request over a connection and
// reads the response. This is the client-side of the tool wire protocol.
//
// The connection is NOT closed by this function — the caller may reuse
// the connection for multiple sequential invocations.
func DialInvoke(ctx context.Context, conn net.Conn, toolName string, args json.RawMessage) (*ToolResult, error) {
	// Build the request.
	req := &pb.ToolRequest{
		Name:     toolName,
		ArgsJson: args,
	}

	reqData, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("tools: dial: marshal request: %w", err)
	}

	// Set write deadline from context if available.
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			// Non-fatal — some conn types don't support deadlines.
			slog.Debug("tools: dial: set write deadline", "error", err)
		}
	}

	// Write request.
	if err := writeToolFrame(conn, reqData); err != nil {
		return nil, fmt.Errorf("tools: dial: write request: %w", err)
	}

	// Set read deadline from context if available.
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetReadDeadline(deadline); err != nil {
			slog.Debug("tools: dial: set read deadline", "error", err)
		}
	}

	// Read response.
	respData, err := readToolFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("tools: dial: read response: %w", err)
	}

	var resp pb.ToolResponse
	if err := proto.Unmarshal(respData, &resp); err != nil {
		return nil, fmt.Errorf("tools: dial: unmarshal response: %w", err)
	}

	return &ToolResult{
		IsError: resp.IsError,
		Content: resp.ContentJson,
	}, nil
}

// writeToolFrame writes a length-prefixed protobuf payload.
func writeToolFrame(w io.Writer, data []byte) error {
	if len(data) > maxToolFrameSize {
		return fmt.Errorf("tools: frame too large: %d > %d", len(data), maxToolFrameSize)
	}

	// Write 4-byte big-endian length header.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}

	_, err := w.Write(data)
	return err
}

// readToolFrame reads a length-prefixed protobuf payload.
func readToolFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}

	size := binary.BigEndian.Uint32(hdr[:])
	if size > maxToolFrameSize {
		return nil, fmt.Errorf("tools: frame too large: %d > %d", size, maxToolFrameSize)
	}

	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}

	return data, nil
}
