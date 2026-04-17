package main

import (
	"context"
	"net"
	"testing"
	"time"
)

// --- upgradeRestartCommand tests ---
// The restart command should exec the binary with -self-install, not
// construct raw shell commands.

func TestUpgradeRestartCommand_Systemd(t *testing.T) {
	t.Parallel()

	cmd := upgradeRestartCommand("/opt/cortex-mesh/bin/cortex-mesh")
	expected := "/opt/cortex-mesh/bin/cortex-mesh -self-install"
	if cmd != expected {
		t.Errorf("expected %q, got %q", expected, cmd)
	}
}

// --- probeExistingNode tests ---
// probeExistingNode tries a quick TCP dial to see if the node is already
// listening. It does NOT do mTLS — that requires a membrane config that
// the example owns. It simply checks TCP reachability.

func TestProbeExistingNode_Unreachable(t *testing.T) {
	t.Parallel()

	// Dial a port that nothing is listening on.
	// Use a random high port that's almost certainly unused.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close() // Close immediately so nothing is listening.

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn := probeExistingNode(ctx, addr)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("probeExistingNode should return nil for unreachable host")
	}
}

func TestProbeExistingNode_Reachable(t *testing.T) {
	t.Parallel()

	// Start a listener to simulate an existing node.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = lis.Close() }()

	// Accept one connection in the background.
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Hold the connection open until test ends.
		<-time.After(5 * time.Second)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn := probeExistingNode(ctx, lis.Addr().String())
	if conn == nil {
		t.Fatal("probeExistingNode should return a connection for reachable host")
	}
	_ = conn.Close()
}

// --- needsUpgrade tests ---
// Determines whether we should push a new binary based on flag state.

func TestNeedsUpgrade_SkipDeployFalse(t *testing.T) {
	t.Parallel()

	// When skipDeploy is false, we always upload — so upgrade is needed.
	if !needsUpgrade(false) {
		t.Error("expected upgrade needed when skipDeploy=false")
	}
}

func TestNeedsUpgrade_SkipDeployTrue(t *testing.T) {
	t.Parallel()

	// When skipDeploy is true, skip the upload — no upgrade needed.
	if needsUpgrade(true) {
		t.Error("expected no upgrade when skipDeploy=true")
	}
}
