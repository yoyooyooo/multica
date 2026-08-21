package daemon

import (
	"strings"
	"testing"
)

func TestPlatformGitEmailStableAcrossRenameShape(t *testing.T) {
	t.Parallel()

	agentID := "550e8400-e29b-41d4-a716-446655440000"
	got := platformGitEmail(agentID)
	want := "agent-550e8400e29b41d4a716446655440000@users.noreply.multica.local"
	if got != want {
		t.Fatalf("platformGitEmail = %q, want %q", got, want)
	}
	// Uppercase input must not change the stable email.
	if upper := platformGitEmail(strings.ToUpper(agentID)); upper != want {
		t.Fatalf("platformGitEmail uppercase = %q, want %q", upper, want)
	}
	if platformGitEmail("") != "" {
		t.Fatal("empty agent id must not produce an email")
	}
}

func TestApplyPlatformGitIdentityUsesAgentSnapshotAndBlocksCustomEnvOverride(t *testing.T) {
	t.Parallel()

	agentID := "11111111-2222-3333-4444-555555555555"
	task := Task{
		AgentID: agentID,
		Agent: &AgentData{
			ID:   agentID,
			Name: "Review Bot",
			CustomEnv: map[string]string{
				"GIT_AUTHOR_NAME":     "Evil Author",
				"GIT_AUTHOR_EMAIL":    "evil@example.com",
				"GIT_COMMITTER_NAME":  "Evil Committer",
				"GIT_COMMITTER_EMAIL": "evil-c@example.com",
				"ANTHROPIC_API_KEY":   "sk-test",
			},
		},
	}

	agentEnv := map[string]string{"PATH": "/usr/bin"}
	layerCustomEnvAndHermesHome(agentEnv, task.Agent.CustomEnv, "", nil)
	applyPlatformGitIdentity(agentEnv, task, "fallback-name")

	if got := agentEnv["ANTHROPIC_API_KEY"]; got != "sk-test" {
		t.Fatalf("unrelated custom_env key lost: %q", got)
	}
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		if isBlockedEnvKey(key) != true {
			t.Fatalf("%s must be blocklisted", key)
		}
	}
	if _, ok := agentEnv["GIT_AUTHOR_NAME"]; !ok {
		// custom_env blocked keys were dropped; platform identity must re-apply.
	}
	wantEmail := platformGitEmail(agentID)
	if agentEnv["GIT_AUTHOR_NAME"] != "Review Bot" || agentEnv["GIT_COMMITTER_NAME"] != "Review Bot" {
		t.Fatalf("name = author %q committer %q, want Review Bot", agentEnv["GIT_AUTHOR_NAME"], agentEnv["GIT_COMMITTER_NAME"])
	}
	if agentEnv["GIT_AUTHOR_EMAIL"] != wantEmail || agentEnv["GIT_COMMITTER_EMAIL"] != wantEmail {
		t.Fatalf("email = author %q committer %q, want %q", agentEnv["GIT_AUTHOR_EMAIL"], agentEnv["GIT_COMMITTER_EMAIL"], wantEmail)
	}
}

func TestApplyPlatformGitIdentityRenameUpdatesNameKeepsEmail(t *testing.T) {
	t.Parallel()

	agentID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	email := platformGitEmail(agentID)

	first := map[string]string{}
	applyPlatformGitIdentity(first, Task{
		AgentID: agentID,
		Agent:   &AgentData{ID: agentID, Name: "Old Name"},
	}, "")
	second := map[string]string{}
	applyPlatformGitIdentity(second, Task{
		AgentID: agentID,
		Agent:   &AgentData{ID: agentID, Name: "New Name"},
	}, "")

	if first["GIT_AUTHOR_EMAIL"] != email || second["GIT_AUTHOR_EMAIL"] != email {
		t.Fatalf("email drifted across rename: %q -> %q, want %q", first["GIT_AUTHOR_EMAIL"], second["GIT_AUTHOR_EMAIL"], email)
	}
	if first["GIT_AUTHOR_NAME"] != "Old Name" || second["GIT_AUTHOR_NAME"] != "New Name" {
		t.Fatalf("name did not follow task snapshot: %q -> %q", first["GIT_AUTHOR_NAME"], second["GIT_AUTHOR_NAME"])
	}
}

func TestApplyPlatformGitIdentityFallbackName(t *testing.T) {
	t.Parallel()

	agentEnv := map[string]string{}
	applyPlatformGitIdentity(agentEnv, Task{AgentID: "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"}, "Daemon Resolved Name")
	if agentEnv["GIT_AUTHOR_NAME"] != "Daemon Resolved Name" {
		t.Fatalf("fallback name = %q", agentEnv["GIT_AUTHOR_NAME"])
	}
	if !strings.HasPrefix(agentEnv["GIT_AUTHOR_EMAIL"], "agent-") {
		t.Fatalf("email = %q", agentEnv["GIT_AUTHOR_EMAIL"])
	}
}
