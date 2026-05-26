//go:build windows

package agent

import (
	"fmt"
	"os"
	"os/exec"
)

// createDirLink mirrors a directory from src to dst inside the per-task
// scratch CLAUDE_CONFIG_DIR. Windows os.Symlink requires
// SeCreateSymbolicLinkPrivilege (Developer Mode or admin), which most
// Multica CLI / Desktop installs do not have — without a fallback the
// mirror leaves an empty `~/.claude/`-equivalent, and the default
// "ignore" mode silently breaks Claude Code authentication on those
// hosts. A directory junction (`mklink /J`) does not require the
// privilege and behaves equivalently for our read-only mirror use, so
// it is the first fallback. See server/internal/daemon/execenv for the
// same pattern applied to CODEX_HOME.
func createDirLink(src, dst string) error {
	if err := os.Symlink(src, dst); err == nil {
		return nil
	}
	out, err := exec.Command("cmd", "/c", "mklink", "/J", dst, src).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mklink /J %s %s: %s: %w", dst, src, out, err)
	}
	return nil
}

// createFileLink mirrors a file from src to dst inside the per-task scratch
// CLAUDE_CONFIG_DIR. Tries symlink first, then hardlink (works on the same
// NTFS volume without elevated privileges and still shares content with the
// shared `~/.claude/.credentials.json` so token refreshes propagate), then
// a final content copy as a last resort.
func createFileLink(src, dst string) error {
	if err := os.Symlink(src, dst); err == nil {
		return nil
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}
