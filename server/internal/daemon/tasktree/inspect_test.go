package tasktree

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInspectReportsASingleTree(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	gitRun(t, repo, "branch", "dev")
	root := filepath.Join(t.TempDir(), "root")
	specs := singleSpec(repo, "main")
	layout, err := Create(root, "MUL-7", specs, testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	work := commitFile(t, layout.Trees[0].Path, "work.txt", "done\n", "work on the task")
	commitFile(t, repo, "main.txt", "moved on\n", "on main")
	writeFile(t, filepath.Join(layout.Trees[0].Path, "draft.txt"), "draft\n")
	before := snapshotUser(t, repo)

	status, err := Inspect(layout.Dir)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if status.Dir != layout.Dir || len(status.Trees) != 1 {
		t.Fatalf("Inspect reports %+v, want one tree in %s", status, layout.Dir)
	}
	got := status.Trees[0]
	if got.Name != "MUL-7" || got.Path != layout.Dir {
		t.Errorf("the tree is %s at %s, want MUL-7 at %s", got.Name, got.Path, layout.Dir)
	}
	if got.Repository != repo {
		t.Errorf("the tree's repository is %s, want %s", got.Repository, repo)
	}
	if got.BaseBranch != "main" || got.Branch != "task/MUL-7" {
		t.Errorf("the tree is on %s cut from %q, want task/MUL-7 cut from main", got.Branch, got.BaseBranch)
	}
	if !got.Healthy || got.Problem != "" {
		t.Errorf("the tree is not healthy: %q", got.Problem)
	}
	if !got.Dirty {
		t.Error("the tree's uncommitted file is not reported")
	}
	if got.Ahead != 1 || got.Behind != 1 {
		t.Errorf("the tree is %d ahead and %d behind, want 1 and 1", got.Ahead, got.Behind)
	}
	if got.LastCommit.Hash != work || got.LastCommit.Subject != "work on the task" || got.LastCommit.Author != "Test User" {
		t.Errorf("the last commit is %+v", got.LastCommit)
	}
	if got.LastCommit.Time.IsZero() {
		t.Error("the last commit has no time")
	}

	// Inspect only reads.
	assertUserUntouched(t, before, snapshotUser(t, repo))
}

// A task directory with several trees lists each by its slug, in name order.
func TestInspectListsEveryTreeByName(t *testing.T) {
	t.Parallel()
	app := newRepo(t, "app")
	gitRun(t, app, "branch", "dev")
	web := newRepo(t, "web")
	layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-2", []Spec{
		{Repo: web, BaseBranch: "main"},
		{Repo: app, BaseBranch: "main"},
		{Repo: app, BaseBranch: "dev"},
	}, testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	writeFile(t, filepath.Join(layout.Dir, "app--dev", "draft.txt"), "draft\n")

	status, err := Inspect(layout.Dir)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	want := []struct{ name, repo, base, branch string }{
		{"app--dev", app, "dev", "task/MUL-2/app--dev"},
		{"app--main", app, "main", "task/MUL-2/app--main"},
		{"web", web, "main", "task/MUL-2/web"},
	}
	if len(status.Trees) != len(want) {
		t.Fatalf("Inspect lists %+v, want %d trees", status.Trees, len(want))
	}
	for i, tree := range status.Trees {
		w := want[i]
		if tree.Name != w.name || tree.Path != filepath.Join(layout.Dir, w.name) {
			t.Errorf("tree %d is %s at %s, want %s at %s", i+1, tree.Name, tree.Path, w.name, filepath.Join(layout.Dir, w.name))
		}
		if tree.Repository != w.repo || tree.BaseBranch != w.base || tree.Branch != w.branch {
			t.Errorf("tree %s is on %s cut from %s of %s, want %s cut from %s of %s",
				tree.Name, tree.Branch, tree.BaseBranch, tree.Repository, w.branch, w.base, w.repo)
		}
		if !tree.Healthy || tree.Ahead != 0 || tree.Behind != 0 {
			t.Errorf("tree %s: healthy %v, %d ahead, %d behind (%s)", tree.Name, tree.Healthy, tree.Ahead, tree.Behind, tree.Problem)
		}
		if tree.Dirty != (w.name == "app--dev") {
			t.Errorf("tree %s dirty = %v", tree.Name, tree.Dirty)
		}
	}
}

