//go:build !windows

package agent

import "os"

// createDirLink symlinks src → dst for a directory entry mirrored into the
// per-task scratch CLAUDE_CONFIG_DIR. On Unix the kernel always honours
// symlinks, so a single os.Symlink call is sufficient and matches the
// existing behaviour.
func createDirLink(src, dst string) error {
	return os.Symlink(src, dst)
}

// createFileLink symlinks src → dst for a file entry mirrored into the
// per-task scratch CLAUDE_CONFIG_DIR. Unix has no equivalent of Windows's
// SeCreateSymbolicLinkPrivilege gate, so a single os.Symlink is enough.
func createFileLink(src, dst string) error {
	return os.Symlink(src, dst)
}
