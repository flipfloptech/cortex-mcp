package buddyinfo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/memory"
)

type tool struct{}

func New() registry.Tool {
	return &tool{}
}

func (t *tool) Name() string {
	return "get_buddy_info"
}

func (t *tool) Description() string {
	return "Analyzes memory fragmentation by checking the availability of contiguous memory blocks."
}

func (t *tool) Help() string {
	return "Parses /proc/buddyinfo to calculate a fragmentation score (0-100, where 100 means highly fragmented) based on the availability of Order 4+ memory blocks."
}

func (t *tool) Category() string {
	return "memory"
}

func (t *tool) Hidden() bool {
	return false
}

func (t *tool) Parameters() []registry.ToolParam {
	return nil
}

func (t *tool) IsSupported() (bool, string) {
	if !memory.IsBuddyInfoSupported() {
		return false, "/proc/buddyinfo is missing or inaccessible"
	}
	return true, ""
}

func (t *tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {

	res, err := memory.GetBuddyInfo(ctx)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to get buddy info: %v", err)), nil
	}

	summary := fmt.Sprintf("Fragmentation Score: %d (Total Free: %d MB)", res.SystemSummary.FragmentationScore, res.SystemSummary.TotalFreeMB)
	return registry.NewResult(t.Name(), registry.StatusOK, summary, res), nil
}

func init() {
	registry.Register(New())
}
