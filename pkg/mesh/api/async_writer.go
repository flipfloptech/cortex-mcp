package api

import (
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"crypto/ed25519"

	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
)

// controlWriteQueueSize is the per-peer buffered channel capacity for
// outbound control messages. When the channel is full, messages are dropped
// rather than blocking the sender. Gossip and discovery frames are
// eventually consistent and loss-tolerant — dropping is safe.
//
// 64 frames provides ~20 seconds of gossip backlog at 3s intervals,
// plus headroom for burst WhoHas/IHave traffic during Sonar requests.
const controlWriteQueueSize = 64

// controlWriteDeadline is the maximum time a control frame write may
// block on the underlying connection before being considered failed.
// This mitigates HOL blocking (M-2): when data streams saturate the
// TCP buffer, control writes timeout instead of blocking indefinitely.
//
// 5s matches the yamux ConnectionWriteTimeout for consistency.
// A blocked control write for 5s indicates severe TCP congestion —
// at that point, the peer connection is degraded anyway.
const controlWriteDeadline = 5 * time.Second

// controlMsg is a union type carried through the per-peer write channel.
// It supports two modes:
//   - Structured: frame is non-nil → marshal + write (used by sendControl)
//   - Pre-marshaled: raw is non-nil → write directly (used by sendControlRaw)
//
// Only one of frame/raw should be set per message. If both are set,
// raw takes precedence (avoids redundant marshal).
type controlMsg struct {
	frame *pb.ControlFrame // structured frame — marshaled by the writer goroutine
	raw   []byte           // pre-marshaled bytes — written directly, no marshal
}

// newPeerConnWithWriter creates a peerConn with an async write queue
// and starts the dedicated writer goroutine. The writer drains the
// channel and writes frames to the control stream.
//
// The session parameter may be nil (used in tests without yamux).
func newPeerConnWithWriter(nodeID string, control net.Conn, session *yamux.Session, peerPubKey ed25519.PublicKey) *peerConn {
	pc := &peerConn{
		nodeID:      nodeID,
		control:     control,
		session:     session,
		peerPubKey:  peerPubKey,
		frameReader: pb.NewFrameReader(peerFrameBudget),
		writeCh:     make(chan controlMsg, controlWriteQueueSize),
		writerWg:    &sync.WaitGroup{},
		deadCh:      make(chan struct{}),
	}

	pc.writerWg.Add(1)
	go pc.controlWriteLoop()

	return pc
}

// sendControl enqueues a structured control frame for async delivery.
// The frame will be marshaled to bytes by the dedicated writer goroutine.
//
// Returns true if the frame was enqueued, false if it was dropped
// (queue full or writer stopped).
//
// This method NEVER blocks — it uses a non-blocking channel send.
// Dropped frames are silently discarded; gossip and discovery protocols
// are loss-tolerant and will naturally retry.
func (pc *peerConn) sendControl(frame *pb.ControlFrame) bool {
	pc.writeMu.RLock()
	defer pc.writeMu.RUnlock()

	if pc.writerStopped.Load() {
		return false
	}

	select {
	case pc.writeCh <- controlMsg{frame: frame}:
		return true
	default:
		slog.Debug("api: control write queue full, dropping frame",
			"peer", pc.nodeID,
		)
		return false
	}
}

// sendControlRaw enqueues pre-marshaled bytes for async delivery.
// The bytes are written directly to the control stream without
// re-marshaling. Use this for broadcasts where the same ControlFrame
// is sent to multiple peers — marshal once, then sendControlRaw to each.
//
// Returns true if enqueued, false if dropped (queue full or writer stopped).
// NEVER blocks.
func (pc *peerConn) sendControlRaw(data []byte) bool {
	pc.writeMu.RLock()
	defer pc.writeMu.RUnlock()

	if pc.writerStopped.Load() {
		return false
	}

	select {
	case pc.writeCh <- controlMsg{raw: data}:
		return true
	default:
		slog.Debug("api: control write queue full, dropping raw frame",
			"peer", pc.nodeID,
		)
		return false
	}
}

