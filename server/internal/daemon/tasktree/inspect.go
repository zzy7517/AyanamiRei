package tasktree

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// Commit summarizes the commit a tree stands on.
type Commit struct {
	Hash    string
	Subject string
	Author  string
	Time    time.Time
}

// TreeStatus is one tree of a task as found on disk.
type TreeStatus struct {
	// Name is the tree's directory name inside the task directory: its slug,
	// or the task key when the task has a single tree.
	Name string
	Path string
	// Repository is the root of the repository's main working tree, or ""
	// when git cannot name it.
	Repository string
	// BaseBranch is the branch the tree was cut from, as Create recorded it.
	// It is "" for a tree Create did not make or whose record is gone.
	BaseBranch string
	// Branch is the branch checked out in the tree, or "" when HEAD is
	// detached.
	Branch string
	// Healthy is set when the path is a worktree checked out on the task
	// branch its place dictates; Problem says why it is not.
	Healthy bool
	Problem string
	// Dirty is set when the worktree holds uncommitted or untracked changes.
	Dirty bool
	// Ahead and Behind count the commits the tree's branch has beyond its
	// base branch and lacks behind it. Both are -1 while the base branch is
	// unknown or no longer exists, or HEAD is detached.
	Ahead  int
	Behind int
	// LastCommit summarizes the commit the tree stands on. It is zero when
	// there is none.
	LastCommit Commit
}

// TaskStatus is the state of a task directory's trees.
type TaskStatus struct {
	Dir   string
	Trees []TreeStatus
}

// tree is a tree's status together with what git reported about its working
// tree, which Delete needs to act on it.
type tree struct {
	TreeStatus
	gitDir string
	// common identifies the tree's repository.
	common string
}

// Inspect reports the trees in taskDir and their state. It only reads. A task
// directory without a worktree yields no trees; a missing one is an error
// wrapping fs.ErrNotExist.
func Inspect(taskDir string) (*TaskStatus, error) {
	taskDir, err := absolutePath("task directory", taskDir)
	if err != nil {
		return nil, err
	}
	unlock := lockTask(taskDir)
	defer unlock()

	trees, err := discover(taskDir)
	if err != nil {
		return nil, err
	}
	status := &TaskStatus{Dir: taskDir, Trees: make([]TreeStatus, len(trees))}
	for i := range trees {
		status.Trees[i] = trees[i].TreeStatus
	}
	return status, nil
}

func absolutePath(what, path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("tasktree: %s %q must be an absolute path", what, path)
	}
	return filepath.Clean(path), nil
}

// discover finds the trees in a task directory: the directory itself when it
// is a worktree, otherwise each subdirectory that is one. A link standing in
// for a directory is not followed, so a worktree elsewhere is not a tree of
// the task.
func discover(taskDir string) ([]tree, error) {
	info, err := os.Stat(taskDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("tasktree: task directory %s: %w", taskDir, fs.ErrNotExist)
	case err != nil:
		return nil, fmt.Errorf("tasktree: task directory %s: %w", taskDir, err)
	case !info.IsDir():
		return nil, fmt.Errorf("tasktree: task directory %s is not a directory", taskDir)
	}
	key := filepath.Base(taskDir)
	if isWorkTree(taskDir) {
		return []tree{describe(taskDir, key, BranchPrefix+key)}, nil
	}
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return nil, fmt.Errorf("tasktree: read task directory %s: %w", taskDir, err)
	}
	var trees []tree
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(taskDir, entry.Name())
		if isWorkTree(path) {
			trees = append(trees, describe(path, entry.Name(), BranchPrefix+key+"/"+entry.Name()))
		}
	}
	return trees, nil
}

// describe reports the state of the worktree at path, judged against the task
// branch the layout dictates for its name.
func describe(path, name, taskBranch string) tree {
	info := probe(path)
	t := tree{
		TreeStatus: TreeStatus{Name: name, Path: path, Branch: info.branch, Ahead: -1, Behind: -1},
		gitDir:     info.gitDir,
		common:     info.common,
	}
	working := info.problem == "" && info.top == canonical(path)
	switch {
	case info.problem != "":
		t.Problem = info.problem
	case !working:
		t.Problem = "a directory inside the working tree " + info.top
	case info.detached:
		t.Problem = "HEAD is detached, not on the task branch " + taskBranch
	case info.branch != taskBranch:
		t.Problem = fmt.Sprintf("on branch %s, not on the task branch %s", info.branch, taskBranch)
	}
	if working {
		t.Repository = mainWorkTree(path)
		t.BaseBranch = readBaseBranch(info.gitDir)
		t.Dirty, _ = execenv.WorktreeIsDirty(path)
		if ref := baseRef(path, t.BaseBranch); ref != "" && !info.detached {
			if ahead, behind, err := aheadBehind(path, ref); err == nil {
				t.Ahead, t.Behind = ahead, behind
			}
		}
		t.LastCommit = lastCommit(path)
	}
	t.Healthy = t.Problem == ""
	return t
}

// aheadBehind counts the commits HEAD has beyond ref and lacks behind it.
func aheadBehind(path, ref string) (ahead, behind int, err error) {
	out, err := gitTrimmed(path, "rev-list", "--left-right", "--count", ref+"...HEAD")
	if err != nil {
		return 0, 0, err
	}
	behind, ahead, err = parseCounts(out)
	return ahead, behind, err
}

// parseCounts reads the "<left>\t<right>" of git rev-list --left-right --count.
func parseCounts(out string) (left, right int, err error) {
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("tasktree: unexpected count %q", out)
	}
	if left, err = strconv.Atoi(fields[0]); err != nil {
		return 0, 0, err
	}
	right, err = strconv.Atoi(fields[1])
	return left, right, err
}

func lastCommit(path string) Commit {
	out, err := gitTrimmed(path, "log", "-1", "--format=%H%x00%an%x00%aI%x00%s")
	if err != nil {
		return Commit{}
	}
	hash, rest, _ := strings.Cut(out, "\x00")
	author, rest, _ := strings.Cut(rest, "\x00")
	when, subject, _ := strings.Cut(rest, "\x00")
	c := Commit{Hash: hash, Subject: subject, Author: author}
	c.Time, _ = time.Parse(time.RFC3339, when)
	return c
}

// BranchList is what Branches returns.
type BranchList struct {
	// Local holds the local branches.
	Local []string
	// Remotes holds the remote-tracking branches, as "origin/main".
	Remotes []string
}

// Branches lists the local and remote-tracking branches of the repository at
// repoPath, each sorted by name. Symbolic refs such as origin/HEAD are left
// out: they follow another branch and are no base of their own.
func Branches(repoPath string) (BranchList, error) {
	out, err := gitOut(repoPath, "for-each-ref", "--format=%(refname)%00%(symref)", "refs/heads/", "refs/remotes/")
	if err != nil {
		return BranchList{}, fmt.Errorf("tasktree: list the branches of %s: %w", repoPath, err)
	}
	var list BranchList
	for _, line := range strings.Split(out, "\n") {
		ref, symref, _ := strings.Cut(line, "\x00")
		switch {
		case ref == "", symref != "":
			continue
		case strings.HasPrefix(ref, "refs/heads/"):
			list.Local = append(list.Local, strings.TrimPrefix(ref, "refs/heads/"))
		case strings.HasPrefix(ref, "refs/remotes/"):
			list.Remotes = append(list.Remotes, strings.TrimPrefix(ref, "refs/remotes/"))
		}
	}
	return list, nil
}
