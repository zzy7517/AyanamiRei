package tasktree

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// dirtyCheckout gives the user's checkout uncommitted work in every form: a
// modified tracked file, a staged new file, an untracked file and a stash.
func dirtyCheckout(t *testing.T, repo string) {
	t.Helper()
	commitFile(t, repo, "tracked.txt", "committed\n", "add tracked")
	writeFile(t, filepath.Join(repo, "README"), "stashed edit\n")
	gitRun(t, repo, "stash", "push", "-q", "-m", "wip")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "edited, unstaged\n")
	writeFile(t, filepath.Join(repo, "staged.txt"), "staged\n")
	gitRun(t, repo, "add", "staged.txt")
	writeFile(t, filepath.Join(repo, "notes.txt"), "untracked\n")
}

func treeHEAD(t *testing.T, tree Tree) string {
	t.Helper()
	return gitRun(t, tree.Path, "rev-parse", "HEAD")
}

func taskBranches(repo string) string {
	out, _ := gitTry(repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/task/")
	return out
}

func singleSpec(repo, branch string) []Spec {
	return []Spec{{Repo: repo, BaseBranch: branch}}
}

// A tree is the latest commit of its base branch and of nothing else: the
// user's uncommitted work, in any form, is not carried into it.
func TestCreateStartsFromTheBaseTipWithoutTheUsersChanges(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	dirtyCheckout(t, repo)
	gitRun(t, repo, "branch", "dev")
	base := gitRun(t, repo, "rev-parse", "main")
	root := filepath.Join(t.TempDir(), "root")
	before := snapshotUser(t, repo)

	layout, err := Create(root, "MUL-7", singleSpec(repo, "main"), testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(layout.Trees) != 1 {
		t.Fatalf("layout has %d trees, want 1", len(layout.Trees))
	}
	tree := layout.Trees[0]
	if tree.Path != filepath.Join(root, "MUL-7") || tree.Branch != "task/MUL-7" {
		t.Fatalf("tree is %s on %s, want %s on task/MUL-7", tree.Path, tree.Branch, filepath.Join(root, "MUL-7"))
	}
	if got := treeHEAD(t, tree); got != base {
		t.Errorf("the tree starts at %s, want the base tip %s", got, base)
	}
	if got := gitRun(t, tree.Path, "symbolic-ref", "--short", "HEAD"); got != "task/MUL-7" {
		t.Errorf("the tree is on %s, want task/MUL-7", got)
	}
	if got := gitRun(t, tree.Path, "status", "--porcelain"); got != "" {
		t.Errorf("the tree is not clean:\n%s", got)
	}
	if got := readFile(t, filepath.Join(tree.Path, "tracked.txt")); got != "committed\n" {
		t.Errorf("the user's edit to tracked.txt was carried into the tree: %q", got)
	}
	for _, name := range []string{"notes.txt", "staged.txt"} {
		if exists(filepath.Join(tree.Path, name)) {
			t.Errorf("%s of the user's checkout was carried into the tree", name)
		}
	}

	assertUserUntouched(t, before, snapshotUser(t, repo))
}

// `git worktree add <path> -b <branch> origin/main` records origin/main as the
// branch's upstream in the user's .git/config. The tree is cut from the
// commit, so the repository's configuration stays the user's.
func TestCreateFromRemoteTrackingBaseLeavesTheRepositoryConfigAlone(t *testing.T) {
	t.Parallel()
	origin := newRepo(t, "origin")
	tip := commitFile(t, origin, "later.txt", "only on the remote\n", "on origin")
	repo := cloneRepo(t, origin, "clone")
	before := snapshotUser(t, repo)

	layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-1", singleSpec(repo, "origin/main"), testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := treeHEAD(t, layout.Trees[0]); got != tip {
		t.Errorf("the tree starts at %s, want origin/main's tip %s", got, tip)
	}
	if _, err := gitTry(repo, "config", "--get-regexp", `^branch\.task/`); err == nil {
		t.Error("the repository config has a section for the task branch")
	}
	assertUserUntouched(t, before, snapshotUser(t, repo))
}

// The branch is where a task's work lives: a tree removed and rebuilt goes on
// standing on that work, even once the base branch has moved on.
func TestCreateRebuildContinuesTheTaskBranch(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	root := filepath.Join(t.TempDir(), "root")
	specs := singleSpec(repo, "main")
	layout, err := Create(root, "MUL-7", specs, testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	work := commitFile(t, layout.Trees[0].Path, "work.txt", "done\n", "work on the task")
	commitFile(t, repo, "main.txt", "moved on\n", "on main")
	moved := gitRun(t, repo, "rev-parse", "main")

	if _, err := Delete(layout.Dir, testLogger()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := gitRun(t, repo, "rev-parse", "task/MUL-7"); got != work {
		t.Fatalf("task branch is at %s after Delete, want the task's commit %s", got, work)
	}

	rebuilt, err := Create(root, "MUL-7", specs, testLogger())
	if err != nil {
		t.Fatalf("Create after Delete: %v", err)
	}
	if got := treeHEAD(t, rebuilt.Trees[0]); got != work {
		t.Errorf("the rebuilt tree starts at %s, want the task's commit %s (base moved to %s)", got, work, moved)
	}
	status, err := Inspect(layout.Dir)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(status.Trees) != 1 || status.Trees[0].Ahead != 1 || status.Trees[0].Behind != 1 {
		t.Fatalf("rebuilt tree reports %+v, want ahead 1 behind 1", status.Trees)
	}
}

// A tree deleted with rm -rf leaves its registration behind in the repository.
// The rebuild clears that registration instead of failing on it.
func TestCreateRebuildsAfterTheDirectoryWasRemovedByHand(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	root := filepath.Join(t.TempDir(), "root")
	specs := singleSpec(repo, "main")
	layout, err := Create(root, "MUL-7", specs, testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	work := commitFile(t, layout.Trees[0].Path, "work.txt", "done\n", "work on the task")
	if err := os.RemoveAll(layout.Dir); err != nil {
		t.Fatalf("remove the tree by hand: %v", err)
	}
	if got := registeredWorktrees(t, repo); len(got) != 2 {
		t.Fatalf("the repository lists %q, want the main worktree and the stale registration", got)
	}

	rebuilt, err := Create(root, "MUL-7", specs, testLogger())
	if err != nil {
		t.Fatalf("Create after a removal by hand: %v", err)
	}
	if got := treeHEAD(t, rebuilt.Trees[0]); got != work {
		t.Errorf("the rebuilt tree starts at %s, want the task's commit %s", got, work)
	}
	if got := registeredWorktrees(t, repo); len(got) != 2 {
		t.Errorf("the repository lists %q, want the main worktree and the rebuilt tree only", got)
	}
}

// A healthy tree is left alone: not its commits, not its uncommitted work.
func TestCreateIsIdempotent(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	root := filepath.Join(t.TempDir(), "root")
	specs := singleSpec(repo, "main")
	layout, err := Create(root, "MUL-7", specs, testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tree := layout.Trees[0]
	commitFile(t, tree.Path, "work.txt", "done\n", "work on the task")
	writeFile(t, filepath.Join(tree.Path, "draft.txt"), "uncommitted\n")
	work := treeHEAD(t, tree)
	before := registeredWorktrees(t, repo)

	again, err := Create(root, "MUL-7", specs, testLogger())
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if !reflect.DeepEqual(again, layout) {
		t.Fatalf("second Create laid out %+v, want %+v", again, layout)
	}
	if got := treeHEAD(t, tree); got != work {
		t.Errorf("the second Create moved the tree to %s from %s", got, work)
	}
	if got := readFile(t, filepath.Join(tree.Path, "draft.txt")); got != "uncommitted\n" {
		t.Errorf("the second Create disturbed the tree's uncommitted work: %q", got)
	}
	if got := registeredWorktrees(t, repo); !reflect.DeepEqual(got, before) {
		t.Errorf("the second Create changed the registrations to %q from %q", got, before)
	}
}

// Two or more trees: the task directory is a plain directory, each tree is the
// subdirectory named by its slug, and one repository on two branches yields
// two trees.
func TestCreateManyTrees(t *testing.T) {
	t.Parallel()
	app := newRepo(t, "app")
	gitRun(t, app, "branch", "dev")
	commitFile(t, app, "main.txt", "main\n", "on main")
	web := newRepo(t, "web")
	specs := []Spec{
		{Repo: app, BaseBranch: "main"},
		{Repo: app, BaseBranch: "dev"},
		{Repo: web, BaseBranch: "main"},
	}

	layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-2", specs, testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if isWorkTree(layout.Dir) {
		t.Fatalf("task directory %s is a worktree; with several trees it is a plain directory", layout.Dir)
	}
	var slugs []string
	for i, tree := range layout.Trees {
		slugs = append(slugs, tree.Slug)
		if !isWorkTree(tree.Path) {
			t.Errorf("tree %d at %s is not a worktree", i+1, tree.Path)
		}
		if tree.Path != filepath.Join(layout.Dir, tree.Slug) {
			t.Errorf("tree %d is at %s, want %s", i+1, tree.Path, filepath.Join(layout.Dir, tree.Slug))
		}
		if want := "task/MUL-2/" + tree.Slug; tree.Branch != want {
			t.Errorf("tree %d is on %s, want %s", i+1, tree.Branch, want)
		}
		if want := gitRun(t, tree.Repo, "rev-parse", tree.BaseBranch); treeHEAD(t, tree) != want {
			t.Errorf("tree %d starts at %s, want the tip of %s (%s)", i+1, treeHEAD(t, tree), tree.BaseBranch, want)
		}
		if got := gitRun(t, tree.Path, "symbolic-ref", "--short", "HEAD"); got != tree.Branch {
			t.Errorf("tree %d has %s checked out, want %s", i+1, got, tree.Branch)
		}
	}
	if want := []string{"app--main", "app--dev", "web"}; !reflect.DeepEqual(slugs, want) {
		t.Errorf("slugs = %q, want %q", slugs, want)
	}

	status, err := Inspect(layout.Dir)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var names []string
	for _, tree := range status.Trees {
		names = append(names, tree.Name)
		if !tree.Healthy {
			t.Errorf("tree %s is not healthy: %s", tree.Name, tree.Problem)
		}
	}
	if want := []string{"app--dev", "app--main", "web"}; !reflect.DeepEqual(names, want) {
		t.Errorf("Inspect lists %q, want %q", names, want)
	}
}

// The spec may name any directory of the repository; the slug is the base
// name of the path as given, and the tree covers the whole repository.
func TestCreateFromASubdirectoryOfTheRepository(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	commitFile(t, repo, filepath.Join("svc", "main.go"), "package main\n", "add svc")

	layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-1", singleSpec(filepath.Join(repo, "svc"), "main"), testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tree := layout.Trees[0]
	if tree.Slug != "svc" {
		t.Errorf("slug = %q, want the base name of the path as given: svc", tree.Slug)
	}
	if got := gitRun(t, tree.Path, "rev-parse", "--show-toplevel"); got != tree.Path {
		t.Errorf("the tree's top level is %s, want the whole repository at %s", got, tree.Path)
	}
	status, err := Inspect(layout.Dir)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := status.Trees[0].Repository; got != repo {
		t.Errorf("Inspect reports the repository as %s, want %s", got, repo)
	}
}

// The root may be reached through a link. A tree made through one spelling is
// the same tree through the other.
func TestCreateThroughASymlinkedRoot(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	specs := singleSpec(repo, "main")
	if _, err := Create(alias, "MUL-1", specs, testLogger()); err != nil {
		t.Fatalf("Create through the link: %v", err)
	}
	if _, err := Create(real, "MUL-1", specs, testLogger()); err != nil {
		t.Fatalf("Create through the real path did not recognize its own tree: %v", err)
	}
	if got := registeredWorktrees(t, repo); len(got) != 2 {
		t.Errorf("the repository lists %q, want the main worktree and the one tree", got)
	}
}

// Everything checkable is checked before anything is created: a refused
// Create leaves the repository, the task directory and the root as they were.
func TestCreateRefusesBeforeCreatingAnything(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		// arrange builds the situation and returns the root and specs to try.
		arrange  func(t *testing.T, repo, root, dir string) (string, []Spec)
		mentions []string
	}{
		"the repository does not exist": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				return root, singleSpec(filepath.Join(filepath.Dir(repo), "missing"), "main")
			},
			[]string{"does not exist"},
		},
		"the repository is not a git working tree": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				plain := filepath.Join(filepath.Dir(repo), "plain")
				if err := os.MkdirAll(plain, 0o755); err != nil {
					t.Fatal(err)
				}
				return root, singleSpec(plain, "main")
			},
			[]string{"not a git working tree"},
		},
		"the repository has no commit": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				empty := filepath.Join(filepath.Dir(repo), "empty")
				if err := os.MkdirAll(empty, 0o755); err != nil {
					t.Fatal(err)
				}
				gitRun(t, empty, "init", "-q", "-b", "main")
				return root, singleSpec(empty, "main")
			},
			[]string{"no commit"},
		},
		"the base branch does not exist": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				return root, singleSpec(repo, "nope")
			},
			[]string{"no local or remote-tracking branch", `"nope"`},
		},
		"the same repository and base branch twice": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				sub := filepath.Join(repo, "sub")
				if err := os.MkdirAll(sub, 0o755); err != nil {
					t.Fatal(err)
				}
				return root, []Spec{{Repo: repo, BaseBranch: "main"}, {Repo: sub, BaseBranch: "main"}}
			},
			[]string{"listed twice with base branch"},
		},
		"the task directory is a plain directory with a file in it": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				writeFile(t, filepath.Join(dir, "keep.txt"), "not mine\n")
				return root, singleSpec(repo, "main")
			},
			[]string{"exists and is not a worktree"},
		},
		"the task directory is a file": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				writeFile(t, dir, "a file\n")
				return root, singleSpec(repo, "main")
			},
			[]string{"not a directory"},
		},
		"the task directory is a worktree of another repository": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				other := newRepo(t, "other")
				gitRun(t, other, "worktree", "add", "-q", dir, "-b", "side", "main")
				return root, singleSpec(repo, "main")
			},
			[]string{"worktree of another repository"},
		},
		"the tree is on another branch": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				if _, err := Create(root, "MUL-1", singleSpec(repo, "main"), testLogger()); err != nil {
					t.Fatal(err)
				}
				gitRun(t, dir, "checkout", "-q", "-b", "side")
				return root, singleSpec(repo, "main")
			},
			[]string{"on branch side, not on the task branch task/MUL-1"},
		},
		"the tree has a detached HEAD": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				if _, err := Create(root, "MUL-1", singleSpec(repo, "main"), testLogger()); err != nil {
					t.Fatal(err)
				}
				gitRun(t, dir, "checkout", "-q", "--detach")
				return root, singleSpec(repo, "main")
			},
			[]string{"detached HEAD"},
		},
		"the task branch is checked out elsewhere": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				gitRun(t, repo, "branch", "task/MUL-1", "main")
				gitRun(t, repo, "worktree", "add", "-q", filepath.Join(filepath.Dir(root), "elsewhere"), "task/MUL-1")
				return root, singleSpec(repo, "main")
			},
			[]string{"task/MUL-1 is checked out at"},
		},
		"the task root is inside a repository checkout": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				return filepath.Join(repo, "trees"), singleSpec(repo, "main")
			},
			[]string{"inside the git working tree", "<repo>"},
		},
		"the task root is a repository checkout itself": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				return repo, singleSpec(repo, "main")
			},
			[]string{"inside the git working tree"},
		},
		"a one-tree task grown to two trees": {
			func(t *testing.T, repo, root, dir string) (string, []Spec) {
				if _, err := Create(root, "MUL-1", singleSpec(repo, "main"), testLogger()); err != nil {
					t.Fatal(err)
				}
				return root, []Spec{{Repo: repo, BaseBranch: "main"}, {Repo: repo, BaseBranch: "dev"}}
			},
			[]string{"cannot hold the 2 trees"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newRepo(t, "app")
			base := filepath.Dir(repo)
			root, specs := tc.arrange(t, repo, filepath.Join(base, "root"), filepath.Join(base, "root", "MUL-1"))
			branches, worktrees, rootExisted := taskBranches(repo), registeredWorktrees(t, repo), exists(root)

			layout, err := Create(root, "MUL-1", specs, testLogger())
			if layout != nil {
				t.Fatalf("Create returned %+v, want nil", layout)
			}
			// "<repo>" in a mention stands for the checkout's path.
			mentions := make([]string, len(tc.mentions))
			for i, mention := range tc.mentions {
				mentions[i] = strings.ReplaceAll(mention, "<repo>", repo)
			}
			wantError(t, err, append([]string{"tasktree:"}, mentions...)...)
			if got := taskBranches(repo); got != branches {
				t.Errorf("the task branches changed:\nbefore %q\nafter  %q", branches, got)
			}
			if got := registeredWorktrees(t, repo); !reflect.DeepEqual(got, worktrees) {
				t.Errorf("the registrations changed:\nbefore %q\nafter  %q", worktrees, got)
			}
			if got := exists(root); got != rootExisted {
				t.Errorf("the task root existing changed from %v to %v", rootExisted, got)
			}
		})
	}
}

