package irqaffinity

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

var procInterrupts = "/proc/interrupts"

func init() {
	registry.Register(New())
}

// New returns a new IrqAffinityTool.
func New() *IrqAffinityTool {
	return &IrqAffinityTool{}
}

// IrqAffinityTool implements the get_irq_affinity diagnostic tool.
type IrqAffinityTool struct{}

// Name returns the unique tool identifier.
func (t *IrqAffinityTool) Name() string { return "get_irq_affinity" }

// Description returns a short summary for get_tool_list output.
func (t *IrqAffinityTool) Description() string {
	return "Get hardware interrupt distribution and CPU affinity"
}

// Help returns the full tool help text.
func (t *IrqAffinityTool) Help() string {
	return `get_irq_affinity — Hardware Interrupt Distribution

Provides a consolidated map of hardware interrupt distribution, identifying the highest-volume devices 
and the specific CPU cores burdened by them. Useful to diagnose thread jitter and sub-optimal 
irqbalance configurations.

Data Source: /proc/interrupts
Logic: Only returns CPUs that handle >1% of an IRQ's total volume. Returns the top 20 highest-volume IRQs.

Parameters: None`
}

// Category returns the tool category.
func (t *IrqAffinityTool) Category() string { return "compute" }

// Parameters returns the parameter schema.
func (t *IrqAffinityTool) Parameters() []registry.ToolParam { return nil }

// Hidden returns whether the tool is hidden from the MCP tool list.
func (t *IrqAffinityTool) Hidden() bool { return false }

// IsSupported checks if this tool can operate on the current node.
func (t *IrqAffinityTool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux"
	}
	if _, err := os.Stat(procInterrupts); err != nil {
		return false, "missing " + procInterrupts
	}
	return true, ""
}

// CpuBurden represents the total interrupts handled by a specific CPU.
type CpuBurden struct {
	CpuId           int `json:"cpu_id"`
	TotalInterrupts int `json:"total_interrupts"`
}

// SystemSummary holds OS-level aggregate interrupt stats.
type SystemSummary struct {
	TotalHardwareInterrupts int         `json:"total_hardware_interrupts"`
	MostBurdenedCpus        []CpuBurden `json:"most_burdened_cpus"`
}

// IrqSourceData holds the profile of a single IRQ.
type IrqSourceData struct {
	IrqNumber   string `json:"irq_number"`
	Type        string `json:"type"`
	TotalCount  int    `json:"total_count"`
	PrimaryCpus []int  `json:"primary_cpus"`
}

// IrqAffinityData is the JSON root struct.
type IrqAffinityData struct {
	SystemSummary SystemSummary            `json:"system_summary"`
	TopIrqSources map[string]IrqSourceData `json:"top_irq_sources"`
}

type irqRecord struct {
	Key  string
	Data IrqSourceData
}

// Execute gathers IRQ affinity data and returns a standardized result.
func (t *IrqAffinityTool) Execute(_ context.Context, _ json.RawMessage) (*registry.ToolResult, error) {

	file, err := os.Open(procInterrupts)
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)

	// Parse first line to count CPUs
	if !scanner.Scan() {
		return registry.NewErrorResult(t.Name(), "empty /proc/interrupts"), nil
	}
	headerFields := strings.Fields(scanner.Text())
	numCpus := len(headerFields)
	if numCpus == 0 {
		return registry.NewErrorResult(t.Name(), "failed to parse CPUs from header"), nil
	}

	cpuTotals := make([]int, numCpus)
	var records []irqRecord
	totalSystemInterrupts := 0

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)

		// A valid line must have the IRQ name and counts for each CPU
		if len(fields) < numCpus+1 {
			continue
		}

		irqStr := strings.TrimSuffix(fields[0], ":")

		totalCount := 0
		counts := make([]int, numCpus)
		for i := 0; i < numCpus; i++ {
			c, _ := strconv.Atoi(fields[i+1])
			counts[i] = c
			totalCount += c
			cpuTotals[i] += c
		}
		totalSystemInterrupts += totalCount

		if totalCount == 0 {
			continue
		}

		// Calculate 1% threshold
		threshold := totalCount / 100
		var primaryCpus []int
		for i, c := range counts {
			if c > threshold {
				primaryCpus = append(primaryCpus, i)
			}
		}

		// Determine type and key
		isNumeric := true
		for _, char := range irqStr {
			if char < '0' || char > '9' {
				isNumeric = false
				break
			}
		}

		var irqType string
		var key string

		if isNumeric {
			if len(fields) > numCpus+1 {
				irqType = fields[numCpus+1]
				if len(fields) > numCpus+2 {
					key = strings.Join(fields[numCpus+2:], " ")
				} else {
					key = irqStr // fallback if no device name
				}
			} else {
				key = irqStr
			}
		} else {
			key = irqStr
			if len(fields) > numCpus+1 {
				irqType = strings.Join(fields[numCpus+1:], " ")
			}
		}

		records = append(records, irqRecord{
			Key: key,
			Data: IrqSourceData{
				IrqNumber:   irqStr,
				Type:        irqType,
				TotalCount:  totalCount,
				PrimaryCpus: primaryCpus,
			},
		})
	}

	if err := scanner.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}

	// Sort IRQs by total volume descending and take Top 20
	sort.Slice(records, func(i, j int) bool {
		return records[i].Data.TotalCount > records[j].Data.TotalCount
	})

	topIrqSources := make(map[string]IrqSourceData)
	limit := 20
	if len(records) < 20 {
		limit = len(records)
	}
	for i := 0; i < limit; i++ {
		topIrqSources[records[i].Key] = records[i].Data
	}

	// Calculate top 5 most burdened CPUs
	var cpuBurdens []CpuBurden
	for i, total := range cpuTotals {
		if total > 0 {
			cpuBurdens = append(cpuBurdens, CpuBurden{
				CpuId:           i,
				TotalInterrupts: total,
			})
		}
	}
	sort.Slice(cpuBurdens, func(i, j int) bool {
		return cpuBurdens[i].TotalInterrupts > cpuBurdens[j].TotalInterrupts
	})

	cpuLimit := 5
	if len(cpuBurdens) < 5 {
		cpuLimit = len(cpuBurdens)
	}

	data := IrqAffinityData{
		SystemSummary: SystemSummary{
			TotalHardwareInterrupts: totalSystemInterrupts,
			MostBurdenedCpus:        cpuBurdens[:cpuLimit],
		},
		TopIrqSources: topIrqSources,
	}

	return registry.NewResult(t.Name(), registry.StatusOK, "IRQ Affinity Data", data), nil
}
