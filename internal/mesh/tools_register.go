package mesh

// Tool plugin imports — each import triggers init() registration.
//
// To add a new tool to the binary, create a package under
// internal/registry/tools/<name>/ that implements registry.Tool
// and calls registry.Register() in init(), then add an import here.
//
// The PluginRegistry evaluates each tool's IsSupported() at startup.
// Unsupported tools are logged and excluded — zero runtime cost.
import (
	_ "github.com/flipfloptech/cortex-mcp/internal/registry/tools/buddyinfo"
	_ "github.com/flipfloptech/cortex-mcp/internal/registry/tools/cpupowerstate"
	_ "github.com/flipfloptech/cortex-mcp/internal/registry/tools/cputopology"
	_ "github.com/flipfloptech/cortex-mcp/internal/registry/tools/loadavg"
	_ "github.com/flipfloptech/cortex-mcp/internal/registry/tools/memoryinfo"
	_ "github.com/flipfloptech/cortex-mcp/internal/registry/tools/processlist"
	_ "github.com/flipfloptech/cortex-mcp/internal/registry/tools/processtree"
	_ "github.com/flipfloptech/cortex-mcp/internal/registry/tools/sysinfo"
	_ "github.com/flipfloptech/cortex-mcp/internal/registry/tools/threadwchan"
	_ "github.com/flipfloptech/cortex-mcp/internal/registry/tools/uptime"
)