// A failure after the first tree was made undoes that tree: the worktree and
// the task branch, which was new and empty. Nothing of the task is left in
// either repository or on disk.
func TestCreateRollsBackWhatItAdded(t *testing.T) {
	t.Parallel()
	app := newRepo(t, "app")
	web := newRepo(t, "web")
	// git registers a worktree before its post-checkout hook runs, and a
	// failing hook fails the add: the worst case, made to order.
	installHook(t, web, "post-checkout", "#!/bin/sh\nexit 1\n")
	root := filepath.Join(t.TempDir(), "root")

	layout, err := Create(root, "MUL-3", []Spec{
		{Repo: app, BaseBranch: "main"},
		{Repo: web, BaseBranch: "main"},
	}, testLogger())
	if layout != nil {
		t.Fatalf("Create returned %+v, want nil", layout)
	}
	wantError(t, err, filepath.Join(root, "MUL-3", "web"))

	for _, repo := range []string{app, web} {
		if got := taskBranches(repo); got != "" {
			t.Errorf("%s is left with task branches:\n%s", repo, got)
		}
		if got := registeredWorktrees(t, repo); len(got) != 1 {
			t.Errorf("%s is left with worktrees %q, want only its own", repo, got)
		}
	}
	if exists(root) {
		t.Errorf("the task root %s was left behind", root)
	}
}

