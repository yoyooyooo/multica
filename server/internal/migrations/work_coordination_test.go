package migrations

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkCoordinationMigrationFiles(t *testing.T) {
	t.Parallel()

	dir := filepath.Clean(filepath.Join("..", "..", "migrations"))
	for n := 202; n <= 210; n++ {
		for _, direction := range []string{"up", "down"} {
			matches, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("%03d_coordination*.%s.sql", n, direction)))
			if err != nil {
				t.Fatalf("glob migration %03d %s: %v", n, direction, err)
			}
			if len(matches) != 1 {
				t.Fatalf("migration %03d %s files = %v, want exactly one", n, direction, matches)
			}
			body, err := os.ReadFile(matches[0])
			if err != nil {
				t.Fatalf("read %s: %v", matches[0], err)
			}
			upper := strings.ToUpper(string(body))
			for _, forbidden := range []string{"FOREIGN KEY", "REFERENCES ", "ON DELETE", "ON UPDATE"} {
				if strings.Contains(upper, forbidden) {
					t.Fatalf("%s contains forbidden relationship clause %q", matches[0], forbidden)
				}
			}
		}
	}

	structure := readWorkCoordinationMigration(t, dir, 202, "up")
	if strings.Contains(strings.ToUpper(structure), "PRIMARY KEY") || strings.Contains(strings.ToUpper(structure), " UNIQUE") {
		t.Fatal("202 structure migration must not create inline PK/UNIQUE constraints")
	}
	for n := 203; n <= 209; n++ {
		up := strings.TrimSpace(readWorkCoordinationMigration(t, dir, n, "up"))
		if strings.Count(up, ";") != 1 || !strings.Contains(strings.ToUpper(up), "INDEX CONCURRENTLY") {
			t.Fatalf("%03d up must be one concurrent-index statement: %q", n, up)
		}
		down := strings.TrimSpace(readWorkCoordinationMigration(t, dir, n, "down"))
		if strings.Count(down, ";") != 1 || !strings.Contains(strings.ToUpper(down), "DROP INDEX CONCURRENTLY IF EXISTS") {
			t.Fatalf("%03d down must be one concurrent-index drop: %q", n, down)
		}
	}
	attach := strings.ToUpper(readWorkCoordinationMigration(t, dir, 210, "up"))
	if !strings.Contains(attach, "PRIMARY KEY USING INDEX") || strings.Count(attach, "UNIQUE USING INDEX") != 2 {
		t.Fatal("210 must attach both primary keys and the two receipt unique constraints")
	}
	if strings.Contains(attach, "COORDINATION_SCOPE_ACTIVE_NATURAL_IDX") {
		t.Fatal("partial active-scope unique index must not be attached as a constraint")
	}
}

func readWorkCoordinationMigration(t *testing.T, dir string, n int, direction string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("%03d_coordination*.%s.sql", n, direction)))
	if err != nil || len(matches) != 1 {
		t.Fatalf("resolve migration %03d %s: matches=%v err=%v", n, direction, matches, err)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	return string(body)
}
