package tasktree

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// treeNames lists the trees Inspect reports in a task directory.
func treeNames(t *testing.T, taskDir string) []string {
	t.Helper()
	status, err := Inspect(taskDir)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var names []string
	for _, tree := range status.Trees {
		names = append(names, tree.Name)
	}
	return names
}

// A branch without a commit of its own takes everything left uncommitted: the
// rule worktree mode documents, down to ignoring ignored files and not running
// the user's commit hooks.
func TestDeleteCommitsUncommittedWorkWhenTheBranchHasNoCommitOfItsOwn(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-7", singleSpec(repo, "main"), testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tree := layout.Trees[0]
	base := treeHEAD(t, tree)
	ran := filepath.Join(t.TempDir(), "pre-commit ran")
	installHook(t, repo, "pre-commit", "#!/bin/sh\ntouch '"+ran+"'\n")
	writeFile(t, filepath.Join(tree.Path, "README"), "edited\n")
	writeFile(t, filepath.Join(tree.Path, "staged.txt"), "staged\n")
	gitRun(t, tree.Path, "add", "staged.txt")
	writeFile(t, filepath.Join(tree.Path, "untracked.txt"), "untracked\n")
	writeFile(t, filepath.Join(tree.Path, ".gitignore"), "secret.env\n")
	writeFile(t, filepath.Join(tree.Path, "secret.env"), "ignored\n")

	result, err := Delete(layout.Dir, testLogger())
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(result.Trees) != 1 {
		t.Fatalf("Delete reported %+v, want one tree", result.Trees)
	}
	out := result.Trees[0]
	if out.Path != tree.Path || out.Branch != "task/MUL-7" || !out.AutoCommitted || out.LeftoverRef != "" || out.Kept || out.Err != nil {
		t.Errorf("Delete reported %+v", out)
	}
	if exists(tree.Path) || exists(layout.Dir) {
		t.Errorf("the tree %s was left behind", tree.Path)
	}
	if got := registeredWorktrees(t, repo); len(got) != 1 {
		t.Errorf("the repository lists %q, want only its own worktree", got)
	}
	if exists(ran) {
		t.Error("the repository's pre-commit hook ran")
	}

	tip := gitRun(t, repo, "rev-parse", "task/MUL-7")
	if tip == base {
		t.Fatal("the uncommitted work was not committed to the task branch")
	}
	if got := gitRun(t, repo, "rev-parse", "task/MUL-7^"); got != base {
		t.Errorf("the commit's parent is %s, want the base tip %s", got, base)
	}
	var files []string
	for _, name := range strings.Split(gitRun(t, repo, "ls-tree", "-r", "--name-only", "task/MUL-7"), "\n") {
		files = append(files, name)
	}
	sort.Strings(files)
	if want := []string{".gitignore", "README", "staged.txt", "untracked.txt"}; !reflect.DeepEqual(files, want) {
		t.Errorf("the commit holds %q, want %q (the ignored file stays out)", files, want)
	}
	if got := gitRun(t, repo, "show", "task/MUL-7:README"); got != "edited" {
		t.Errorf("the commit's README is %q, want the edit", got)
	}
	if got := gitRun(t, repo, "show", "task/MUL-7:untracked.txt"); got != "untracked" {
		t.Errorf("the commit's untracked.txt is %q", got)
	}
	if got := gitRun(t, repo, "log", "-1", "--format=%an <%ae>", "task/MUL-7"); got != "Test User <test@test.com>" {
		t.Errorf("the commit's author is %q, want the repository's own identity", got)
	}
	if got := gitRun(t, repo, "log", "-1", "--format=%s", "task/MUL-7"); got != "chore(task): uncommitted changes from task/MUL-7" {
		t.Errorf("the commit's subject is %q", got)
	}
}