// A rollback removes what the failed call added and nothing older: a task
// branch that survived an earlier tree stays, with its commits.
func TestCreateRollbackKeepsAnOlderTaskBranch(t *testing.T) {
	t.Parallel()
	app := newRepo(t, "app")
	web := newRepo(t, "web")
	root := filepath.Join(t.TempDir(), "root")
	specs := []Spec{{Repo: app, BaseBranch: "main"}, {Repo: web, BaseBranch: "main"}}
	layout, err := Create(root, "MUL-3", specs, testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	work := commitFile(t, layout.Trees[0].Path, "work.txt", "done\n", "work on the task")
	if _, err := Delete(layout.Dir, testLogger()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Both task branches survived; now the second tree cannot come back.
	installHook(t, web, "post-checkout", "#!/bin/sh\nexit 1\n")
	if _, err := Create(root, "MUL-3", specs, testLogger()); err == nil {
		t.Fatal("Create succeeded although a tree could not be made")
	}

	if got := registeredWorktrees(t, app); len(got) != 1 {
		t.Errorf("the first repository lists %q, want only its own worktree", got)
	}
	if got := registeredWorktrees(t, web); len(got) != 1 {
		t.Errorf("the second repository lists %q, want only its own worktree", got)
	}
	if got := gitRun(t, app, "rev-parse", "task/MUL-3/app"); got != work {
		t.Errorf("task/MUL-3/app is at %s, want the task's commit %s", got, work)
	}
	if _, err := gitTry(web, "rev-parse", "--verify", "--quiet", "task/MUL-3/web"); err != nil {
		t.Error("the rollback deleted task/MUL-3/web, which existed before the failed Create")
	}
	// The root stood before the failed call; the task directory did not.
	if exists(layout.Dir) {
		t.Errorf("the task directory %s was left behind", layout.Dir)
	}
}

// A task branch the failed call created is rolled back only while it is still
// as new as it was made: one that received a commit meanwhile keeps the commit.
func TestCreateRollbackKeepsABranchThatReceivedACommit(t *testing.T) {
	t.Parallel()
	app := newRepo(t, "app")
	web := newRepo(t, "web")
	base := gitRun(t, app, "rev-parse", "main")
	// The first repository's post-checkout hook commits onto the tree it sees
	// created; the second repository's fails the add.
	installHook(t, app, "post-checkout", "#!/bin/sh\ngit commit -q --allow-empty --no-verify -m 'landed meanwhile'\n")
	installHook(t, web, "post-checkout", "#!/bin/sh\nexit 1\n")
	root := filepath.Join(t.TempDir(), "root")

	if _, err := Create(root, "MUL-3", []Spec{
		{Repo: app, BaseBranch: "main"},
		{Repo: web, BaseBranch: "main"},
	}, testLogger()); err == nil {
		t.Fatal("Create succeeded although a tree could not be made")
	}

	if got := gitRun(t, app, "log", "-1", "--format=%s", "task/MUL-3/app"); got != "landed meanwhile" {
		t.Errorf("task/MUL-3/app is at %q, want the commit that landed on it meanwhile", got)
	}
	if got := gitRun(t, app, "rev-parse", "task/MUL-3/app^"); got != base {
		t.Errorf("that commit's parent is %s, want the base tip %s", got, base)
	}
	if got := registeredWorktrees(t, app); len(got) != 1 {
		t.Errorf("the first repository lists %q, want only its own worktree", got)
	}
	if got := taskBranches(web); got != "" {
		t.Errorf("the second repository is left with task branches:\n%s", got)
	}
	if exists(root) {
		t.Errorf("the task root %s was left behind", root)
	}
}

// A rollback removes what the failed call made and nothing else. When the
// registration left behind at the path cannot be cleared, Create fails before
// making the tree — and the task branch that survived the earlier tree, with
// the work on it, is not touched.
func TestCreateFailureBeforeTheAddLeavesTheTaskBranchAlone(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root is not stopped by directory permissions")
	}
	repo := newRepo(t, "app")
	root := filepath.Join(t.TempDir(), "root")
	specs := singleSpec(repo, "main")
	layout, err := Create(root, "MUL-1", specs, testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	work := commitFile(t, layout.Trees[0].Path, "work.txt", "done\n", "work on the task")
	if err := os.RemoveAll(layout.Dir); err != nil {
		t.Fatalf("remove the tree by hand: %v", err)
	}
	// git cannot drop the stale registration while it may not write there.
	admin := filepath.Join(repo, ".git", "worktrees")
	if err := os.Chmod(admin, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(admin, 0o755)

	_, err = Create(root, "MUL-1", specs, testLogger())
	wantError(t, err, "clear the worktree registration")
	if got := gitRun(t, repo, "rev-parse", "task/MUL-1"); got != work {
		t.Errorf("task/MUL-1 is at %s, want the task's commit %s", got, work)
	}
}

// Work on a task tree never writes to the user's checkout: not its files, not
// its index, not its stash, not HEAD, and no ref but the task's own.
func TestUserCheckoutIsUntouchedThroughout(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	dirtyCheckout(t, repo)
	gitRun(t, repo, "branch", "dev")
	root := filepath.Join(t.TempDir(), "root")
	specs := singleSpec(repo, "main")
	before := snapshotUser(t, repo)

	// Deleted with nothing committed of its own: what it left uncommitted is
	// committed to the task branch.
	layout, err := Create(root, "MUL-7", specs, testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	writeFile(t, filepath.Join(layout.Trees[0].Path, "draft.txt"), "draft\n")
	deleted, err := Delete(layout.Dir, testLogger())
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !deleted.Trees[0].AutoCommitted {
		t.Errorf("Delete did not commit the uncommitted work: %+v", deleted.Trees[0])
	}

	// Deleted with a commit of its own: what it then left uncommitted is kept
	// under a hidden ref in the repository.
	layout, err = Create(root, "MUL-7", specs, testLogger())
	if err != nil {
		t.Fatalf("Create after Delete: %v", err)
	}
	writeFile(t, filepath.Join(layout.Trees[0].Path, "leftover.txt"), "left over\n")
	if _, err := Create(root, "MUL-7", specs, testLogger()); err != nil {
		t.Fatalf("Create again: %v", err)
	}
	if _, err := Inspect(layout.Dir); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	deleted, err = Delete(layout.Dir, testLogger())
	if err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if deleted.Trees[0].LeftoverRef == "" {
		t.Fatalf("second Delete did not save the leftovers: %+v", deleted.Trees[0])
	}

	// Deleted clean.
	if _, err := Create(root, "MUL-7", specs, testLogger()); err != nil {
		t.Fatalf("Create after the second Delete: %v", err)
	}
	if _, err := Delete(layout.Dir, testLogger()); err != nil {
		t.Fatalf("third Delete: %v", err)
	}

	assertUserUntouched(t, before, snapshotUser(t, repo))
	if _, err := gitTry(repo, "rev-parse", "--verify", "--quiet", "task/MUL-7"); err != nil {
		t.Error("the task branch did not survive the deletions")
	}
}

// blockedCreate is a Create running in the background, parked inside
// `git worktree add` by a post-checkout hook that waits for a release file. By
// then the task directory is a worktree on disk and Create holds the task's
// mutex.
type blockedCreate struct {
	repo    string
	taskDir string
	created <-chan error
	release func()
}

func startBlockedCreate(t *testing.T) blockedCreate {
	t.Helper()
	repo := newRepo(t, "app")
	control := t.TempDir()
	started, releaseFile := filepath.Join(control, "started"), filepath.Join(control, "release")
	installHook(t, repo, "post-checkout", "#!/bin/sh\ntouch '"+started+"'\nwhile [ ! -e '"+releaseFile+"' ]; do sleep 0.01; done\n")
	root := filepath.Join(control, "root")
	created := make(chan error, 1)
	go func() {
		_, err := Create(root, "MUL-1", singleSpec(repo, "main"), testLogger())
		created <- err
	}()
	waitForFile(t, started)
	release := func() {
		if err := os.WriteFile(releaseFile, nil, 0o644); err != nil {
			t.Errorf("release the hook: %v", err)
		}
	}
	// Whatever the test finds, the hook is let go and Create runs dry.
	t.Cleanup(release)
	return blockedCreate{
		repo:    repo,
		taskDir: taskDirOf(t, root, "MUL-1"),
		created: created,
		release: release,
	}
}

// alongside starts op while Create is parked, fails when op returns before
// Create does, then lets Create finish and returns op's own result.
func (c blockedCreate) alongside(t *testing.T, name string, op func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- op() }()
	select {
	case err := <-done:
		t.Fatalf("%s returned (%v) while Create was still running on the same task", name, err)
	case <-time.After(200 * time.Millisecond):
	}
	c.release()
	if err := <-c.created; err != nil {
		t.Fatalf("Create: %v", err)
	}
	return <-done
}

func TestInspectWaitsForCreateOnTheSameTask(t *testing.T) {
	t.Parallel()
	create := startBlockedCreate(t)
	var status *TaskStatus
	if err := create.alongside(t, "Inspect", func() error {
		var err error
		status, err = Inspect(create.taskDir)
		return err
	}); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	// Inspect saw the finished tree, not one halfway through its creation.
	if len(status.Trees) != 1 || !status.Trees[0].Healthy || status.Trees[0].Branch != "task/MUL-1" {
		t.Errorf("Inspect reports %+v", status.Trees)
	}
}

func TestDeleteWaitsForCreateOnTheSameTask(t *testing.T) {
	t.Parallel()
	create := startBlockedCreate(t)
	if err := create.alongside(t, "Delete", func() error {
		_, err := Delete(create.taskDir, testLogger())
		return err
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Delete removed the finished tree, not one halfway through its creation.
	if exists(create.taskDir) {
		t.Errorf("the task directory %s survived Delete", create.taskDir)
	}
	if got := registeredWorktrees(t, create.repo); len(got) != 1 {
		t.Errorf("the repository lists %q, want only its own worktree", got)
	}
	if _, err := gitTry(create.repo, "rev-parse", "--verify", "--quiet", "task/MUL-1"); err != nil {
		t.Error("Delete removed the task branch")
	}
}
