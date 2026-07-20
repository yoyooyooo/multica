package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkCoordinationDirectPackagesRejectMissingOrUnavailableRequiredDB(t *testing.T) {
	for _, tc := range []struct {
		name string
		pkg  string
		run  string
	}{
		{name: "migrate helper", pkg: "./cmd/migrate", run: "^TestWorkCoordinationMigrationRunner$"},
		{name: "handler TestMain", pkg: "./internal/handler", run: "^TestWorkCoordination"},
	} {
		for _, dbCase := range []struct {
			name  string
			dbURL string
		}{
			{name: "missing URL"},
			{name: "unreachable URL", dbURL: "postgres://required-test@127.0.0.1:1/unavailable?sslmode=disable&connect_timeout=1"},
		} {
			t.Run(tc.name+"/"+dbCase.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-run", tc.run, tc.pkg)
				cmd.Dir = filepath.Join("..", "..")
				cmd.Env = requiredDBEnv(dbCase.dbURL)
				output, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatalf("direct required package invocation unexpectedly succeeded: %s", output)
				}
				if ctx.Err() != nil {
					t.Fatalf("direct required package invocation timed out: %v", ctx.Err())
				}
			})
		}
	}
}

func TestWorkCoordinationHarnessRejectsUnavailableDB(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	script := filepath.Join(root, "scripts", "test-work-coordination-db-required.sh")
	cmd := exec.Command("bash", script)
	cmd.Env = requiredDBEnv("postgres://required-test@127.0.0.1:1/unavailable?sslmode=disable&connect_timeout=1")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("DB-required harness unexpectedly succeeded without a database: %s", output)
	}
}

func requiredDBEnv(dbURL string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "DATABASE_URL=") || strings.HasPrefix(item, "WORK_COORDINATION_DB_REQUIRED=") {
			continue
		}
		env = append(env, item)
	}
	env = append(env, "WORK_COORDINATION_DB_REQUIRED=1")
	if dbURL != "" {
		env = append(env, "DATABASE_URL="+dbURL)
	}
	return env
}
