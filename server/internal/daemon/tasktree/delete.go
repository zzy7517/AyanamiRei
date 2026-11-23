package tasktree

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// TreeOutcome is what removing one tree did with the work it held.
type TreeOutcome struct {
	Path   string
	Branch string
	// AutoCommitted is set when uncommitted work was committed to Branch.
	AutoCommitted bool
	// LeftoverRef names the hidden ref holding uncommitted work that was kept
	// off Branch.
	LeftoverRef string
	// Kept is set when the tree was not removed; Err says why.
	Kept bool
	Err  error
}

// DeleteResult is what became of each tree a removal selected.
type DeleteResult struct {
	Trees []TreeOutcome
}

// Delete finalizes and removes every tree of the task in taskDir, then removes
// taskDir once it holds nothing. The task branches stay.
//
// Each tree is handled on its own: one that cannot be finalized is kept and
// reported while the others are still removed, and the returned error joins
// every tree's. A missing task directory is an error wrapping fs.ErrNotExist.
func Delete(taskDir string, logger *slog.Logger) (*DeleteResult, error) {
	return removeTrees(taskDir, "", logger)
}

// DeleteTree is Delete for one tree, named as Inspect reports it: its slug, or
// the task key when the task has a single tree.
func DeleteTree(taskDir, name string, logger *slog.Logger) (*DeleteResult, error) {
	return removeTrees(taskDir, name, logger)
}

func removeTrees(taskDir, name string, logger *slog.Logger) (*DeleteResult, error) {
	taskDir, err := absolutePath("task directory", taskDir)
	if err != nil {
		return nil, err
	}
	unlockTask := lockTask(taskDir)
	defer unlockTask()

	trees, err := discover(taskDir)
	if err != nil {
		return nil, err
	}
	var selected []tree
	for _, t := range trees {
		if name == "" || t.Name == name {
			selected = append(selected, t)
		}
	}
	if name != "" && len(selected) == 0 {
		return nil, fmt.Errorf("tasktree: task directory %s has no tree named %q", taskDir, name)
	}
	unlockRepos, err := lockRepositories(distinctRepositories(selected), logger)
	if err != nil {
		return nil, fmt.Errorf("tasktree: lock the repositories of %s: %w", taskDir, err)
	}
	defer unlockRepos()

	result := &DeleteResult{}
	var errs []error
	for _, t := range selected {
		outcome := removeTree(t, logger)
		result.Trees = append(result.Trees, outcome)
		if outcome.Err != nil {
			errs = append(errs, outcome.Err)
		}
	}
	if len(errs) == 0 {
		if err := removeIfEmpty(taskDir); err != nil {
			errs = append(errs, fmt.Errorf("tasktree: remove task directory %s: %w", taskDir, err))
		}
	}
	return result, errors.Join(errs...)
}

// distinctRepositories names each repository behind trees once, by the first
// tree of it, so that its lock is taken once.
func distinctRepositories(trees []tree) []*repository {
	var repos []*repository
	seen := make(map[string]bool, len(trees))
	for _, t := range trees {
		if t.common != "" && !seen[t.common] {
			seen[t.common] = true
			repos = append(repos, &repository{root: t.Path, common: t.common})
		}
	}
	return repos
}

// removeTree finalizes the uncommitted work of one tree and then removes the
// worktree. On any failure the tree stays exactly as it is.
func removeTree(t tree, logger *slog.Logger) TreeOutcome {
	out := TreeOutcome{Path: t.Path, Branch: t.Branch}
	kept := func(format string, args ...any) TreeOutcome {
		out.Kept = true
		args = append(args, t.Path, t.Repository)
		out.Err = fmt.Errorf("tasktree: "+format+"; the work is preserved at %s (listed by `git worktree list` in %s)", args...)
		return out
	}
	if !t.Healthy {
		return kept("refusing to remove %s: %s", t.Path, t.Problem)
	}

	// An unresolved merge is never committed: `git add -A` would turn the
	// conflict markers into a commit. The tree is kept for someone to finish
	// the merge, as worktree mode keeps its own.
	if unmerged, err := execenv.UnmergedPaths(t.Path); err != nil {
		return kept("could not check the tree on %s for an unresolved merge: %v", t.Branch, err)
	} else if len(unmerged) > 0 {
		return kept("refusing to remove the tree on %s: %s are still unmerged", t.Branch, strings.Join(unmerged, ", "))
	}

	dirty, err := execenv.WorktreeIsDirty(t.Path)
	if err != nil {
		// Not being able to tell is treated as dirty: a commit that finds
		// nothing costs an error at worst, assuming clean risks deleting work.
		dirty = true
	}
	if dirty {
		// The documented rule: a branch without a commit of its own takes
		// everything left uncommitted; one that has commits keeps them off, and
		// the leftovers go under a hidden ref. An unknown ahead count — the
		// base branch is gone — counts as no commit of its own, the side that
		// keeps the work on the branch.
		if t.Ahead > 0 {
			tip, err := gitTrimmed(t.Path, "rev-parse", "--verify", "HEAD")
			if err != nil {
				return kept("could not resolve the tip of %s: %v", t.Branch, err)
			}
			ref, err := execenv.SaveLeftovers(t.Path, t.Path, t.Branch, tip, logger)
			if err != nil {
				return kept("could not save the changes left uncommitted on %s: %v", t.Branch, err)
			}
			out.LeftoverRef = ref
		} else {
			committed, err := execenv.CommitEverything(t.Path, "chore(task): uncommitted changes from "+t.Branch, false)
			if err != nil {
				return kept("could not commit the uncommitted changes to %s: %v", t.Branch, err)
			}
			out.AutoCommitted = committed
		}
	}

	repo := t.Repository
	if repo == "" {
		repo = t.Path
	}
	if _, err := gitOut(repo, "worktree", "remove", "--force", t.Path); err != nil {
		return kept("could not remove the worktree of %s: %v", t.Branch, err)
	}
	if logger != nil {
		logger.Info("tasktree: tree removed",
			"path", t.Path,
			"branch", t.Branch,
			"auto_committed", out.AutoCommitted,
			"leftover_ref", out.LeftoverRef)
	}
	return out
}
