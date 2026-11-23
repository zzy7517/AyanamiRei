package tasktree

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// createStep is one tree's part of a validated Create.
type createStep struct {
	tree Tree
	repo *repository
	base string // the commit of the base branch the tree starts from
	// healthy is set when a worktree already stands at the tree's path, on
	// the task branch, cut from the tree's repository.
	healthy bool
	// branchExists is set when the task branch survived an earlier tree.
	branchExists bool
	// stale is set when the repository still registers a worktree at the
	// tree's path although none is there.
	stale bool
}

// Create makes sure every tree of the task exists and returns its layout.
//
// A tree whose task branch does not exist yet is cut from the latest commit of
// its base branch as that ref stands locally; nothing is fetched. One whose
// branch survived an earlier tree is checked out from that branch again. A
// tree that is already a healthy worktree of its repository on its task branch
// is left alone, so Create is safe to repeat and is the rebuild after a
// deletion.
//
// Everything is validated before anything is created. When a tree cannot be
// created, the trees this call added are removed again — with the task
// branches it created, which are new and empty — and the error says what went
// wrong.
func Create(root, taskKey string, specs []Spec, logger *slog.Logger) (*TaskLayout, error) {
	layout, err := Layout(root, taskKey, specs)
	if err != nil {
		return nil, err
	}
	unlockTask := lockTask(layout.Dir)
	defer unlockTask()

	repos, index, err := openRepositories(specs)
	if err != nil {
		return nil, err
	}
	unlockRepos, err := lockRepositories(repos, logger)
	if err != nil {
		return nil, fmt.Errorf("tasktree: lock the repositories of task %s: %w", layout.Key, err)
	}
	defer unlockRepos()

	steps, err := planCreate(layout, repos, index)
	if err != nil {
		return nil, err
	}
	if err := createTrees(layout, steps, logger); err != nil {
		return nil, err
	}
	return layout, nil
}

// openRepositories validates every spec's repository and returns the distinct
// repositories, plus for each spec the index of its repository in that slice.
// Two specs naming different worktrees of one repository share an index.
func openRepositories(specs []Spec) (repos []*repository, index []int, err error) {
	seen := make(map[string]int, len(specs))
	for _, spec := range specs {
		repo, err := openRepository(spec.Repo)
		if err != nil {
			return nil, nil, err
		}
		if _, ok := seen[repo.common]; !ok {
			seen[repo.common] = len(repos)
			repos = append(repos, repo)
		}
		index = append(index, seen[repo.common])
	}
	return repos, index, nil
}

