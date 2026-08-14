package daemon

import (
	"strings"
)

// platformGitNoreplyDomain is a non-deliverable domain used for stable Agent
// commit emails. The address is not a real mailbox; it only attributes commits
// to a Multica Agent UUID across tasks.
const platformGitNoreplyDomain = "users.noreply.multica.local"

// applyPlatformGitIdentity stamps the Multica Agent-derived Git author and
// committer identity onto a task process environment after custom_env has been
// layered. custom_env cannot override these keys: they are blocklisted and
// re-applied here so the platform identity always wins.
func applyPlatformGitIdentity(agentEnv map[string]string, task Task, fallbackName string) {
	if agentEnv == nil {
		return
	}
	agentID := strings.TrimSpace(task.AgentID)
	displayName := strings.TrimSpace(fallbackName)
	if task.Agent != nil {
		if id := strings.TrimSpace(task.Agent.ID); id != "" {
			agentID = id
		}
		if name := strings.TrimSpace(task.Agent.Name); name != "" {
			displayName = name
		}
	}
	if displayName == "" {
		displayName = "Multica Agent"
	}
	email := platformGitEmail(agentID)
	if email == "" {
		return
	}
	agentEnv["GIT_AUTHOR_NAME"] = displayName
	agentEnv["GIT_AUTHOR_EMAIL"] = email
	agentEnv["GIT_COMMITTER_NAME"] = displayName
	agentEnv["GIT_COMMITTER_EMAIL"] = email
}

// platformGitEmail derives a stable, non-deliverable noreply address from the
// Agent UUID. The same Agent keeps the same email across rename and tasks.
func platformGitEmail(agentID string) string {
	id := strings.ToLower(strings.TrimSpace(agentID))
	if id == "" {
		return ""
	}
	// Keep UUID punctuation out of the local-part while remaining reversible
	// enough for operators to map back to the Agent ID.
	local := strings.NewReplacer("-", "", "{", "", "}", "").Replace(id)
	if local == "" {
		return ""
	}
	return "agent-" + local + "@" + platformGitNoreplyDomain
}
