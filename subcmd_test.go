package main

import (
	"testing"
)

func TestParseSubcommand_Empty(t *testing.T) {
	t.Parallel()

	cmd := parseSubcommand(nil)
	if cmd.Name != "" {
		t.Errorf("expected empty name, got %q", cmd.Name)
	}
	if cmd.Target != "" {
		t.Errorf("expected empty target, got %q", cmd.Target)
	}
}

func TestParseSubcommand_Install(t *testing.T) {
	t.Parallel()

	cmd := parseSubcommand([]string{"install"})
	if cmd.Name != "install" {
		t.Errorf("expected name=install, got %q", cmd.Name)
	}
	if cmd.Target != "" {
		t.Errorf("expected empty target, got %q", cmd.Target)
	}
}

func TestParseSubcommand_InstallWithTarget(t *testing.T) {
	t.Parallel()

	cmd := parseSubcommand([]string{"install", "oss1"})
	if cmd.Name != "install" {
		t.Errorf("expected name=install, got %q", cmd.Name)
	}
	if cmd.Target != "oss1" {
		t.Errorf("expected target=oss1, got %q", cmd.Target)
	}
}

func TestParseSubcommand_AllSubcommands(t *testing.T) {
	t.Parallel()

	names := []string{"install", "uninstall", "start", "stop", "bridge", "daemon", "serve"}
	for _, name := range names {
		cmd := parseSubcommand([]string{name})
		if cmd.Name != name {
			t.Errorf("expected name=%s, got %q", name, cmd.Name)
		}
	}
}

func TestParseSubcommand_UnknownArg(t *testing.T) {
	t.Parallel()

	// Unknown args should be treated as default mode.
	cmd := parseSubcommand([]string{"bogus-thing"})
	if cmd.Name != "" {
		t.Errorf("expected empty name for unknown arg, got %q", cmd.Name)
	}
}

func TestParseSubcommand_BridgeWithAddress(t *testing.T) {
	t.Parallel()

	cmd := parseSubcommand([]string{"bridge", "localhost:4443"})
	if cmd.Name != "bridge" {
		t.Errorf("expected name=bridge, got %q", cmd.Name)
	}
	if cmd.Target != "localhost:4443" {
		t.Errorf("expected target=localhost:4443, got %q", cmd.Target)
	}
}

func TestParseSubcommand_StopWithNodeID(t *testing.T) {
	t.Parallel()

	cmd := parseSubcommand([]string{"stop", "mds-01"})
	if cmd.Name != "stop" {
		t.Errorf("expected name=stop, got %q", cmd.Name)
	}
	if cmd.Target != "mds-01" {
		t.Errorf("expected target=mds-01, got %q", cmd.Target)
	}
}