// A branch with commits of its own keeps them as they are: what the tree left
// uncommitted goes under a hidden ref, as one commit on top of the branch's
// tip that `git restore --source` can bring back.
func TestDeleteSavesUncommittedWorkUnderAHiddenRefWhenTheBranchHasItsOwnCommits(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	root := filepath.Join(t.TempDir(), "root")
	specs := singleSpec(repo, "main")
	layout, err := Create(root, "MUL-7", specs, testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tree := layout.Trees[0]
	work := commitFile(t, tree.Path, "work.txt", "done\n", "work on the task")
	writeFile(t, filepath.Join(tree.Path, "work.txt"), "edited after the commit\n")
	writeFile(t, filepath.Join(tree.Path, "new.txt"), "untracked\n")
	before := snapshotUser(t, repo)

	result, err := Delete(layout.Dir, testLogger())
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	out := result.Trees[0]
	if out.AutoCommitted || out.Kept || out.Err != nil {
		t.Fatalf("Delete reported %+v", out)
	}
	ref := out.LeftoverRef
	if !strings.HasPrefix(ref, "refs/multica/leftovers/task/MUL-7/") || len(ref) <= len("refs/multica/leftovers/task/MUL-7/") {
		t.Fatalf("the leftovers went to %q", ref)
	}
	if got := gitRun(t, repo, "rev-parse", "task/MUL-7"); got != work {
		t.Errorf("the task branch moved to %s, want the task's own commit %s", got, work)
	}
	if got := gitRun(t, repo, "rev-parse", ref+"^"); got != work {
		t.Errorf("the leftovers' parent is %s, want the branch tip %s", got, work)
	}
	if got := gitRun(t, repo, "show", ref+":work.txt"); got != "edited after the commit" {
		t.Errorf("the leftovers hold work.txt as %q", got)
	}
	if _, err := gitTry(repo, "cat-file", "-e", ref+":new.txt"); err != nil {
		t.Error("the leftovers do not hold the untracked file")
	}
	assertUserUntouched(t, before, snapshotUser(t, repo))

	// The saved work comes back into a rebuilt tree.
	layout, err = Create(root, "MUL-7", specs, testLogger())
	if err != nil {
		t.Fatalf("Create after Delete: %v", err)
	}
	gitRun(t, layout.Trees[0].Path, "restore", "--source="+ref, "--", "work.txt")
	if got := readFile(t, filepath.Join(layout.Trees[0].Path, "work.txt")); got != "edited after the commit\n" {
		t.Errorf("restored work.txt is %q", got)
	}
}

// When the work cannot be committed, the tree stays as it is, and Delete says
// so. Nothing is lost by trying again once it can.
func TestDeleteKeepsTheTreeWhenTheWorkCannotBeCommitted(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-7", singleSpec(repo, "main"), testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tree := layout.Trees[0]
	base := treeHEAD(t, tree)
	writeFile(t, filepath.Join(tree.Path, "draft.txt"), "draft\n")
	breakCommitting(t, repo)

	result, err := Delete(layout.Dir, testLogger())
	wantError(t, err, "could not commit the uncommitted changes to task/MUL-7", tree.Path)
	out := result.Trees[0]
	if !out.Kept || out.Err == nil || out.AutoCommitted || out.LeftoverRef != "" {
		t.Errorf("Delete reported %+v", out)
	}
	if !exists(filepath.Join(tree.Path, "draft.txt")) {
		t.Error("the tree's uncommitted work is gone")
	}
	if got := treeHEAD(t, tree); got != base {
		t.Errorf("the task branch moved to %s from %s", got, base)
	}
	if got := registeredWorktrees(t, repo); len(got) != 2 {
		t.Errorf("the repository lists %q, want its own worktree and the tree", got)
	}

	gitRun(t, repo, "config", "--unset", "commit.gpgSign")
	result, err = Delete(layout.Dir, testLogger())
	if err != nil {
		t.Fatalf("Delete once commits work again: %v", err)
	}
	if !result.Trees[0].AutoCommitted || exists(tree.Path) {
		t.Errorf("the retry reported %+v and the tree still exists: %v", result.Trees[0], exists(tree.Path))
	}
	if got := gitRun(t, repo, "show", "task/MUL-7:draft.txt"); got != "draft" {
		t.Errorf("the commit's draft.txt is %q", got)
	}
}

// An unresolved merge is never committed: `git add -A` would turn the conflict
// markers into a commit. The tree is left for someone to finish the merge.
func TestDeleteKeepsATreeWithAnUnresolvedMerge(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	gitRun(t, repo, "checkout", "-q", "-b", "side")
	commitFile(t, repo, "README", "the side's readme\n", "on side")
	gitRun(t, repo, "checkout", "-q", "main")
	commitFile(t, repo, "README", "main's readme\n", "on main")
	layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-7", singleSpec(repo, "main"), testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tree := layout.Trees[0]
	base := treeHEAD(t, tree)
	if out, err := gitTry(tree.Path, "merge", "side"); err == nil || !strings.Contains(out, "CONFLICT") {
		t.Fatalf("the merge did not conflict: %s (%v)", out, err)
	}

	result, err := Delete(layout.Dir, testLogger())
	wantError(t, err, "still unmerged", "README")
	if !result.Trees[0].Kept {
		t.Errorf("Delete reported %+v", result.Trees[0])
	}
	if !exists(tree.Path) || !isWorkTree(tree.Path) {
		t.Errorf("the tree %s is gone", tree.Path)
	}
	if got := registeredWorktrees(t, repo); len(got) != 2 {
		t.Errorf("the repository lists %q, want its own worktree and the tree", got)
	}
	if got := gitRun(t, tree.Path, "ls-files", "-u"); got == "" {
		t.Error("the tree no longer holds the unresolved merge")
	}
	if got := treeHEAD(t, tree); got != base {
		t.Errorf("the tree moved to %s from %s", got, base)
	}
}

// Each tree is handled on its own: one that has to stay does not keep the
// others from going.
func TestDeleteHandlesEachTreeOnItsOwn(t *testing.T) {
	t.Parallel()
	app := newRepo(t, "app")
	web := newRepo(t, "web")
	breakCommitting(t, web)
	layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-3", []Spec{
		{Repo: app, BaseBranch: "main"},
		{Repo: web, BaseBranch: "main"},
	}, testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, tree := range layout.Trees {
		writeFile(t, filepath.Join(tree.Path, "draft.txt"), "draft\n")
	}

	result, err := Delete(layout.Dir, testLogger())
	wantError(t, err, filepath.Join(layout.Dir, "web"))
	if len(result.Trees) != 2 {
		t.Fatalf("Delete reported %+v, want two trees", result.Trees)
	}
	if got := result.Trees[0]; got.Kept || got.Err != nil || exists(got.Path) {
		t.Errorf("the first tree: %+v, exists %v", got, exists(got.Path))
	}
	if got := result.Trees[1]; !got.Kept || got.Err == nil || !exists(got.Path) {
		t.Errorf("the second tree: %+v, exists %v", got, exists(got.Path))
	}
	if names := treeNames(t, layout.Dir); !reflect.DeepEqual(names, []string{"web"}) {
		t.Errorf("the task directory holds %q, want the kept tree only", names)
	}
	if got := registeredWorktrees(t, app); len(got) != 1 {
		t.Errorf("the first repository lists %q, want only its own worktree", got)
	}
	for repo, branch := range map[string]string{app: "task/MUL-3/app", web: "task/MUL-3/web"} {
		if _, err := gitTry(repo, "rev-parse", "--verify", "--quiet", branch); err != nil {
			t.Errorf("%s lost its task branch %s", repo, branch)
		}
	}
}

