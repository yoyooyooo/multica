package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCreateAgent_SkillsLocal_Validation pins the contract for the
// per-agent host-skill merge switch added with MUL-2603 / GitHub #3052:
//
//   - Field omitted → platform default "merge" is persisted (preserves the
//     pre-MUL-2603 inherit-from-machine behavior so existing personal
//     workflows that rely on locally installed skills keep working).
//   - "ignore" / "merge" → accepted and round-tripped.
//   - Any other value → 400 BadRequest. The DB CHECK would catch it as a
//     500 fallback, but we want a clean error for clients.
func TestCreateAgent_SkillsLocal_Validation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	claudeRuntimeID := createClaudeProviderRuntime(t)

	t.Cleanup(func() {
		testPool.Exec(ctx,
			`DELETE FROM agent WHERE workspace_id = $1 AND name LIKE 'skills-local-test-%'`,
			testWorkspaceID,
		)
	})

	t.Run("omitted defaults to merge", func(t *testing.T) {
		body := map[string]any{
			"name":                 "skills-local-test-default",
			"runtime_id":           claudeRuntimeID,
			"visibility":           "private",
			"max_concurrent_tasks": 1,
		}
		w := httptest.NewRecorder()
		testHandler.CreateAgent(w, newRequest(http.MethodPost, "/api/agents", body))
		if w.Code != http.StatusCreated {
			t.Fatalf("default skills_local: expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		_ = json.NewDecoder(w.Body).Decode(&resp)
		if resp["skills_local"] != "merge" {
			t.Errorf("expected skills_local=merge (platform default), got %v", resp["skills_local"])
		}
	})

	t.Run("explicit ignore round-trips", func(t *testing.T) {
		body := map[string]any{
			"name":                 "skills-local-test-ignore",
			"runtime_id":           claudeRuntimeID,
			"visibility":           "private",
			"max_concurrent_tasks": 1,
			"skills_local":         "ignore",
		}
		w := httptest.NewRecorder()
		testHandler.CreateAgent(w, newRequest(http.MethodPost, "/api/agents", body))
		if w.Code != http.StatusCreated {
			t.Fatalf("skills_local=ignore: expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		_ = json.NewDecoder(w.Body).Decode(&resp)
		if resp["skills_local"] != "ignore" {
			t.Errorf("expected skills_local=ignore, got %v", resp["skills_local"])
		}
	})

	t.Run("garbage value rejected with 400", func(t *testing.T) {
		body := map[string]any{
			"name":                 "skills-local-test-garbage",
			"runtime_id":           claudeRuntimeID,
			"visibility":           "private",
			"max_concurrent_tasks": 1,
			"skills_local":         "yes-please",
		}
		w := httptest.NewRecorder()
		testHandler.CreateAgent(w, newRequest(http.MethodPost, "/api/agents", body))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("garbage skills_local: expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// TestUpdateAgent_SkillsLocal_Tristate covers PATCH semantics: nil
// pointer means "no change", non-nil overrides after validation. Unlike
// thinking_level there is no "clear" mode — the column is NOT NULL.
func TestUpdateAgent_SkillsLocal_Tristate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	claudeRuntimeID := createClaudeProviderRuntime(t)
	agentID := createAgentOnRuntime(t, "skills-local-update-test", claudeRuntimeID, "")
	// Demote the seed row to "ignore" so we can verify both omit (preserve
	// ignore) and explicit-flip-back (set merge) paths.
	if _, err := testPool.Exec(ctx, `UPDATE agent SET skills_local = 'ignore' WHERE id = $1`, agentID); err != nil {
		t.Fatalf("seed skills_local=ignore: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID)
	})

	t.Run("omitted preserves existing value", func(t *testing.T) {
		body := map[string]any{
			"name": "skills-local-update-test-renamed",
		}
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPatch, "/api/agents/"+agentID, body), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("name-only update: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		_ = json.NewDecoder(w.Body).Decode(&resp)
		if resp["skills_local"] != "ignore" {
			t.Errorf("name-only update silently changed skills_local: got %v, want ignore", resp["skills_local"])
		}
	})

	t.Run("explicit merge flips back", func(t *testing.T) {
		body := map[string]any{
			"skills_local": "merge",
		}
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPatch, "/api/agents/"+agentID, body), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("update skills_local=merge: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		_ = json.NewDecoder(w.Body).Decode(&resp)
		if resp["skills_local"] != "merge" {
			t.Errorf("expected skills_local=merge after update, got %v", resp["skills_local"])
		}
	})

	t.Run("garbage value is 400 not silent default", func(t *testing.T) {
		body := map[string]any{
			"skills_local": "all-of-them",
		}
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPatch, "/api/agents/"+agentID, body), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("garbage skills_local: expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// TestNormalizeSkillsLocal_DriftStaysSafe is a unit-level safety net for
// the on-read coercion: if a row gets written with anything other than
// the two recognised values (hand edits, future schema drift, older
// daemons sending up garbage), the API must surface "merge" — the
// platform default — never leak an indeterminate value to clients or the
// daemon. Only the exact literal "ignore" maps to ignore; anything else
// coerces to the documented default.
func TestNormalizeSkillsLocal_DriftStaysSafe(t *testing.T) {
	cases := []struct {
		stored string
		want   string
	}{
		{"", "merge"},
		{"ignore", "ignore"},
		{"merge", "merge"},
		{"IGNORE", "merge"}, // case-sensitive — drift falls back to platform default
		{"yes", "merge"},
		{"true", "merge"},
	}
	for _, c := range cases {
		if got := normalizeSkillsLocal(c.stored); got != c.want {
			t.Errorf("normalizeSkillsLocal(%q) = %q, want %q", c.stored, got, c.want)
		}
	}
}