// A worktree that is not the one the layout dictates is reported, with the
// reason, and nothing else in the task directory counts as a tree.
func TestInspectReportsWhyATreeIsNotTheTasks(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		stray   func(t *testing.T, tree string)
		problem string
		branch  string
		// counts against the base branch; a detached HEAD has none
		ahead, behind int
	}{
		"on another branch": {
			stray:   func(t *testing.T, tree string) { gitRun(t, tree, "checkout", "-q", "-b", "side") },
			problem: "on branch side, not on the task branch task/MUL-1",
			branch:  "side",
		},
		"with a detached HEAD": {
			stray:   func(t *testing.T, tree string) { gitRun(t, tree, "checkout", "-q", "--detach") },
			problem: "HEAD is detached, not on the task branch task/MUL-1",
			branch:  "",
			ahead:   -1, behind: -1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newRepo(t, "app")
			layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-1", singleSpec(repo, "main"), testLogger())
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			tc.stray(t, layout.Dir)

			status, err := Inspect(layout.Dir)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			got := status.Trees[0]
			if got.Healthy || got.Problem != tc.problem {
				t.Errorf("the tree is healthy %v with problem %q, want %q", got.Healthy, got.Problem, tc.problem)
			}
			if got.Branch != tc.branch {
				t.Errorf("the tree is on %q, want %q", got.Branch, tc.branch)
			}
			if got.Repository != repo || got.BaseBranch != "main" || got.Ahead != tc.ahead || got.Behind != tc.behind {
				t.Errorf("the tree is %+v, want repository %s, base main, %d ahead, %d behind",
					got, repo, tc.ahead, tc.behind)
			}
		})
	}
}

// Only worktrees of the task directory are trees: a plain directory, a file,
// or a link standing in for a worktree elsewhere is left out.
func TestInspectIgnoresWhatIsNotATree(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-1", singleSpec(repo, "main"), testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	taskDir := filepath.Join(t.TempDir(), "MUL-2")
	writeFile(t, filepath.Join(taskDir, "plain", "file.txt"), "a plain directory\n")
	writeFile(t, filepath.Join(taskDir, "file.txt"), "a file\n")
	if err := os.Symlink(layout.Dir, filepath.Join(taskDir, "link")); err != nil {
		t.Fatal(err)
	}

	status, err := Inspect(taskDir)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(status.Trees) != 0 || status.Dir != taskDir {
		t.Errorf("Inspect reports %+v, want no trees in %s", status, taskDir)
	}
	// Nothing that is not a tree is removed on the task directory's behalf.
	if _, err := Delete(taskDir, testLogger()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !exists(filepath.Join(taskDir, "plain", "file.txt")) || !isWorkTree(layout.Dir) {
		t.Error("Delete removed something that is not a tree")
	}
}

// A task directory without a tree yields no trees; a missing one is an error.
func TestInspectOfATaskDirectoryWithoutTrees(t *testing.T) {
	t.Parallel()
	empty := filepath.Join(t.TempDir(), "MUL-1")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(empty)
	if err != nil || status == nil || len(status.Trees) != 0 {
		t.Errorf("Inspect of an empty task directory: %+v, %v", status, err)
	}

	missing := filepath.Join(t.TempDir(), "MUL-2")
	if status, err := Inspect(missing); status != nil || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Inspect of a missing task directory: %+v, %v", status, err)
	}
	if _, err := Inspect("relative/MUL-2"); err == nil {
		t.Error("Inspect accepted a relative path")
	} else {
		wantError(t, err, "absolute")
	}
	file := filepath.Join(t.TempDir(), "MUL-3")
	writeFile(t, file, "a file\n")
	if _, err := Inspect(file); err == nil {
		t.Error("Inspect accepted a file")
	} else {
		wantError(t, err, "not a directory")
	}
}