// One tree goes on request. The task directory goes with the last one, and
// every task branch stays.
func TestDeleteTreeRemovesOneTreeAndKeepsTheOthers(t *testing.T) {
	t.Parallel()
	app := newRepo(t, "app")
	gitRun(t, app, "branch", "dev")
	web := newRepo(t, "web")
	layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-3", []Spec{
		{Repo: app, BaseBranch: "main"},
		{Repo: app, BaseBranch: "dev"},
		{Repo: web, BaseBranch: "main"},
	}, testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	result, err := DeleteTree(layout.Dir, "web", testLogger())
	if err != nil {
		t.Fatalf("DeleteTree: %v", err)
	}
	if len(result.Trees) != 1 || result.Trees[0].Path != filepath.Join(layout.Dir, "web") {
		t.Fatalf("DeleteTree reported %+v", result.Trees)
	}
	if names := treeNames(t, layout.Dir); !reflect.DeepEqual(names, []string{"app--dev", "app--main"}) {
		t.Errorf("the task directory holds %q, want the two app trees", names)
	}

	if _, err := DeleteTree(layout.Dir, "nope", testLogger()); err == nil {
		t.Error("DeleteTree accepted a tree that is not there")
	} else {
		wantError(t, err, `has no tree named "nope"`)
	}
	if _, err := DeleteTree(layout.Dir, "app--dev", testLogger()); err != nil {
		t.Fatalf("DeleteTree of app--dev: %v", err)
	}
	if names := treeNames(t, layout.Dir); !reflect.DeepEqual(names, []string{"app--main"}) {
		t.Errorf("the task directory holds %q, want app--main only", names)
	}
	if _, err := DeleteTree(layout.Dir, "app--main", testLogger()); err != nil {
		t.Fatalf("DeleteTree of app--main: %v", err)
	}
	if exists(layout.Dir) {
		t.Errorf("the task directory %s was left behind", layout.Dir)
	}
	for _, branch := range []string{"task/MUL-3/app--main", "task/MUL-3/app--dev"} {
		if _, err := gitTry(app, "rev-parse", "--verify", "--quiet", branch); err != nil {
			t.Errorf("the task branch %s did not stay", branch)
		}
	}
	if _, err := gitTry(web, "rev-parse", "--verify", "--quiet", "task/MUL-3/web"); err != nil {
		t.Error("the task branch task/MUL-3/web did not stay")
	}
	if got := registeredWorktrees(t, app); len(got) != 1 {
		t.Errorf("the first repository lists %q, want only its own worktree", got)
	}
}

