package systemdstatus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestSystemdStatusTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_systemd_status" {
		t.Errorf("expected Name() == 'get_systemd_status', got %q", tool.Name())
	}
	if tool.Category() != "system" {
		t.Errorf("expected Category() == 'system', got %q", tool.Category())
	}
	params := tool.Parameters()
	if len(params) != 2 {
		t.Errorf("expected 2 parameters, got %d", len(params))
	}
}

func TestParseSystemdShow(t *testing.T) {
	t.Parallel()

	input := `Id=ssh.service
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
Description=OpenBSD Secure Shell server
MainPID=1234

Id=docker.service
LoadState=loaded
ActiveState=inactive
SubState=dead
UnitFileState=disabled
Description=Docker Application Container Engine
MainPID=0`

	services := parseSystemdShow([]byte(input))
	if len(services) != 2 {
		t.Fatalf("expected 2 services parsed, got %d", len(services))
	}

	if services[0].ID != "ssh.service" || services[0].LoadState != "loaded" || services[0].ActiveState != "active" || services[0].SubState != "running" || services[0].UnitFileState != "enabled" || services[0].Description != "OpenBSD Secure Shell server" || services[0].MainPID != 1234 {
		t.Errorf("unexpected parsed service 0: %+v", services[0])
	}

	if services[1].ID != "docker.service" || services[1].LoadState != "loaded" || services[1].ActiveState != "inactive" || services[1].SubState != "dead" || services[1].UnitFileState != "disabled" || services[1].Description != "Docker Application Container Engine" || services[1].MainPID != 0 {
		t.Errorf("unexpected parsed service 1: %+v", services[1])
	}
}

func TestParseListUnits(t *testing.T) {
	t.Parallel()

	input := `● certbot.service loaded failed failed Certbot renewal
  ssh.service     loaded active running OpenBSD Secure Shell server`

	services := parseListUnits([]byte(input))
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}

	if services[0].ID != "certbot.service" || services[0].LoadState != "loaded" || services[0].ActiveState != "failed" || services[0].SubState != "failed" || services[0].Description != "Certbot renewal" {
		t.Errorf("unexpected service 0: %+v", services[0])
	}

	if services[1].ID != "ssh.service" || services[1].LoadState != "loaded" || services[1].ActiveState != "active" || services[1].SubState != "running" || services[1].Description != "OpenBSD Secure Shell server" {
		t.Errorf("unexpected service 1: %+v", services[1])
	}
}

func TestSystemdStatusTool_Execute(t *testing.T) {
	t.Parallel()

	tool := &QuerySystemdStatusTool{
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "systemctl" {
				return nil, errors.New("expected systemctl command")
			}

			// verify show specific units
			if args[0] == "show" {
				return []byte(`Id=ssh.service
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
Description=OpenBSD Secure Shell server
MainPID=1234`), nil
			}

			// verify list failed units
			if args[0] == "list-units" {
				for _, arg := range args {
					if arg == "--state=failed" {
						return []byte("● certbot.service loaded failed failed Certbot renewal"), nil
					}
					if arg == "--type=service" {
						return []byte("  ssh.service     loaded active running OpenBSD Secure Shell server"), nil
					}
				}
			}

			return nil, errors.New("unexpected systemctl arguments")
		},
	}

	// 1. Check specific service status
	{
		args, _ := json.Marshal(map[string]interface{}{
			"services": []string{"ssh"},
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		if res.Status != registry.StatusOK {
			t.Fatalf("expected status OK, got %s", res.Status)
		}

		var data SystemdStatusData
		_ = json.Unmarshal(res.Data, &data)

		if len(data.Services) != 1 || data.Services[0].ID != "ssh.service" {
			t.Errorf("expected 1 service (ssh.service), got %+v", data.Services)
		}
	}

	// 2. Check failed service discovery (default behavior if no services requested)
	{
		res, err := tool.Execute(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}

		var data SystemdStatusData
		_ = json.Unmarshal(res.Data, &data)

		if len(data.Failed) != 1 || data.Failed[0].ID != "certbot.service" {
			t.Errorf("expected 1 failed service (certbot.service), got %+v", data.Failed)
		}
	}

	// 3. Check all service listing
	{
		args, _ := json.Marshal(map[string]interface{}{
			"list_all": true,
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		var data SystemdStatusData
		_ = json.Unmarshal(res.Data, &data)

		if len(data.Services) != 1 || data.Services[0].ID != "ssh.service" {
			t.Errorf("expected 1 service listed, got %+v", data.Services)
		}
	}
}
