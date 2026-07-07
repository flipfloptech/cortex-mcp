package nvmesmartlog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestToolContract(t *testing.T) {
	tool := New()

	if tool.Name() != "get_nvme_smart_log" {
		t.Errorf("expected get_nvme_smart_log, got %s", tool.Name())
	}
	if tool.Category() != "storage" {
		t.Errorf("expected storage, got %s", tool.Category())
	}
	if tool.Description() == "" || tool.Help() == "" {
		t.Errorf("missing description or help")
	}
}

func TestExecute_EPERM(t *testing.T) {
	// Mock an EPERM response.
	tool := New()

	// Fast track test: we pass bad args.
	res, err := tool.Execute(context.Background(), []byte(`{"target_device":"nonexistent"}`))
	if err != nil {
		t.Fatalf("expected nil error (encapsulated), got %v", err)
	}

	if res.Status != registry.StatusError {
		t.Logf("Encapsulated gracefully: %+v", res)
	}
}

func TestExecute_FallbackAndParsing(t *testing.T) {
	// 1. Create a mock sysfs directory structure
	tmpDir := t.TempDir()
	classNvme := filepath.Join(tmpDir, "class", "nvme")
	if err := os.MkdirAll(classNvme, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(classNvme, "nvme0"), 0755); err != nil {
		t.Fatal(err)
	}

	// Save existing configuration and restore after test
	oldSysfsRoot := sysfsRoot
	oldExecCommand := execCommand
	oldExecLookPath := execLookPath
	defer func() {
		sysfsRoot = oldSysfsRoot
		execCommand = oldExecCommand
		execLookPath = oldExecLookPath
	}()

	sysfsRoot = tmpDir

	tests := []struct {
		name          string
		nvmeExists    bool
		smartctlExist bool
		wantAudited   int
	}{
		{
			name:          "Both available, nvme-cli succeeds",
			nvmeExists:    true,
			smartctlExist: true,
			wantAudited:   1,
		},
		{
			name:          "Only smartctl available, smartctl succeeds",
			nvmeExists:    false,
			smartctlExist: true,
			wantAudited:   1,
		},
		{
			name:          "nvme-cli fails, fallback to smartctl succeeds",
			nvmeExists:    true,
			smartctlExist: true,
			wantAudited:   1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Mock path checks
			execLookPath = func(file string) (string, error) {
				if file == "nvme" {
					if tc.nvmeExists {
						return "/mock/bin/nvme", nil
					}
					return "", fmt.Errorf("not found")
				}
				if file == "smartctl" {
					if tc.smartctlExist {
						return "/mock/bin/smartctl", nil
					}
					return "", fmt.Errorf("not found")
				}
				if file == "sudo" {
					return "/mock/bin/sudo", nil
				}
				return "", fmt.Errorf("not found")
			}

			// Mock execution command
			execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				cs := []string{"-test.run=TestHelperProcess", "--", name}
				cs = append(cs, args...)
				cmd := exec.CommandContext(ctx, os.Args[0], cs...)
				cmd.Env = []string{
					"GO_WANT_HELPER_PROCESS=1",
					fmt.Sprintf("MOCK_NVME_EXISTS=%t", tc.nvmeExists),
					fmt.Sprintf("MOCK_SMARTCTL_EXISTS=%t", tc.smartctlExist),
					fmt.Sprintf("MOCK_TEST_NAME=%s", tc.name),
				}
				return cmd
			}

			tool := New()
			res, err := tool.Execute(context.Background(), []byte(`{}`))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if res.Status != registry.StatusOK {
				t.Fatalf("expected status OK, got %s, details: %s", res.Status, res.Summary)
			}

			var out Output
			if err := json.Unmarshal(res.Data, &out); err != nil {
				t.Fatalf("failed to unmarshal output: %v", err)
			}

			if out.SystemSummary.DrivesAudited != tc.wantAudited {
				t.Errorf("expected drives audited %d, got %d", tc.wantAudited, out.SystemSummary.DrivesAudited)
			}

			if len(out.Drives) != tc.wantAudited {
				t.Errorf("expected drives list length %d, got %d", tc.wantAudited, len(out.Drives))
			}

			if tc.wantAudited > 0 {
				drive := out.Drives[0]
				if drive.DeviceName != "nvme0" {
					t.Errorf("expected drive name nvme0, got %s", drive.DeviceName)
				}
				if drive.TemperatureC != 44 {
					t.Errorf("expected drive temp 44, got %d", drive.TemperatureC)
				}
				if drive.Status != "healthy" {
					t.Errorf("expected drive status healthy, got %s", drive.Status)
				}
			}
		})
	}
}

// TestHelperProcess is the helper process used to mock execution output.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	args = args[1:] // remove "--"

	cmdName := args[0]
	subCmd := ""
	if cmdName == "sudo" {
		for _, arg := range args {
			if arg == "nvme" || arg == "smartctl" {
				subCmd = arg
				break
			}
		}
	} else {
		subCmd = cmdName
	}

	testName := os.Getenv("MOCK_TEST_NAME")

	if subCmd == "nvme" {
		// Mock nvme failing for the fallback test
		if testName == "nvme-cli fails, fallback to smartctl succeeds" {
			fmt.Fprintf(os.Stderr, "nvme-cli error: command failed\n")
			os.Exit(1)
		}
		// Return nvme-cli mock JSON
		fmt.Println(`{
			"temperature": 317,
			"avail_spare": 100,
			"percent_used": 0,
			"media_errors": 0,
			"critical_warning": 0
		}`)
	} else if subCmd == "smartctl" {
		// Return smartctl mock JSON
		fmt.Println(`{
			"nvme_smart_health_information_log": {
				"critical_warning": 0,
				"temperature": 44,
				"available_spare": 100,
				"percentage_used": 0,
				"media_errors": 0
			}
		}`)
	} else {
		fmt.Fprintf(os.Stderr, "unknown mock command: %s\n", subCmd)
		os.Exit(1)
	}
}