// controlWriteLoop is the dedicated writer goroutine for a peer's
// control stream. It drains the write channel and performs the actual
// blocking I/O in isolation — one slow peer only blocks its own
// writer goroutine, not the entire control plane.
//
// Supports two message modes:
//   - Structured (msg.frame): marshals via pb.WriteFrame
//   - Pre-marshaled (msg.raw): writes via pb.WriteRawFrame (zero re-marshal)
//
// Each write is guarded by controlWriteDeadline to mitigate HOL blocking
// (M-2). When data streams saturate the TCP buffer, control writes fail
// fast instead of blocking indefinitely.
func (pc *peerConn) controlWriteLoop() {
	defer pc.writerWg.Done()

	for msg := range pc.writeCh {
		var err error

		// Set write deadline to prevent HOL blocking (M-2).
		// If the underlying TCP buffer is saturated by data streams,
		// this ensures control writes fail within controlWriteDeadline
		// instead of blocking indefinitely.
		if deadline := controlWriteDeadline; deadline > 0 {
			if setErr := pc.control.SetWriteDeadline(time.Now().Add(deadline)); setErr != nil {
				slog.Debug("api: set write deadline failed",
					"peer", pc.nodeID, "error", setErr)
			}
		}

		if msg.raw != nil {
			// Pre-marshaled bytes — write directly. Zero marshal cost.
			err = pb.WriteRawFrame(pc.control, msg.raw)
		} else if msg.frame != nil {
			// Structured frame — marshal + write.
			err = pb.WriteFrame(pc.control, msg.frame)
		}

		if err != nil {
			slog.Debug("api: control write failed",
				"peer", pc.nodeID,
				"error", err,
			)
			// Write failure means the control stream is dead.
			// We can't call n.handlePeerDeath here because we don't have
			// a reference to the Node. Instead, we close the control
			// stream so the read loop (startControlLoop) immediately
			// detects the closure and fires handlePeerDeath.
			_ = pc.control.Close()
			return
		}
	}
}

// stopWriter signals the writer goroutine to stop and waits for it
// to drain. After stopWriter returns, sendControl and sendControlRaw
// will return false for all subsequent calls.
//
// Sequence:
//  1. Set stopped flag (prevents new sends)
//  2. Close the write channel (signals controlWriteLoop to exit after drain)
//  3. Close the control stream (unblocks any in-progress write)
//  4. Wait for the writer goroutine to finish
func (pc *peerConn) stopWriter() {
	pc.writeMu.Lock()
	// Mark the writer as stopped BEFORE closing the channel.
	// This ensures sendControl/sendControlRaw see the flag and avoid
	// sending on a closed channel.
	pc.writerStopped.Store(true)

	pc.writerOnce.Do(func() {
		close(pc.writeCh)
	})
	pc.writeMu.Unlock()

	// Close the control stream to unblock the writer goroutine if it's
	// stuck in a write on a slow/dead peer. Without this, Wait()
	// would deadlock when the writer is blocked on I/O.
	_ = pc.control.Close()

	pc.writerWg.Wait()
}

// broadcastAllPeers sends a structured control frame to all peers via
// their async write queues. Each peer's writer goroutine marshals
// independently. Use broadcastAllRaw for broadcasts that benefit from
// marshal-once semantics.
func broadcastAllPeers(pm *peerManager, frame *pb.ControlFrame) (sent, dropped int) {
	for _, pc := range pm.All() {
		if pc.sendControl(frame) {
			sent++
		} else {
			dropped++
		}
	}
	return
}

// broadcastExceptPeer sends a structured control frame to all peers
// except the excluded one, via their async write queues.
func broadcastExceptPeer(pm *peerManager, excludeNodeID string, frame *pb.ControlFrame) (sent, dropped int) {
	for _, pc := range pm.All() {
		if pc.nodeID == excludeNodeID {
			continue
		}
		if pc.sendControl(frame) {
			sent++
		} else {
			dropped++
		}
	}
	return
}

// broadcastAllRaw sends pre-marshaled bytes to all peers. The bytes
// are written directly without re-marshaling — the protobuf marshal
// happens exactly once in the caller, not N times in the writers.
//
// Use this for periodic broadcasts (gossip) where the same payload
// goes to every peer. Saves (N-1) protobuf marshals per broadcast.
func broadcastAllRaw(pm *peerManager, data []byte) (sent, dropped int) {
	for _, pc := range pm.All() {
		if pc.sendControlRaw(data) {
			sent++
		} else {
			dropped++
		}
	}
	return
}
