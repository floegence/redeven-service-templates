package cataloggen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommittedCatalogIsCurrent(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	if err := HasForbiddenCompatibilityCode(root); err != nil {
		t.Fatal(err)
	}
	output, err := Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(root, output); err != nil {
		t.Fatal(err)
	}
}

func TestCompatibilityDirectoryIsRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "compatibility"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(root); err == nil {
		t.Fatal("Generate accepted a compatibility directory")
	}
}
