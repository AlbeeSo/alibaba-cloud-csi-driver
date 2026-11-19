//go:build linux

package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	mountinfo "github.com/moby/sys/mountinfo"
)

func TestFuseConnectionIDParsesPastUnderlyingBind(t *testing.T) {
	const table = "100 1 253:3 /data /target rw - ext4 /dev/root rw\n101 100 0:386 / /target rw - fuse ossfs2 rw\n102 1 0:777 / /another-pod rw - fuse ossfs2 rw\n"
	infos, err := mountinfo.GetMountsFromReader(strings.NewReader(table), exactMountEntries("/target"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := fuseConnectionID(infos)
	if err != nil || id != 386 {
		t.Fatalf("must select the propagated FUSE connection, got %d: %v", id, err)
	}
}

func TestFuseConnectionIDIgnoresUnderlyingPVCBind(t *testing.T) {
	for _, infos := range [][]*mountinfo.Info{
		{{FSType: "ext4", Minor: 3}, {FSType: "fuse", Minor: 386}},
		{{FSType: "fuse.ossfs2", Minor: 386}, {FSType: "ext4", Minor: 3}},
		{{FSType: "fuse.virtiofs", Minor: 2}, {FSType: "fuse.ossfs2", Minor: 386}},
	} {
		id, err := fuseConnectionID(infos)
		if err != nil || id != 386 {
			t.Fatalf("selected connection %d: %v", id, err)
		}
	}
	for _, infos := range [][]*mountinfo.Info{nil, {{FSType: "ext4", Minor: 3}}, {{FSType: "fuse.virtiofs", Minor: 2}}, {{FSType: "fuse", Minor: 386}, {FSType: "fuse", Minor: 387}}} {
		if _, err := fuseConnectionID(infos); err == nil {
			t.Fatal("must not flush an unverified connection")
		}
	}
}

func TestFlushFuseConnection(t *testing.T) {
	tmp := t.TempDir()
	origDir := FuseConnectionsDir
	FuseConnectionsDir = tmp
	t.Cleanup(func() { FuseConnectionsDir = origDir })

	connID := uint64(42)
	connDir := filepath.Join(tmp, "42")
	if err := os.MkdirAll(connDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := FlushFuseConnection(connID); err != nil {
		t.Fatalf("FlushFuseConnection() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(connDir, "flush"))
	if err != nil {
		t.Fatalf("read flush file: %v", err)
	}
	if string(data) != "1" {
		t.Errorf("flush file content = %q, want %q", string(data), "1")
	}
}

func TestFlushFuseConnection_NoDir(t *testing.T) {
	tmp := t.TempDir()
	origDir := FuseConnectionsDir
	FuseConnectionsDir = tmp
	t.Cleanup(func() { FuseConnectionsDir = origDir })

	err := FlushFuseConnection(123)
	if err == nil {
		t.Fatal("expected error when connection dir does not exist")
	}
}

func TestGetFuseConnectionID_NotMounted(t *testing.T) {
	_, err := GetFuseConnectionID("/nonexistent/path/that/is/not/mounted")
	if err == nil {
		t.Fatal("expected error for non-mounted path")
	}
}