// planCreate validates what can only be checked against the repositories and
// the disk, and decides how each tree has to be made.
func planCreate(layout *TaskLayout, repos []*repository, index []int) ([]createStep, error) {
	if top, inside := enclosingWorkTree(layout.Root); inside {
		return nil, fmt.Errorf("tasktree: task root %s would be inside the git working tree %s; task trees belong outside every repository checkout", layout.Root, top)
	}
	if len(layout.Trees) > 1 && isWorkTree(layout.Dir) {
		return nil, fmt.Errorf("tasktree: task directory %s is itself a worktree and cannot hold the %d trees of this task", layout.Dir, len(layout.Trees))
	}

	registrations := make([][]worktreeEntry, len(repos))
	pairs := make(map[[2]string]bool, len(layout.Trees))
	steps := make([]createStep, 0, len(layout.Trees))
	for i, tree := range layout.Trees {
		repo := repos[index[i]]
		ref := baseRef(repo.root, tree.BaseBranch)
		if ref == "" {
			return nil, fmt.Errorf("tasktree: repository %s has no local or remote-tracking branch %q", tree.Repo, tree.BaseBranch)
		}
		pair := [2]string{repo.common, tree.BaseBranch}
		if pairs[pair] {
			return nil, fmt.Errorf("tasktree: repository %s is listed twice with base branch %q", tree.Repo, tree.BaseBranch)
		}
		pairs[pair] = true
		base, err := gitTrimmed(repo.root, "rev-parse", "--verify", ref)
		if err != nil {
			return nil, fmt.Errorf("tasktree: resolve branch %q in %s: %w", tree.BaseBranch, tree.Repo, err)
		}
		step := createStep{tree: tree, repo: repo, base: base}
		_, err = gitOut(repo.root, "show-ref", "--verify", "--quiet", "refs/heads/"+tree.Branch)
		step.branchExists = err == nil

		if registrations[index[i]] == nil {
			if registrations[index[i]], err = listWorktrees(repo.root); err != nil {
				return nil, fmt.Errorf("tasktree: list the worktrees of %s: %w", tree.Repo, err)
			}
		}
		for _, entry := range registrations[index[i]] {
			if canonical(entry.path) == realish(tree.Path) {
				step.stale = true
			} else if step.branchExists && entry.branch == "refs/heads/"+tree.Branch {
				return nil, fmt.Errorf("tasktree: task branch %s is checked out at %s; remove that worktree first", tree.Branch, entry.path)
			}
		}
		if err := checkTreePath(&step); err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, nil
}

// checkTreePath records what stands at the step's path. An existing worktree
// has to be exactly the tree the layout describes; anything else but an empty
// directory — which git worktree add fills — is refused.
func checkTreePath(step *createStep) error {
	tree := step.tree
	info, err := os.Lstat(tree.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("tasktree: inspect %s: %w", tree.Path, err)
	case !info.IsDir():
		return fmt.Errorf("tasktree: %s exists and is not a directory", tree.Path)
	}
	entries, err := os.ReadDir(tree.Path)
	if err != nil {
		return fmt.Errorf("tasktree: inspect %s: %w", tree.Path, err)
	}
	if len(entries) == 0 {
		return nil
	}
	step.stale = false
	found := probe(tree.Path)
	switch {
	case found.problem != "":
		return fmt.Errorf("tasktree: %s exists and is not a worktree: %s", tree.Path, found.problem)
	case found.top != canonical(tree.Path):
		return fmt.Errorf("tasktree: %s exists and is a directory inside the working tree %s", tree.Path, found.top)
	case found.common != step.repo.common:
		return fmt.Errorf("tasktree: %s exists and is a worktree of another repository (%s)", tree.Path, found.common)
	case found.detached:
		return fmt.Errorf("tasktree: %s exists and has a detached HEAD, not the task branch %s", tree.Path, tree.Branch)
	case found.branch != tree.Branch:
		return fmt.Errorf("tasktree: %s exists and is on branch %s, not on the task branch %s", tree.Path, found.branch, tree.Branch)
	}
	step.healthy = true
	return nil
}

// enclosingWorkTree returns the git working tree that contains root, or the
// nearest existing directory above it, and whether there is one. Trees inside
// a checkout would show up in that checkout's status and in the snapshots its
// worktree-mode tasks take, so a root there is refused.
func enclosingWorkTree(root string) (string, bool) {
	dir := filepath.Clean(root)
	for !pathExists(dir) {
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
	top, err := gitTrimmed(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	return top, true
}

// createTrees carries a validated plan out. The trees it adds are undone when
// a later one fails.
func createTrees(layout *TaskLayout, steps []createStep, logger *slog.Logger) error {
	rootExisted, dirExisted := pathExists(layout.Root), pathExists(layout.Dir)
	if err := os.MkdirAll(layout.Dir, 0o755); err != nil {
		return fmt.Errorf("tasktree: create task directory %s: %w", layout.Dir, err)
	}
	added := make([]createStep, 0, len(steps))
	for _, step := range steps {
		var err error
		if step.healthy {
			err = recordBaseBranch(step.tree, false)
		} else if err = clearStaleRegistration(step); err == nil {
			// Only the add can leave a worktree behind: git registers one
			// before its checkout hooks run, so a failed add is undone too.
			added = append(added, step)
			err = addTree(step)
		}
		if err != nil {
			err = fmt.Errorf("tasktree: create tree %s: %w", step.tree.Path, err)
			if rollback := undoCreated(added, logger); rollback != nil {
				err = errors.Join(err, rollback)
			}
			if !dirExisted {
				_ = removeIfEmpty(layout.Dir)
			}
			if !rootExisted {
				_ = removeIfEmpty(layout.Root)
			}
			return err
		}
	}
	if logger != nil {
		logger.Info("tasktree: trees ready", "task_dir", layout.Dir, "created", len(added), "kept", len(steps)-len(added))
	}
	return nil
}

// clearStaleRegistration drops the registration a repository keeps for the
// step's path although no worktree stands there, so that the path is free for
// the tree. Nothing else of the repository is pruned.
func clearStaleRegistration(step createStep) error {
	if !step.stale {
		return nil
	}
	// git removes the registration of a path only when nothing stands there; an
	// empty directory — one Create made for the tree to fill — it refuses. The
	// path is missing or empty by then.
	if err := os.Remove(step.tree.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clear the empty directory at the path: %w", err)
	}
	if _, err := gitOut(step.repo.root, "worktree", "remove", "--force", step.tree.Path); err != nil {
		return fmt.Errorf("clear the worktree registration left behind at the path: %w", err)
	}
	return nil
}

// addTree makes the worktree of one step. A missing task branch is created at
// the base commit; passing the commit rather than the branch keeps git from
// configuring an upstream on the user's task branch when the base is a
// remote-tracking branch.
func addTree(step createStep) error {
	args := []string{"worktree", "add", step.tree.Path}
	if step.branchExists {
		args = append(args, step.tree.Branch)
	} else {
		args = append(args, "-b", step.tree.Branch, step.base)
	}
	if _, err := gitOut(step.repo.root, args...); err != nil {
		return err
	}
	return recordBaseBranch(step.tree, true)
}

// undoCreated removes the trees a failed Create added. A task branch the call
// created goes with its tree while it still stands at its base commit, as new
// and empty as it was made; one that survived an earlier tree — or received a
// commit meanwhile — stays.
func undoCreated(added []createStep, logger *slog.Logger) error {
	var errs []error
	for i := len(added) - 1; i >= 0; i-- {
		step := added[i]
		// A step that failed before git registered its tree leaves nothing to
		// remove; a worktree still standing after the remove is a failure.
		if _, err := gitOut(step.repo.root, "worktree", "remove", "--force", step.tree.Path); err != nil && isWorkTree(step.tree.Path) {
			errs = append(errs, fmt.Errorf("tasktree: roll back %s: %w", step.tree.Path, err))
			continue
		}
		if step.branchExists {
			continue
		}
		at, err := gitTrimmed(step.repo.root, "rev-parse", "--verify", "refs/heads/"+step.tree.Branch)
		if err != nil || at != step.base {
			continue
		}
		if _, err := gitOut(step.repo.root, "branch", "-D", "--", step.tree.Branch); err != nil {
			errs = append(errs, fmt.Errorf("tasktree: roll back branch %s: %w", step.tree.Branch, err))
		}
	}
	if len(errs) > 0 && logger != nil {
		logger.Error("tasktree: could not undo a failed Create; the task directory needs attention", "errors", errs)
	}
	return errors.Join(errs...)
}

// recordBaseBranch stores the tree's base branch in the worktree's private
// git dir, where Inspect and Delete read it back.
func recordBaseBranch(tree Tree, overwrite bool) error {
	gitDir, err := gitTrimmed(tree.Path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("locate the git dir of the worktree: %w", err)
	}
	path := filepath.Join(filepath.FromSlash(gitDir), baseBranchFile)
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
	}
	if err := os.WriteFile(path, []byte(tree.BaseBranch+"\n"), 0o644); err != nil {
		return fmt.Errorf("record the base branch: %w", err)
	}
	return nil
}

func readBaseBranch(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, baseBranchFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// removeIfEmpty deletes dir when it holds nothing and leaves it alone when it
// does not. os.Remove reports a directory that is not empty as ENOTEMPTY, or
// as EEXIST on some systems.
func removeIfEmpty(dir string) error {
	err := os.Remove(dir)
	if err == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
		return nil
	}
	return err
}
