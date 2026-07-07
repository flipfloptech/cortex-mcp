package moduleinfo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestKernelModuleInfoTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_kernel_module_info" {
		t.Errorf("expected Name() == 'get_kernel_module_info', got %q", tool.Name())
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("expected Category() == 'system', got %q", tool.Category())
	}
	params := tool.Parameters()
	if len(params) != 1 {
		t.Errorf("expected 1 parameter, got %d", len(params))
	}
	if params[0].Name != "module_name" || !params[0].Required {
		t.Errorf("expected required parameter 'module_name'")
	}
}

func TestParseModinfo(t *testing.T) {
	t.Parallel()

	input := `filename:       /lib/modules/kernel/drivers/uinput.ko
author:         Aristeu Sergio
description:    User level driver support
license:        GPL
alias:          devname:uinput
alias:          char-major-10-223
depends:        input-core,udev
srcversion:     20CBEBB74489362
custom_field:   some_value`

	info := parseModinfo([]byte(input))

	if info.Filename != "/lib/modules/kernel/drivers/uinput.ko" {
		t.Errorf("unexpected filename: %q", info.Filename)
	}
	if info.Author != "Aristeu Sergio" {
		t.Errorf("unexpected author: %q", info.Author)
	}
	if info.Description != "User level driver support" {
		t.Errorf("unexpected description: %q", info.Description)
	}
	if info.License != "GPL" {
		t.Errorf("unexpected license: %q", info.License)
	}
	if info.SrcVersion != "20CBEBB74489362" {
		t.Errorf("unexpected srcversion: %q", info.SrcVersion)
	}
	if len(info.Depends) != 2 || info.Depends[0] != "input-core" || info.Depends[1] != "udev" {
		t.Errorf("unexpected depends: %+v", info.Depends)
	}
	if len(info.Aliases) != 2 || info.Aliases[0] != "devname:uinput" || info.Aliases[1] != "char-major-10-223" {
		t.Errorf("unexpected aliases: %+v", info.Aliases)
	}
	if info.StaticProperties["custom_field"] != "some_value" {
		t.Errorf("unexpected custom_field: %q", info.StaticProperties["custom_field"])
	}
}

func TestKernelModuleInfoTool_Execute(t *testing.T) {
	t.Parallel()

	tool := &QueryKernelModuleInfoTool{
		dirExists: func(path string) bool {
			return path == "/sys/module/uinput"
		},
		readSysfsFile: func(path string) ([]byte, error) {
			switch path {
			case "/sys/module/uinput/coresize":
				return []byte("28672\n"), nil
			case "/sys/module/uinput/initsize":
				return []byte("0\n"), nil
			case "/sys/module/uinput/initstate":
				return []byte("live\n"), nil
			case "/sys/module/uinput/refcnt":
				return []byte("1\n"), nil
			case "/sys/module/uinput/taint":
				return []byte("\n"), nil
			}
			return nil, errors.New("file not found")
		},
		readSysfsHolders: func(path string) ([]string, error) {
			if path == "/sys/module/uinput/holders" {
				return []string{"holder1", "holder2"}, nil
			}
			return nil, errors.New("holders dir not found")
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "modinfo" && len(args) == 1 && args[0] == "uinput" {
				return []byte(`filename:       /lib/modules/kernel/drivers/uinput.ko
author:         Aristeu Sergio
description:    User level driver support
license:        GPL`), nil
			}
			return nil, errors.New("modinfo failed")
		},
	}

	// Execute for loaded module
	{
		args, _ := json.Marshal(map[string]interface{}{
			"module_name": "uinput",
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		if res.Status != registry.StatusOK {
			t.Fatalf("expected status OK, got %s", res.Status)
		}

		var data ModuleInfoData
		_ = json.Unmarshal(res.Data, &data)

		if data.Name != "uinput" || data.Author != "Aristeu Sergio" || data.Filename != "/lib/modules/kernel/drivers/uinput.ko" {
			t.Errorf("unexpected module metadata: %+v", data)
		}

		if data.LiveState == nil {
			t.Fatal("expected live state to be populated")
		}

		if data.LiveState.CoreSize != 28672 || data.LiveState.RefCount != 1 || data.LiveState.InitState != "live" || len(data.LiveState.Holders) != 2 || data.LiveState.Holders[0] != "holder1" {
			t.Errorf("unexpected live state properties: %+v", data.LiveState)
		}
	}

	// Execute for non-existent module
	{
		args, _ := json.Marshal(map[string]interface{}{
			"module_name": "nonexistent",
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		if res.Status != registry.StatusError {
			t.Errorf("expected status Error for nonexistent module, got %s", res.Status)
		}
	}
}
