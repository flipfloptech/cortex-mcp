// Package main provides the subcommand parser for the mesh-example binary.
//
// Subcommands replace legacy flag-based lifecycle dispatch:
//
//	./mesh-example install [node_id]   — persist nodes as systemd services
//	./mesh-example uninstall [node_id] — remove nodes (ephemeral or persistent)
//	./mesh-example start [node_id]     — start persistent services via SSH
//	./mesh-example stop [node_id]      — stop persistent services via mesh/SSH
//	./mesh-example bridge <addr>       — raw TCP bridge for firewall traversal
//	./mesh-example daemon              — run as persistent daemon (systemd entry)
//	./mesh-example serve               — ephemeral fleet node (SelfDeployer sets this)
//	./mesh-example                     — default: connect or deploy ephemerally
package main

// subcommand represents a parsed CLI subcommand with optional target.
type subcommand struct {
	// Name is the subcommand name: "install", "uninstall", "start", "stop",
	// "bridge", "daemon", "serve", or "" for default mode.
	Name string

	// Target is the optional node ID or address argument.
	// Empty means "all nodes".
	Target string
}

// parseSubcommand extracts the subcommand and optional target from os.Args.
// It expects args to be os.Args[1:] AFTER flag parsing (i.e., flag.Args()).
//
// Examples:
//
//	[]                    → {Name: "", Target: ""}
//	["install"]           → {Name: "install", Target: ""}
//	["install", "oss1"]   → {Name: "install", Target: "oss1"}
//	["bridge", "0.0.0.0"] → {Name: "bridge", Target: "0.0.0.0"}
//	["daemon"]            → {Name: "daemon", Target: ""}
func parseSubcommand(args []string) subcommand {
	if len(args) == 0 {
		return subcommand{}
	}

	name := args[0]
	switch name {
	case "install", "uninstall", "start", "stop", "bridge", "daemon", "serve":
		cmd := subcommand{Name: name}
		if len(args) > 1 {
			cmd.Target = args[1]
		}
		return cmd
	default:
		// Unknown arg — treat as default mode.
		return subcommand{}
	}
}
