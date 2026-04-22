package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecuteOp_CopyFileAtomic(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "src")
	destPath := filepath.Join(tmpDir, "dest")

	err := os.WriteFile(srcPath, []byte("data"), 0644)
	if err != nil {
		t.Fatalf("write src: %v", err)
	}

	op := LifecycleOp{
		Action: "copy_file",
		Src:    srcPath,
		Path:   destPath,
	}

	if err := ExecuteOp(op); err != nil {
		t.Fatalf("ExecuteOp copy_file: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(data) != "data" {
		t.Errorf("expected 'data', got %q", string(data))
	}
}
