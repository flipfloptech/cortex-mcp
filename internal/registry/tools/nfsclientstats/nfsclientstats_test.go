package nfsclientstats

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestNFSClientStatsTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_nfs_client_stats" {
		t.Errorf("expected Name() == 'get_nfs_client_stats', got %q", tool.Name())
	}
	if tool.Category() != "storage" {
		t.Errorf("expected Category() == 'storage', got %q", tool.Category())
	}
	if tool.Parameters() != nil {
		t.Errorf("expected Parameters() to be nil")
	}
}

func TestParseNFSServers(t *testing.T) {
	t.Parallel()

	input := `NV SERVER   PORT USE HOSTNAME
v4 c0a8010a  801   1 192.168.1.10
`
	servers := parseNFSServers([]byte(input))
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}

	s := servers[0]
	if s.Version != "v4" || s.Server != "c0a8010a" || s.Port != 801 || s.Use != 1 || s.Hostname != "192.168.1.10" {
		t.Errorf("unexpected server parsing result: %+v", s)
	}
}

func TestParseNFSVolumes(t *testing.T) {
	t.Parallel()

	input := `NV SERVER   PORT    DEV     FSID    FSC
v4 86826879 801     0:46    0:0     no
`
	volumes := parseNFSVolumes([]byte(input))
	if len(volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(volumes))
	}

	v := volumes[0]
	if v.Version != "v4" || v.Device != "86826879" || v.FSID != "0:0" || v.FSCache != "no" {
		t.Errorf("unexpected volume parsing result: %+v", v)
	}
}

func TestParseNFSMountstats(t *testing.T) {
	t.Parallel()

	input := `device /dev/nvme0n1p2 mounted on / with fstype btrfs statvers=1.1
device nfs-server:/export/share mounted on /mnt/nfs with fstype nfs4 statvers=1.1
	opts:	rw,vers=4.2,rsize=1048576,wsize=1048576
	age:	123456
	xprt:	tcp 12345 0 1 0 0 1000 995 2 50 100 32 3
`
	mounts := parseNFSMountstats([]byte(input))
	if len(mounts) != 1 {
		t.Fatalf("expected 1 NFS mount, got %d", len(mounts))
	}

	m := mounts[0]
	if m.Device != "nfs-server:/export/share" || m.MountPoint != "/mnt/nfs" || m.FSType != "nfs4" || m.Age != 123456 {
		t.Errorf("unexpected mount metadata: %+v", m)
	}
	if m.Protocol != "tcp" || m.Sends != 1000 || m.Receives != 995 || m.BadXIDs != 2 || m.ActiveReqs != 50 || m.Backlog != 100 || m.MaxSlots != 32 || m.PendingReqs != 3 {
		t.Errorf("unexpected mount transport stats: %+v", m)
	}
}

func TestNFSClientStatsTool_Execute(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	procfs := filepath.Join(tmpDir, "proc")
	nfsfs := filepath.Join(procfs, "fs", "nfsfs")
	if err := os.MkdirAll(nfsfs, 0755); err != nil {
		t.Fatal(err)
	}

	serversData := `NV SERVER   PORT USE HOSTNAME
v4 c0a8010a  801   1 192.168.1.10
`
	volumesData := `NV SERVER   PORT    DEV     FSID    FSC
v4 86826879 801     0:46    0:0     no
`
	mountstatsData := `device nfs-server:/export/share mounted on /mnt/nfs with fstype nfs4 statvers=1.1
	age:	123456
	xprt:	tcp 12345 0 1 0 0 1000 995 2 50 100 32 3
`

	if err := os.WriteFile(filepath.Join(nfsfs, "servers"), []byte(serversData), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nfsfs, "volumes"), []byte(volumesData), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procfs, "mountstats"), []byte(mountstatsData), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &NFSClientStatsTool{
		procfsRoot: procfs,
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != registry.StatusOK {
		t.Fatalf("expected status OK, got %s. Summary: %s", res.Status, res.Summary)
	}

	var data NFSClientStatsData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if len(data.Servers) != 1 || len(data.Volumes) != 1 || len(data.Mounts) != 1 {
		t.Fatalf("unexpected data lengths: %+v", data)
	}

	if data.Servers[0].Hostname != "192.168.1.10" {
		t.Errorf("expected server hostname 192.168.1.10, got %s", data.Servers[0].Hostname)
	}
	if data.Volumes[0].FSCache != "no" {
		t.Errorf("expected fscache no, got %s", data.Volumes[0].FSCache)
	}
	if data.Mounts[0].Backlog != 100 {
		t.Errorf("expected backlog 100, got %d", data.Mounts[0].Backlog)
	}
}
