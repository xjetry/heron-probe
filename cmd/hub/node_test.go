package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeCreatePrintsRestartNotice(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = f
	defer func() { os.Stderr = old; f.Close() }()
	if err := runNode([]string{"create", "--db", filepath.Join(t.TempDir(), "t.db"), "--name", "new"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), restartNotice) {
		t.Fatalf("create missing restart notice: %s", b)
	}
}