// A worktree that is not the task's — on another branch, or with a detached
// HEAD — is not touched.
func TestDeleteRefusesATreeThatIsNotTheTasks(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		stray   func(t *testing.T, tree string)
		problem string
	}{
		"on another branch": {
			func(t *testing.T, tree string) { gitRun(t, tree, "checkout", "-q", "-b", "side") },
			"on branch side, not on the task branch task/MUL-1",
		},
		"with a detached HEAD": {
			func(t *testing.T, tree string) { gitRun(t, tree, "checkout", "-q", "--detach") },
			"HEAD is detached, not on the task branch task/MUL-1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newRepo(t, "app")
			layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-1", singleSpec(repo, "main"), testLogger())
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			tc.stray(t, layout.Trees[0].Path)
			writeFile(t, filepath.Join(layout.Trees[0].Path, "draft.txt"), "draft\n")

			result, err := Delete(layout.Dir, testLogger())
			wantError(t, err, tc.problem, "preserved at")
			if !result.Trees[0].Kept {
				t.Errorf("Delete reported %+v", result.Trees[0])
			}
			if !exists(filepath.Join(layout.Trees[0].Path, "draft.txt")) {
				t.Error("the tree's uncommitted work is gone")
			}
			if got := registeredWorktrees(t, repo); len(got) != 2 {
				t.Errorf("the repository lists %q, want its own worktree and the tree", got)
			}
		})
	}
}

// A task directory that holds no tree goes when it is empty and stays when it
// holds something else; a missing one is an error.
func TestDeleteOfATaskDirectoryWithoutTrees(t *testing.T) {
	t.Parallel()
	empty := filepath.Join(t.TempDir(), "MUL-1")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Delete(empty, testLogger())
	if err != nil || len(result.Trees) != 0 || exists(empty) {
		t.Errorf("Delete of an empty task directory: %+v, %v, exists %v", result, err, exists(empty))
	}

	held := filepath.Join(t.TempDir(), "MUL-2", "notes")
	writeFile(t, held, "not a tree\n")
	result, err = Delete(filepath.Dir(held), testLogger())
	if err != nil || len(result.Trees) != 0 {
		t.Fatalf("Delete of a task directory without trees: %+v, %v", result, err)
	}
	if !exists(held) {
		t.Error("Delete removed something that is not a tree")
	}

	missing := filepath.Join(t.TempDir(), "MUL-3")
	result, err = Delete(missing, testLogger())
	if result != nil || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Delete of a missing task directory: %+v, %v", result, err)
	}
	if _, err := Delete("relative/MUL-3", testLogger()); err == nil {
		t.Error("Delete accepted a relative path")
	} else {
		wantError(t, err, "absolute")
	}
}

// With the base branch gone there is no telling a commit of the task's own
// from one it started on. Everything is committed, the side that keeps the
// work on the branch.
func TestDeleteCommitsEverythingWhenTheBaseBranchIsGone(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, "app")
	gitRun(t, repo, "branch", "dev")
	layout, err := Create(filepath.Join(t.TempDir(), "root"), "MUL-7", singleSpec(repo, "dev"), testLogger())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tree := layout.Trees[0]
	commitFile(t, tree.Path, "work.txt", "done\n", "work on the task")
	work := treeHEAD(t, tree)
	writeFile(t, filepath.Join(tree.Path, "draft.txt"), "draft\n")
	// dev is not checked out anywhere, so it can go.
	gitRun(t, repo, "branch", "-D", "dev")

	if status, err := Inspect(layout.Dir); err != nil {
		t.Fatalf("Inspect: %v", err)
	} else if got := status.Trees[0]; got.Ahead != -1 || got.Behind != -1 || got.BaseBranch != "dev" {
		t.Fatalf("Inspect reports %+v, want ahead -1 behind -1 on base dev", got)
	}

	result, err := Delete(layout.Dir, testLogger())
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if out := result.Trees[0]; !out.AutoCommitted || out.LeftoverRef != "" || out.Kept {
		t.Errorf("Delete reported %+v", out)
	}
	if got := gitRun(t, repo, "rev-parse", "task/MUL-7^"); got != work {
		t.Errorf("the commit's parent is %s, want the task's commit %s", got, work)
	}
	if got := gitRun(t, repo, "show", "task/MUL-7:draft.txt"); got != "draft" {
		t.Errorf("the commit's draft.txt is %q", got)
	}
}
