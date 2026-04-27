package api

import (
	"log/slog"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
)

// bootstrapChunkSize is the maximum number of host entries per bootstrap
// frame. At ~100 bytes per entry (hostname + addresses + transport),
// 500 entries ≈ 50KB per frame — well under MaxFrameSize (256KB).
const bootstrapChunkSize = 500

// buildBootstrapFrame constructs a single BootstrapFrame from the local
// resolver cache. This is the legacy single-frame builder, kept for
// backwards compatibility with existing tests.
//
// WARNING: At scale (>3000 entries), this frame may exceed MaxFrameSize.
// Use buildBootstrapFrames() for production code.
func (n *Node) buildBootstrapFrame() *pb.ControlFrame {
	entries := n.resolver.AllEntries()

	hosts := make(map[string]*pb.HostEntry, len(entries))
	for _, entry := range entries {
		addrs := make([]string, len(entry.Addresses))
		copy(addrs, entry.Addresses)
		hosts[entry.Hostname] = &pb.HostEntry{
			Addresses: addrs,
			Transport: entry.Transport,
			ProxyUrl:  entry.ProxyURL,
		}
	}

	return &pb.ControlFrame{
		Payload: &pb.ControlFrame_Bootstrap{
			Bootstrap: &pb.BootstrapFrame{
				Hosts:              hosts,
				Sequence:           0,
				Total:              1,
				ProtocolVersion:    uint32(n.protocolVersion),
				ApplicationVersion: []byte(n.applicationVersion),
			},
		},
	}
}

// buildBootstrapFrames constructs paginated BootstrapFrames from the local
// resolver cache. Each frame contains at most bootstrapChunkSize entries,
// ensuring each frame stays well under MaxFrameSize (256KB).
//
// Returns nil if the resolver is empty (no frames to send).
func (n *Node) buildBootstrapFrames() []*pb.ControlFrame {
	entries := n.resolver.AllEntries()
	if len(entries) == 0 {
		return []*pb.ControlFrame{
			{
				Payload: &pb.ControlFrame_Bootstrap{
					Bootstrap: &pb.BootstrapFrame{
						Hosts:              nil,
						Sequence:           0,
						Total:              1,
						ProtocolVersion:    uint32(n.protocolVersion),
						ApplicationVersion: []byte(n.applicationVersion),
					},
				},
			},
		}
	}

	totalChunks := (len(entries) + bootstrapChunkSize - 1) / bootstrapChunkSize
	frames := make([]*pb.ControlFrame, 0, totalChunks)

	for i := 0; i < len(entries); i += bootstrapChunkSize {
		end := i + bootstrapChunkSize
		if end > len(entries) {
			end = len(entries)
		}
		chunk := entries[i:end]

		hosts := make(map[string]*pb.HostEntry, len(chunk))
		for _, entry := range chunk {
			addrs := make([]string, len(entry.Addresses))
			copy(addrs, entry.Addresses)
			hosts[entry.Hostname] = &pb.HostEntry{
				Addresses: addrs,
				Transport: entry.Transport,
				ProxyUrl:  entry.ProxyURL,
			}
		}

		frames = append(frames, &pb.ControlFrame{
			Payload: &pb.ControlFrame_Bootstrap{
				Bootstrap: &pb.BootstrapFrame{
					Hosts:              hosts,
					Sequence:           uint32(len(frames)),
					Total:              uint32(totalChunks),
					ProtocolVersion:    uint32(n.protocolVersion),
					ApplicationVersion: []byte(n.applicationVersion),
				},
			},
		})
	}

	return frames
}

// handleBootstrapFrame processes an incoming BootstrapFrame by merging
// the received host entries into the local resolver cache.
// It also performs protocol version validation on the peer connection.
func (n *Node) handleBootstrapFrame(pc *peerConn, bf *pb.BootstrapFrame) {
	if bf == nil {
		return
	}

	// Protocol enforcement: mismatch requires connection termination.
	if bf.ProtocolVersion != uint32(n.protocolVersion) {
		slog.Error("api: peer protocol version mismatch, closing connection",
			"peer", pc.nodeID,
			"peer_version", bf.ProtocolVersion,
			"local_version", n.protocolVersion,
		)
		pc.closeDead()
		return
	}

	if len(bf.Hosts) > 0 {
		entries := make([]nucleus.ResolverEntry, 0, len(bf.Hosts))
		for hostname, he := range bf.Hosts {
			addrs := make([]string, len(he.Addresses))
			copy(addrs, he.Addresses)
			entries = append(entries, nucleus.ResolverEntry{
				Hostname:  hostname,
				Addresses: addrs,
				Transport: he.Transport,
				ProxyURL:  he.GetProxyUrl(),
			})
		}
		n.resolver.Merge(entries)
	}

	slog.Info("api: bootstrap chunk received",
		"peer", pc.nodeID,
		"hosts", len(bf.Hosts),
		"sequence", bf.Sequence,
		"total", bf.Total,
		"protocol_version", bf.ProtocolVersion,
		"application_version", string(bf.ApplicationVersion),
	)
}

// bootstrapRetryInitial is the initial backoff delay when the write queue
// is full during bootstrap delivery.
const bootstrapRetryInitial = 10 * time.Millisecond

// bootstrapRetryMax is the maximum backoff delay between retry attempts.
const bootstrapRetryMax = 500 * time.Millisecond

// sendBootstrapReliable sends the local resolver cache to a peer with
// guaranteed delivery. Unlike sendBootstrap, it retries with exponential
// backoff when the per-peer write queue is full, instead of silently
// dropping chunks.
//
// Exits early if:
//   - The peer dies (deadCh closes)
//   - The node's context is cancelled
//
// This is the P1-4 fix: bootstrap with 50k+ resolver entries no longer
// silently truncates at the 64-slot write queue.
func (n *Node) sendBootstrapReliable(pc *peerConn) {
	frames := n.buildBootstrapFrames()
	if len(frames) == 0 {
		return
	}

	backoff := bootstrapRetryInitial

	for i, frame := range frames {
		for {
			if pc.sendControl(frame) {
				// Successfully enqueued — reset backoff for next chunk.
				backoff = bootstrapRetryInitial
				break
			}

			// Queue full — retry with backoff.
			slog.Debug("api: bootstrap chunk queue-full, retrying",
				"peer", pc.nodeID,
				"chunk", i,
				"total", len(frames),
				"backoff", backoff,
			)

			select {
			case <-time.After(backoff):
				// Grow backoff exponentially, capped.
				backoff *= 2
				if backoff > bootstrapRetryMax {
					backoff = bootstrapRetryMax
				}
			case <-pc.dead():
				slog.Debug("api: bootstrap aborted — peer died",
					"peer", pc.nodeID,
					"delivered", i,
					"total", len(frames),
				)
				return
			case <-n.ctx.Done():
				slog.Debug("api: bootstrap aborted — context cancelled",
					"peer", pc.nodeID,
					"delivered", i,
					"total", len(frames),
				)
				return
			}
		}
	}

	slog.Info("api: bootstrap fully delivered",
		"peer", pc.nodeID,
		"chunks", len(frames),
	)
}