// The base branch is what Create recorded. Without the record — a tree Create
// did not make — there is nothing to count against.
func TestInspectWithoutTheBaseBranchRecord(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-7", singleSpec(repo, "main"), testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	commitFile(t, layout.Trees[0].Path, "work.txt", "done\n", "work on the task")
	gitDir := gitRun(t, layout.Dir, "rev-parse", "--absolute-git-dir")
	if err := os.Remove(filepath.Join(gitDir, baseBranchFile)); err != nil {
		t.Fatalf("remove the base branch record: %v", err)
	}

	status, err := Inspect(layout.Dir)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := status.Trees[0]
	if got.BaseBranch != "" || got.Ahead != -1 || got.Behind != -1 {
		t.Errorf("Inspect reports base %q, %d ahead, %d behind; want none, -1, -1", got.BaseBranch, got.Ahead, got.Behind)
	}
	if !got.Healthy || got.Repository != repo || got.Branch != "task/MUL-7" {
		t.Errorf("the tree's state is %+v", got)
	}
}

// The base branch record lives with the worktree's registration: it goes when
// the tree does, and Create writes it for a tree that stood without one.
func TestBaseBranchRecordIsWrittenAndGoesWithTheTree(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	gitRun(t, repo, "branch", "dev")
	root := filepath.Join(t.TempDir(), "root")
	// A tree on the task branch that Create did not make, so it has no record.
	gitRun(t, repo, "worktree", "add", "-q", filepath.Join(root, "MUL-7"), "-b", "task/MUL-7", "dev")
	layout, err := Create(root, "MUL-7", singleSpec(repo, "dev"), testLogger())
	if err != nil {
		t.Fatalf("Create over a hand-made tree: %v", err)
	}
	if got := treeHEAD(t, layout.Trees[0]); got != gitRun(t, repo, "rev-parse", "dev") {
		t.Fatalf("Create moved the hand-made tree to %s", got)
	}
	status, err := Inspect(layout.Dir)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := status.Trees[0]; !got.Healthy || got.BaseBranch != "dev" {
		t.Errorf("Inspect reports %+v, want a healthy tree recorded as cut from dev", got)
	}

	treeGitDir := gitRun(t, layout.Dir, "rev-parse", "--absolute-git-dir")
	if _, err := Delete(layout.Dir, testLogger()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if exists(treeGitDir) {
		t.Errorf("the removed tree's git dir %s, with the base branch record in it, was left behind", treeGitDir)
	}
}

func TestBranches(t *testing.T) {
	t.Parallel()
	origin := newRepo(t, "origin")
	commitFile(t, origin, "feature.txt", "feature\n", "on origin")
	gitRun(t, origin, "branch", "feature/x")
	repo := cloneRepo(t, origin, "clone")
	gitRun(t, repo, "branch", "local-only")
	gitRun(t, repo, "branch", "task/MUL-1")

	got, err := Branches(repo)
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	want := BranchList{
		Local:   []string{"local-only", "main", "task/MUL-1"},
		Remotes: []string{"origin/feature/x", "origin/main"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Branches = %+v, want %+v", got, want)
	}

	// From a subdirectory of the repository and from a linked worktree of it.
	svc := filepath.Join(repo, "svc")
	if err := os.MkdirAll(svc, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	gitRun(t, repo, "worktree", "add", "-q", "--detach", linked)
	for _, path := range []string{svc, linked} {
		if got, err := Branches(path); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Branches of %s = %+v, %v", path, got, err)
		}
	}
	plain := t.TempDir()
	if _, err := Branches(plain); err == nil {
		t.Error("Branches accepted a directory that is not a repository")
	} else {
		wantError(t, err, "list the branches of "+plain)
	}
}
