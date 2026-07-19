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
	for n := 202; n <= 230; n++ {
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

	for _, n := range []int{202, 211, 218} {
		structure := readWorkCoordinationMigration(t, dir, n, "up")
		if strings.Contains(strings.ToUpper(structure), "PRIMARY KEY") || strings.Contains(strings.ToUpper(structure), " UNIQUE") {
			t.Fatalf("%03d structure migration must not create inline PK/UNIQUE constraints", n)
		}
	}
	for _, n := range []int{203, 204, 205, 206, 207, 208, 209, 212, 213, 214, 215, 216, 219, 220, 221, 222, 223, 224, 225, 226, 227, 228, 229} {
		up := strings.TrimSpace(readWorkCoordinationMigration(t, dir, n, "up"))
		if strings.Count(up, ";") != 1 || !strings.Contains(strings.ToUpper(up), "INDEX CONCURRENTLY") {
			t.Fatalf("%03d up must be one concurrent-index statement: %q", n, up)
		}
		down := strings.TrimSpace(readWorkCoordinationMigration(t, dir, n, "down"))
		if strings.Count(down, ";") != 1 || !strings.Contains(strings.ToUpper(down), "DROP INDEX CONCURRENTLY IF EXISTS") {
			t.Fatalf("%03d down must be one concurrent-index drop: %q", n, down)
		}
	}
	v1Attach := strings.ToUpper(readWorkCoordinationMigration(t, dir, 210, "up"))
	if !strings.Contains(v1Attach, "PRIMARY KEY USING INDEX") || strings.Count(v1Attach, "UNIQUE USING INDEX") != 2 {
		t.Fatal("210 must attach both primary keys and the two receipt unique constraints")
	}
	if strings.Contains(v1Attach, "COORDINATION_SCOPE_ACTIVE_NATURAL_IDX") {
		t.Fatal("partial active-scope unique index must not be attached as a constraint")
	}
	v2Structure := strings.ToUpper(readWorkCoordinationMigration(t, dir, 211, "up"))
	for _, required := range []string{"COORDINATION_DEPENDENCY_SELF_CHECK", "COORDINATION_DEPENDENCY_CREATED_BY_TASK_CHECK", "COORDINATION_DEPENDENCY_RESOLUTION_CHECK"} {
		if !strings.Contains(v2Structure, required) {
			t.Fatalf("211 missing %s", required)
		}
	}
	v2Pair := strings.ToUpper(readWorkCoordinationMigration(t, dir, 213, "up"))
	if !strings.Contains(v2Pair, "UNIQUE INDEX CONCURRENTLY") || !strings.Contains(v2Pair, "WHERE RESOLVED_AT IS NULL") || strings.Contains(v2Pair, "COORDINATION_SCOPE_ID") {
		t.Fatal("213 must be the workspace-global active pair unique index")
	}
	v2Attach := strings.ToUpper(readWorkCoordinationMigration(t, dir, 217, "up"))
	if !strings.Contains(v2Attach, "PRIMARY KEY USING INDEX") || strings.Contains(v2Attach, "UNIQUE USING INDEX") || strings.Contains(v2Attach, "ACTIVE_PAIR") {
		t.Fatal("217 must attach only the dependency primary key")
	}
	v3Structure := strings.ToUpper(readWorkCoordinationMigration(t, dir, 218, "up"))
	for _, required := range []string{"CREATE TABLE COORDINATION_RECORD", "CREATE TABLE COORDINATION_RECORD_ISSUE_REF", "COORDINATION_RECORD_RESOLUTION_STATE_CHECK", "WAITING_ON_ISSUE", "NO_LONGER_BLOCKING", "SUPERSEDED"} {
		if !strings.Contains(v3Structure, required) {
			t.Fatalf("218 missing %s", required)
		}
	}
	for _, forbidden := range []string{"JSONB", "PAYLOAD", "METADATA", " URL"} {
		if strings.Contains(v3Structure, forbidden) {
			t.Fatalf("218 contains forbidden free-form storage marker %s", forbidden)
		}
	}
	v3Attach := strings.ToUpper(readWorkCoordinationMigration(t, dir, 230, "up"))
	if strings.Count(v3Attach, "PRIMARY KEY USING INDEX") != 2 || strings.Count(v3Attach, "UNIQUE USING INDEX") != 2 {
		t.Fatal("230 must attach both primary keys and both typed-ref unique constraints")
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
