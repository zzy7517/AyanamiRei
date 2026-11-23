package execenv

import "log/slog"

// Work on worktrees of a user's repository outside the per-task env root (the
// daemon's task trees) has to apply the delivery rules Finalize applies. These
// functions are that code, exported; none of them restates a rule.

// RunGitStdout runs git in dir and returns its stdout. The error carries git's
// stderr, so the output can be parsed without mistaking a diagnostic for a
// value.
func RunGitStdout(dir string, args ...string) (string, error) {
	return runGitStdout(dir, args...)
}

// LockGitRoot takes the cross-process lock that serializes worktree add,
// remove and prune on gitRoot's repository, and returns its release func. A
// repository whose git dir cannot hold the lock file still gets the in-process
// mutex; a lock another holder does not release within the wait is an error.
func LockGitRoot(gitRoot string, logger *slog.Logger) (func(), error) {
	return lockGitRoot(gitRoot, logger)
}

// WorktreeIsDirty reports whether the worktree holds uncommitted or untracked
// changes.
func WorktreeIsDirty(worktreePath string) (bool, error) {
	return worktreeIsDirty(worktreePath)
}

// UnmergedPaths lists the files git considers unresolved in the worktree.
func UnmergedPaths(worktreePath string) ([]string, error) {
	return unmergedPaths(worktreePath)
}

// CommitEverything stages and commits all of worktreePath's changes onto its
// checked-out branch without running the repository's commit hooks. It reports
// false when there was nothing to commit; an error means nothing was committed
// and the changes are still only on disk.
func CommitEverything(worktreePath, message string, allowEmpty bool) (bool, error) {
	return commitEverything(worktreePath, message, allowEmpty)
}

// SaveLeftovers records everything worktreePath holds beyond tip as one commit
// under refs/multica/leftovers/<branch>/<commit>, without touching the branch,
// the worktree or its index, and returns the ref. It returns "" when there is
// nothing to save.
func SaveLeftovers(gitRoot, worktreePath, branch, tip string, logger *slog.Logger) (string, error) {
	return (&LocalWorktree{GitRoot: gitRoot, Path: worktreePath, Branch: branch}).saveLeftovers(tip, logger)
}
