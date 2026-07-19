package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkCoordinationBuiltinSkillBundle(t *testing.T) {
	skill, ok := loadBuiltinSkill("multica-work-coordination")
	if !ok {
		t.Fatal("multica-work-coordination is not embedded")
	}
	for _, required := range []string{"name: multica-work-coordination", "multica coordination scope ensure", "multica coordination scope get", "multica coordination dependency add", "multica coordination dependency list", "multica coordination dependency resolve", "Work coordination is passive", "does not own scheduling", "downstream blocked_by upstream", "fresh-key duplicate add", "1,000 active dependencies", "mutation revision conflict", "Agent list returns only active pairs containing that task issue"} {
		if !strings.Contains(skill.Content, required) {
			t.Fatalf("built-in skill is missing %q", required)
		}
	}
	files := map[string]string{}
	for _, file := range skill.Files {
		files[file.Path] = file.Content
	}
	for _, required := range []string{"references/README.md", "references/work-coordination-source-map.md"} {
		if _, ok := files[required]; !ok {
			t.Fatalf("built-in skill is missing supporting file %s: %v", required, files)
		}
	}
	sourceMap := files["references/work-coordination-source-map.md"]
	repositoryRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	found := 0
	for _, line := range strings.Split(sourceMap, "\n") {
		path := strings.TrimSpace(line)
		if !strings.HasPrefix(path, "server/") && !strings.HasPrefix(path, "scripts/") {
			continue
		}
		found++
		if _, err := os.Stat(filepath.Join(repositoryRoot, filepath.FromSlash(path))); err != nil {
			t.Fatalf("source-map path %s is not real: %v", path, err)
		}
	}
	if found < 10 {
		t.Fatalf("source map has only %d concrete implementation anchors", found)
	}
}
