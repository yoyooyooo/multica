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
	for _, required := range []string{"name: multica-work-coordination", "multica coordination scope ensure", "multica coordination scope get", "multica coordination inspect", "multica coordination dependency add", "multica coordination dependency list", "multica coordination dependency resolve", "multica coordination blocker add", "multica coordination blocker list", "multica coordination blocker resolve", "Work coordination is passive", "does not own scheduling", "downstream blocked_by upstream", "fresh-key duplicate dependency add", "fresh-key duplicate blocker append creates a distinct evidence record", "1,000 active dependencies", "1,000 open blockers", "waiting_on_issue", "no_longer_blocking", "blocker resolution never resolves", "mutation revision conflict", "read-only repeatable-read snapshot", "receipt_ordinal DESC", "next_receipt_cursor", "Store cleanup/archive is not available", "Program scopes", "Agent dependency list returns only active pairs containing that task issue", "Agent blocker list returns only records containing that issue", "task-token Agent inspect requires the current task's actual root to equal the scope root"} {
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
	for _, required := range []string{
		"GET /api/coordination/scopes/{scopeId}/inspect",
		"service.CoordinationService.InspectScope",
		"handler.Handler.InspectCoordinationScope",
		"main.runCoordinationInspect",
		"db.GetMaxCoordinationReceiptOrdinalByScope",
		"db.ListCoordinationReceiptWindow",
		"db.ListCoordinationRecordIssueRefsByRecordIDs",
		"server/internal/service/coordination_inspect_test.go",
		"server/internal/handler/coordination_inspect_test.go",
		"server/cmd/multica/cmd_coordination_inspect_test.go",
		"server/cmd/server/work_coordination_cli_e2e_test.go",
	} {
		if !strings.Contains(sourceMap, required) {
			t.Fatalf("source map is missing %q", required)
		}
	}
	for _, anchor := range []struct{ path, symbol string }{
		{"server/internal/service/coordination_inspect.go", "func (s *CoordinationService) InspectScope"},
		{"server/internal/service/coordination_inspect.go", "pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}"},
		{"server/internal/handler/coordination_inspect.go", "func (h *Handler) InspectCoordinationScope"},
		{"server/cmd/multica/cmd_coordination_inspect.go", "func runCoordinationInspect"},
		{"server/pkg/db/queries/coordination.sql", "-- name: ListCoordinationReceiptWindow"},
		{"server/cmd/server/work_coordination_cli_e2e_test.go", "func TestWorkCoordinationCLIProcessesAggregatePassiveFlow"},
	} {
		content, err := os.ReadFile(filepath.Join(repositoryRoot, filepath.FromSlash(anchor.path)))
		if err != nil || !strings.Contains(string(content), anchor.symbol) {
			t.Fatalf("source anchor %s does not contain %q: %v", anchor.path, anchor.symbol, err)
		}
	}
	narrative, err := os.ReadFile(filepath.Join(repositoryRoot, "docs", "fork-features", "work-coordination-store", "README.md"))
	if err != nil {
		t.Fatalf("read fork narrative: %v", err)
	}
	for _, required := range []string{"Work Coordination Store V1–V5", "V1–V5 source is accepted", "V5 source acceptance does not authorize deployment", "passive-live-evidence.md"} {
		if !strings.Contains(string(narrative), required) {
			t.Fatalf("fork narrative is missing %q", required)
		}
	}
}
