package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkCoordinationHarnessRejectsUnavailableDB(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	script := filepath.Join(root, "scripts", "test-work-coordination-db-required.sh")
	cmd := exec.Command("bash", script)
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "DATABASE_URL=") {
			continue
		}
		env = append(env, item)
	}
	env = append(env, "DATABASE_URL=postgres://multica:multica@127.0.0.1:1/unavailable?sslmode=disable&connect_timeout=1")
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("DB-required harness unexpectedly succeeded without a database: %s", output)
	}
}
