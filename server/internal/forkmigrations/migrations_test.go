package forkmigrations

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFilesUseIndependentOrderedStream(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"010_second.up.sql", "001_first.up.sql", "010_second.down.sql", "001_first.down.sql"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldLeaves := candidateLeaves
	candidateLeaves = []string{dir}
	t.Cleanup(func() { candidateLeaves = oldLeaves })

	up, err := Files("up")
	if err != nil {
		t.Fatal(err)
	}
	down, err := Files("down")
	if err != nil {
		t.Fatal(err)
	}
	gotUp := []string{filepath.Base(up[0]), filepath.Base(up[1])}
	gotDown := []string{filepath.Base(down[0]), filepath.Base(down[1])}
	if !reflect.DeepEqual(gotUp, []string{"001_first.up.sql", "010_second.up.sql"}) {
		t.Fatalf("up order = %v", gotUp)
	}
	if !reflect.DeepEqual(gotDown, []string{"010_second.down.sql", "001_first.down.sql"}) {
		t.Fatalf("down order = %v", gotDown)
	}
}
