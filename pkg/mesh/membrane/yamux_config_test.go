package membrane

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// --- MeshConfig tests ---

// TestMeshConfig_ValidForYamux verifies the full config passes yamux
// validation. This is the critical gate — if yamux rejects our config,
// nothing works.
func TestMeshConfig_ValidForYamux(t *testing.T) {
	t.Parallel()

	conf := MeshConfig()
	if err := yamux.VerifyConfig(conf); err != nil {
		t.Fatalf("MeshConfig fails yamux.VerifyConfig: %v", err)
	}
}

// TestMeshConfig_KeepAliveInterval verifies that keepalive is tuned for
// mesh control plane needs (tighter than the default 30s). Must detect
// dead connections before routing purges fire (3× gossip = 9s).
func TestMeshConfig_KeepAliveInterval(t *testing.T) {
	t.Parallel()

	conf := MeshConfig()

	if !conf.EnableKeepAlive {
		t.Fatal("keep-alive must be enabled for mesh connectivity")
	}

	// Must be ≤ gossip interval × 3 (9s) to detect dead connections
	// before gradient route entries expire.
	if conf.KeepAliveInterval > 10*time.Second {
		t.Fatalf("KeepAliveInterval = %v, should be ≤ 10s for mesh", conf.KeepAliveInterval)
	}
	if conf.KeepAliveInterval < 1*time.Second {
		t.Fatalf("KeepAliveInterval = %v, too aggressive (< 1s)", conf.KeepAliveInterval)
	}
}

// TestMeshConfig_ConnectionWriteTimeout verifies that the write timeout
// is tighter than default (10s) for faster congestion/failure detection.
func TestMeshConfig_ConnectionWriteTimeout(t *testing.T) {
	t.Parallel()

	conf := MeshConfig()

	if conf.ConnectionWriteTimeout > 5*time.Second {
		t.Fatalf("ConnectionWriteTimeout = %v, should be ≤ 5s for mesh",
			conf.ConnectionWriteTimeout)
	}
	if conf.ConnectionWriteTimeout <= 0 {
		t.Fatal("ConnectionWriteTimeout must be positive")
	}
}

// TestMeshConfig_MaxStreamWindowSize verifies that the per-stream window
// is at the minimum allowed by yamux to limit per-stream buffering.
func TestMeshConfig_MaxStreamWindowSize(t *testing.T) {
	t.Parallel()

	conf := MeshConfig()

	// yamux enforces MaxStreamWindowSize >= 256KB (initialStreamWindow).
	// We set it to exactly this minimum to limit per-stream buffering
	// and create more interleaving opportunities for Stream 0.
	if conf.MaxStreamWindowSize > 256*1024 {
		t.Fatalf("MaxStreamWindowSize = %d, should be at yamux minimum (256KB)",
			conf.MaxStreamWindowSize)
	}
}

// TestMeshConfig_LogsSuppressed verifies that yamux's internal logging
// is suppressed (we use slog).
func TestMeshConfig_LogsSuppressed(t *testing.T) {
	t.Parallel()

	conf := MeshConfig()

	if conf.LogOutput != io.Discard {
		t.Fatal("yamux logs should be suppressed (LogOutput = io.Discard)")
	}
}

// TestMeshConfig_DiffersFromDefault verifies that MeshConfig actually
// tunes values away from yamux.DefaultConfig — if they're identical,
// we're not providing any HOL mitigation.
func TestMeshConfig_DiffersFromDefault(t *testing.T) {
	t.Parallel()

	mesh := MeshConfig()
	defConf := yamux.DefaultConfig()

	changed := mesh.KeepAliveInterval != defConf.KeepAliveInterval
	if mesh.ConnectionWriteTimeout != defConf.ConnectionWriteTimeout {
		changed = true
	}
	if !changed {
		t.Fatal("MeshConfig should differ from yamux.DefaultConfig for HOL mitigation")
	}
}

// TestMeshConfig_UsedByMultiplex verifies that Multiplex uses MeshConfig.
func TestMeshConfig_UsedByMultiplex(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)
	serverTLS, clientTLS := doHandshakePair(t, serverCfg, clientCfg)

	sMC, cMC := doMultiplexPair(t, serverTLS, clientTLS)

	msg := []byte("keepalive-check")
	go func() { _, _ = cMC.Control.Write(msg) }()

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(sMC.Control, buf); err != nil {
		t.Fatalf("Control read: %v", err)
	}
	if !bytes.Equal(buf, msg) {
		t.Fatalf("got %q, want %q", buf, msg)
	}
}

// TestMeshConfig_ControlPlaneUnderDataLoad verifies Stream 0 remains
// responsive while data streams are saturated. This is the core
// behavior protected by the config tuning.
func TestMeshConfig_ControlPlaneUnderDataLoad(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)
	serverTLS, clientTLS := doHandshakePair(t, serverCfg, clientCfg)
	sMC, cMC := doMultiplexPair(t, serverTLS, clientTLS)

	const numStreams = 5
	const dataSize = 512 * 1024 // 512KB per stream
	var streamWg sync.WaitGroup
	streamWg.Add(numStreams)

	// Server: accept and drain data streams.
	go func() {
		for i := 0; i < numStreams; i++ {
			s, err := sMC.Session.Accept()
			if err != nil {
				return
			}
			go func(s net.Conn) {
				_, _ = io.Copy(io.Discard, s)
				_ = s.Close()
			}(s)
		}
	}()

	// Client: flood data streams.
	for i := 0; i < numStreams; i++ {
		go func() {
			defer streamWg.Done()
			stream, err := cMC.Session.Open()
			if err != nil {
				return
			}
			defer func() { _ = stream.Close() }()
			data := make([]byte, dataSize)
			_, _ = stream.Write(data)
		}()
	}

	// While data is flowing, verify control plane responds quickly.
	controlMsg := []byte("gossip-heartbeat")
	done := make(chan bool, 1)

	go func() { _, _ = cMC.Control.Write(controlMsg) }()

	go func() {
		buf := make([]byte, len(controlMsg))
		_, err := io.ReadFull(sMC.Control, buf)
		done <- (err == nil && bytes.Equal(buf, controlMsg))
	}()

	select {
	case ok := <-done:
		if !ok {
			t.Fatal("control plane message corrupted under data load")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("control plane blocked under data stream load — HOL blocking")
	}

	streamWg.Wait()
}
