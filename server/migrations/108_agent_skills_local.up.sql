-- Per-agent switch controlling whether the Claude runtime merges the host
-- machine's `~/.claude/skills/` into the agent's skill set. 'merge' (default)
-- preserves the pre-existing inherit-from-machine behavior so existing
-- workflows that rely on locally installed skills keep working. 'ignore'
-- isolates the runtime so a broken local skill on one operator's machine
-- cannot silently crash a shared agent (GitHub #3052 — Claude exits before
-- reading stdin, leaving the daemon with "broken pipe"). Workspace skills
-- (`{workDir}/.claude/skills/`) are always loaded — the toggle only governs
-- the user-global directory.
ALTER TABLE agent
    ADD COLUMN skills_local TEXT NOT NULL DEFAULT 'merge'
    CHECK (skills_local IN ('ignore', 'merge'));
