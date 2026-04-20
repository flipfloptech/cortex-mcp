package harness

import (
	"context"
	"encoding/json"
	"math/rand"
	"sync"
	"time"

	"github.com/cortex-mesh/cortex-mesh/tools"
	"go.uber.org/zap"
)

// Dispatcher represents the ability to dispatch a tool call to the mesh.
// It aligns with mcp.Dispatcher or gateway.Gateway.
type Dispatcher interface {
	Dispatch(ctx context.Context, name string, rawMessage json.RawMessage) (*tools.ToolResult, error)
}

// FleetDeployer abstracts the ability to deploy and uninstall nodes.
type FleetDeployer interface {
	Deploy(ctx context.Context) error
	Uninstall(ctx context.Context) error
}

// ToolReport represents the success/failure state of a specific tool call.
type ToolReport struct {
	ToolName string
	NodeID   string
	Success  bool
	Error    string
	Duration time.Duration
}

// CheckReport holds the results of a single pass of all tools.
type CheckReport struct {
	Total      int
	Successful int
	Failed     int
	Reports    []ToolReport
}

// Harness defines the soak test and validation logic.
type Harness struct {
	dispatcher Dispatcher
}

// NewHarness creates a new test harness.
func NewHarness(d Dispatcher) *Harness {
	return &Harness{
		dispatcher: d,
	}
}

// RunToolCheck calls all tools one time on all available nodes.
func (h *Harness) RunToolCheck(ctx context.Context, toolsList []string, nodeIDs []string) CheckReport {
	report := CheckReport{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, nodeID := range nodeIDs {
		for _, toolName := range toolsList {
			wg.Add(1)
			go func(n, t string) {
				defer wg.Done()
				start := time.Now()

				// Build dispatch request with specific node targeting
				rawBytes, _ := json.Marshal(map[string]interface{}{
					"name":      t,
					"node_name": n,
				})

				res, err := h.dispatcher.Dispatch(ctx, "call_tool", rawBytes)
				dur := time.Since(start)

				mu.Lock()
				defer mu.Unlock()
				report.Total++

				tr := ToolReport{
					ToolName: t,
					NodeID:   n,
					Duration: dur,
				}

				if err != nil {
					tr.Success = false
					tr.Error = err.Error()
					report.Failed++
				} else if res != nil && res.IsError {
					tr.Success = false
					tr.Error = string(res.Content)
					report.Failed++
				} else {
					tr.Success = true
					report.Successful++
				}
				report.Reports = append(report.Reports, tr)

			}(nodeID, toolName)
		}
	}

	wg.Wait()
	return report
}

// RunToolSoak continually calls tools randomly until count or duration is hit.
func (h *Harness) RunToolSoak(ctx context.Context, toolsList []string, nodeIDs []string, count int, duration time.Duration) CheckReport {
	report := CheckReport{}

	if len(toolsList) == 0 || len(nodeIDs) == 0 {
		return report
	}

	deadline := time.Now().Add(duration)
	if duration == 0 {
		// Practically infinite if duration is 0, bounded by count
		deadline = time.Now().Add(8760 * time.Hour)
	}

	var mu sync.Mutex
	var wg sync.WaitGroup

	// We'll use a semaphore to limit concurrency
	sem := make(chan struct{}, 10)

Loop:
	for i := 0; (count == 0 || i < count) && time.Now().Before(deadline); i++ {
		select {
		case <-ctx.Done():
			break Loop
		default:
		}

		nodeID := nodeIDs[rand.Intn(len(nodeIDs))]
		toolName := toolsList[rand.Intn(len(toolsList))]

		sem <- struct{}{}
		wg.Add(1)

		go func(n, t string) {
			defer wg.Done()
			defer func() { <-sem }()

			start := time.Now()
			rawBytes, _ := json.Marshal(map[string]interface{}{
				"name":      t,
				"node_name": n,
			})

			res, err := h.dispatcher.Dispatch(ctx, "call_tool", rawBytes)
			dur := time.Since(start)

			mu.Lock()
			defer mu.Unlock()
			report.Total++

			tr := ToolReport{
				ToolName: t,
				NodeID:   n,
				Duration: dur,
			}

			if err != nil {
				tr.Success = false
				tr.Error = err.Error()
				report.Failed++
			} else if res != nil && res.IsError {
				tr.Success = false
				tr.Error = string(res.Content)
				report.Failed++
			} else {
				tr.Success = true
				report.Successful++
			}
			report.Reports = append(report.Reports, tr)

			if report.Total%100 == 0 {
				zap.S().Infof("Soak progress: %d calls completed (%d failed)", report.Total, report.Failed)
			}
		}(nodeID, toolName)

		// Small sleep to prevent tight CPU loop
		time.Sleep(10 * time.Millisecond)
	}

	wg.Wait()
	return report
}

// RunDeploySoak continually deploys and uninstalls the fleet until count or duration is hit.
func (h *Harness) RunDeploySoak(ctx context.Context, deployer FleetDeployer, count int, duration time.Duration) CheckReport {
	report := CheckReport{}

	deadline := time.Now().Add(duration)
	if duration == 0 {
		deadline = time.Now().Add(8760 * time.Hour)
	}

	for i := 0; (count == 0 || i < count) && time.Now().Before(deadline); i++ {
		select {
		case <-ctx.Done():
			return report
		default:
		}

		start := time.Now()

		// 1. Deploy
		deployErr := deployer.Deploy(ctx)

		// 2. Wait a bit for things to settle if it succeeded
		if deployErr == nil {
			time.Sleep(2 * time.Second)
		}

		// 3. Uninstall
		uninstallErr := deployer.Uninstall(ctx)

		dur := time.Since(start)

		report.Total++
		tr := ToolReport{
			ToolName: "deploy_cycle",
			Duration: dur,
		}

		if deployErr != nil {
			tr.Success = false
			tr.Error = "deploy error: " + deployErr.Error()
			report.Failed++
		} else if uninstallErr != nil {
			tr.Success = false
			tr.Error = "uninstall error: " + uninstallErr.Error()
			report.Failed++
		} else {
			tr.Success = true
			report.Successful++
		}

		report.Reports = append(report.Reports, tr)

		zap.S().Infof("Deploy soak cycle %d completed (success=%v)", report.Total, tr.Success)
	}

	return report
}
