package treeworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// This machine's global git config breaks git cleanup in tests; every test
// here runs git with an empty one, as the tasktree tests do.
func TestMain(m *testing.M) {
	config, err := os.CreateTemp("", "treeworker-gitconfig-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create an empty global git config:", err)
		os.Exit(1)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", config.Name())
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	code := m.Run()
	config.Close()
	os.Remove(config.Name())
	os.Exit(code)
}

type sentReport struct {
	issueID string
	treeID  string
	report  TreeReport
}

// fakeAPI is the server as the worker sees it. Its work list follows the tree
// states, and a state report moves a tree's state, as the server's does.
type fakeAPI struct {
	mu        sync.Mutex
	scopes    map[string]*IssueScope
	keys      map[string]string // issue id -> task key
	work      []WorkItem        // when set, the work list answers this as it is
	workErr   error
	mature    []WorkItem
	matureErr error
	reportErr error
	reports   []sentReport
	calls     map[string]int
}

func (f *fakeAPI) Work(ctx context.Context) ([]WorkItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["work"]++
	if f.workErr != nil {
		return nil, f.workErr
	}
	if f.work != nil {
		return f.work, nil
	}
	var items []WorkItem
	for _, issueID := range sortedKeys(f.scopes) {
		for _, pair := range f.scopes[issueID].Items {
			if pair.Tree == nil {
				continue
			}
			item := WorkItem{
				TreeID: pair.Tree.ID, IssueID: issueID, TaskKey: f.keys[issueID],
				ProjectID: pair.ProjectID, ProjectName: pair.ProjectID, BaseBranch: pair.BaseBranch,
				TaskBranch: pair.Tree.TaskBranch, LocalPath: pair.LocalPath,
			}
			switch pair.Tree.State {
			case StatePending:
				item.Action = ActionCreate
			case StateDeleted:
				item.Action = ActionRemove
			default:
				continue
			}
			items = append(items, item)
		}
	}
	return items, nil
}

func (f *fakeAPI) Mature(ctx context.Context) ([]WorkItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["mature"]++
	return f.mature, f.matureErr
}

func (f *fakeAPI) Scope(ctx context.Context, issueID string) (*IssueScope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["scope"]++
	scope := f.scopes[issueID]
	if scope == nil {
		return nil, fmt.Errorf("%w: issue %s", ErrNotFound, issueID)
	}
	return scope, nil
}

func (f *fakeAPI) ReportState(ctx context.Context, issueID, treeID string, report TreeReport) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["report"]++
	if f.reportErr != nil {
		return f.reportErr
	}
	f.reports = append(f.reports, sentReport{issueID, treeID, report})
	for _, pair := range f.scopes[issueID].Items {
		if pair.Tree != nil && pair.Tree.ID == treeID {
			pair.Tree.State = report.State
			pair.Tree.Error = report.Error
		}
	}
	return nil
}

func (f *fakeAPI) reported() []sentReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentReport(nil), f.reports...)
}

func (f *fakeAPI) callCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func sortedKeys(m map[string]*IssueScope) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// pairSpec describes one pair of a task: its project's branch, the local
// folder of the project here, and the tree the server keeps for the pair.
type pairSpec struct {
	branch string
	repo   string // "" when the project has no local folder here
	tree   string // "" when the pair has no tree
	state  string
}

type fixture struct {
	t      *testing.T
	ctx    context.Context
	base   string
	root   string
	api    *fakeAPI
	worker *Worker
	logs   *bytes.Buffer
}

func newFixture(t *testing.T, configure func(*Config)) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not available: %v", err)
	}
	f := &fixture{
		t: t, ctx: context.Background(), base: t.TempDir(), logs: &bytes.Buffer{},
		api: &fakeAPI{
			scopes: map[string]*IssueScope{}, keys: map[string]string{}, calls: map[string]int{},
		},
	}
	f.root = filepath.Join(f.base, "tasks")
	cfg := Config{Root: f.root}
	if configure != nil {
		configure(&cfg)
	}
	worker, err := New(f.api, cfg, slog.New(slog.NewTextHandler(f.logs, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.worker = worker
	return f
}

func (f *fixture) tick() bool {
	f.t.Helper()
	return f.worker.tick(f.ctx)
}

// newRepo makes a repository with one commit on main and a branch per extra
// name, outside the task root like a user's checkout.
func (f *fixture) newRepo(name string, branches ...string) string {
	f.t.Helper()
	path := filepath.Join(f.base, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		f.t.Fatalf("create %s: %v", path, err)
	}
	gitRun(f.t, path, "init", "-q", "-b", "main")
	gitRun(f.t, path, "config", "user.name", "Test User")
	gitRun(f.t, path, "config", "user.email", "test@test.com")
	gitRun(f.t, path, "config", "gc.auto", "0")
	writeFile(f.t, filepath.Join(path, "README"), name+"\n")
	gitRun(f.t, path, "add", "README")
	gitRun(f.t, path, "commit", "-qm", "initial")
	for _, branch := range branches {
		gitRun(f.t, path, "branch", branch)
	}
	return path
}

// addTask lists a task with the server: its pairs and its key.
func (f *fixture) addTask(issueID, key string, pairs ...pairSpec) {
	f.t.Helper()
	items := make([]Pair, 0, len(pairs))
	for i, p := range pairs {
		item := Pair{BaseBranch: p.branch, Position: int32(i), LocalPath: p.repo}
		if p.tree != "" {
			item.Tree = &Tree{ID: p.tree, State: p.state}
		}
		items = append(items, item)
	}
	f.api.mu.Lock()
	defer f.api.mu.Unlock()
	f.api.scopes[issueID] = &IssueScope{IssueID: issueID, Items: items}
	f.api.keys[issueID] = key
}

// setTree replaces the state of one tree of a listed task.
func (f *fixture) setTree(issueID, treeID, state string) {
	f.t.Helper()
	f.api.mu.Lock()
	defer f.api.mu.Unlock()
	for _, pair := range f.api.scopes[issueID].Items {
		if pair.Tree != nil && pair.Tree.ID == treeID {
			pair.Tree.State = state
		}
	}
}

func (f *fixture) scope(issueID string) *IssueScope {
	f.t.Helper()
	f.api.mu.Lock()
	defer f.api.mu.Unlock()
	return f.api.scopes[issueID]
}

// tree is where the tree named slug of task key lives; a task with one tree
// has no slug.
func (f *fixture) tree(key string, slug ...string) string {
	return filepath.Join(append([]string{f.root, key}, slug...)...)
}

func ready(path, branch string) TreeReport {
	return TreeReport{State: StateReady, Path: path, TaskBranch: branch}
}

// wantReports compares the reports made since the n-th, as a set: tasks are
// worked on in parallel, so reports of different tasks interleave.
func (f *fixture) wantReports(since int, want ...sentReport) {
	f.t.Helper()
	all := f.api.reported()
	got := append([]sentReport(nil), all[min(since, len(all)):]...)
	sortReports(got)
	sortReports(want)
	if !reflect.DeepEqual(got, want) {
		f.t.Fatalf("reports =\n%v\nwant\n%v", got, want)
	}
}

func sortReports(reports []sentReport) {
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].issueID != reports[j].issueID {
			return reports[i].issueID < reports[j].issueID
		}
		return reports[i].treeID < reports[j].treeID
	})
}

func gitTry(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s failed: %s: %v", args, dir, out, err)
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func commitAll(t *testing.T, dir, message string) {
	t.Helper()
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", message)
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s should exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should not exist, stat says %v", path, err)
	}
}

func assertBranch(t *testing.T, dir, branch string) {
	t.Helper()
	if got := gitRun(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != branch {
		t.Fatalf("%s is on branch %q, want %q", dir, got, branch)
	}
}

func assertClean(t *testing.T, dir string) {
	t.Helper()
	if got := gitRun(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("%s is not clean:\n%s", dir, got)
	}
}

func assertWorktreeOf(t *testing.T, repo, path string) {
	t.Helper()
	if list := gitRun(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(list+"\n", "worktree "+path+"\n") {
		t.Fatalf("%s is not a worktree of %s:\n%s", path, repo, list)
	}
}

func TestTickCreatesOneTree(t *testing.T) {
	f := newFixture(t, nil)
	repo := f.newRepo("app")
	f.addTask("issue-1", "T-1", pairSpec{branch: "main", repo: repo, tree: "t1", state: StatePending})

	if !f.tick() {
		t.Fatal("a tick that built a tree should be followed by the steady interval")
	}

	tree := f.tree("T-1")
	mustExist(t, filepath.Join(tree, "README"))
	assertBranch(t, tree, "task/T-1")
	assertWorktreeOf(t, repo, tree)
	// The tree is a worktree of the user's repository, which stays as it was.
	assertBranch(t, repo, "main")
	assertClean(t, repo)
	f.wantReports(0, sentReport{"issue-1", "t1", ready(tree, "task/T-1")})

	if f.tick() {
		t.Fatal("a tick with nothing left to do should be followed by the slow interval")
	}
	f.wantReports(1)
}

func TestTickLeavesOutAPairWithoutALocalFolder(t *testing.T) {
	f := newFixture(t, nil)
	repo := f.newRepo("app")
	f.addTask("issue-1", "T-1",
		pairSpec{branch: "main", repo: repo, tree: "t1", state: StatePending},
		pairSpec{branch: "main", tree: "t2", state: StatePending},
	)

	f.tick()

	// Only a pair with a folder here takes part in the layout, so the task has one tree.
	tree := f.tree("T-1")
	mustExist(t, filepath.Join(tree, "README"))
	assertBranch(t, tree, "task/T-1")
	// The tree that cannot be built here says why.
	f.wantReports(0,
		sentReport{"issue-1", "t1", ready(tree, "task/T-1")},
		sentReport{"issue-1", "t2", TreeReport{State: StateFailed, Error: "the project has no local folder on this machine"}},
	)
}

func TestTickCreatesSeveralTreesOfOneTask(t *testing.T) {
	f := newFixture(t, nil)
	app := f.newRepo("app", "dev")
	web := f.newRepo("web")
	f.addTask("issue-1", "T-1",
		pairSpec{branch: "main", repo: app, tree: "t1", state: StatePending},
		pairSpec{branch: "dev", repo: app, tree: "t2", state: StatePending},
		pairSpec{branch: "main", repo: web, tree: "t3", state: StatePending},
	)

	if !f.tick() {
		t.Fatal("a tick that built trees should be followed by the steady interval")
	}

	// The same repository twice is told apart by its branch in the slug.
	for slug, branch := range map[string]string{
		"app--main": "task/T-1/app--main",
		"app--dev":  "task/T-1/app--dev",
		"web":       "task/T-1/web",
	} {
		mustExist(t, filepath.Join(f.tree("T-1", slug), "README"))
		assertBranch(t, f.tree("T-1", slug), branch)
	}
	// Each tree starts from its own base branch as it stands locally.
	devHead := gitRun(t, app, "rev-parse", "refs/heads/dev")
	if got := gitRun(t, f.tree("T-1", "app--dev"), "rev-parse", "HEAD"); got != devHead {
		t.Fatalf("the dev tree is at %s, want the tip of dev %s", got, devHead)
	}
	assertClean(t, app)
	assertClean(t, web)
	f.wantReports(0,
		sentReport{"issue-1", "t1", ready(f.tree("T-1", "app--main"), "task/T-1/app--main")},
		sentReport{"issue-1", "t2", ready(f.tree("T-1", "app--dev"), "task/T-1/app--dev")},
		sentReport{"issue-1", "t3", ready(f.tree("T-1", "web"), "task/T-1/web")},
	)
}

func TestTickAddsAPairToAnExistingTask(t *testing.T) {
	f := newFixture(t, nil)
	app := f.newRepo("app")
	web := f.newRepo("web")
	api := f.newRepo("api")
	f.addTask("issue-1", "T-1",
		pairSpec{branch: "main", repo: app, tree: "t1", state: StatePending},
		pairSpec{branch: "main", repo: web, tree: "t2", state: StatePending},
	)
	f.tick()
	// Work committed in a tree of the task.
	writeFile(t, filepath.Join(f.tree("T-1", "app"), "work.txt"), "draft\n")
	commitAll(t, f.tree("T-1", "app"), "work")

	// The task gains a third pair.
	f.addTask("issue-1", "T-1",
		pairSpec{branch: "main", repo: app, tree: "t1", state: StateReady},
		pairSpec{branch: "main", repo: web, tree: "t2", state: StateReady},
		pairSpec{branch: "main", repo: api, tree: "t3", state: StatePending},
	)
	if !f.tick() {
		t.Fatal("a tick that built a tree should be followed by the steady interval")
	}

	// The two trees that existed are kept as they were, work included; only the new one is reported.
	assertBranch(t, f.tree("T-1", "app"), "task/T-1/app")
	if got := gitRun(t, f.tree("T-1", "app"), "log", "-1", "--format=%s"); got != "work" {
		t.Fatalf("the app tree is at %q, want the commit made in it", got)
	}
	mustExist(t, filepath.Join(f.tree("T-1", "app"), "work.txt"))
	mustExist(t, filepath.Join(f.tree("T-1", "api"), "README"))
	assertBranch(t, f.tree("T-1", "api"), "task/T-1/api")
	f.wantReports(2, sentReport{"issue-1", "t3", ready(f.tree("T-1", "api"), "task/T-1/api")})
}

func TestTickRemovesOneTreeOfATask(t *testing.T) {
	f := newFixture(t, nil)
	app := f.newRepo("app")
	web := f.newRepo("web")
	f.addTask("issue-1", "T-1",
		pairSpec{branch: "main", repo: app, tree: "t1", state: StatePending},
		pairSpec{branch: "main", repo: web, tree: "t2", state: StatePending},
	)
	f.tick()

	f.setTree("issue-1", "t2", StateDeleted)
	if !f.tick() {
		t.Fatal("a tick that removed a tree should be followed by the steady interval")
	}

	mustNotExist(t, f.tree("T-1", "web"))
	// The task's other tree is not touched.
	mustExist(t, filepath.Join(f.tree("T-1", "app"), "README"))
	assertBranch(t, f.tree("T-1", "app"), "task/T-1/app")
	// The task branch outlives the tree.
	if _, err := gitTry(web, "rev-parse", "--verify", "refs/heads/task/T-1/web"); err != nil {
		t.Fatalf("the task branch should have stayed in the repository: %v", err)
	}
	f.wantReports(2, sentReport{"issue-1", "t2", TreeReport{State: StateDeleted}})

	// Listing it again finds nothing to remove and reports nothing.
	if f.tick() {
		t.Fatal("nothing was left to remove")
	}
	f.wantReports(3)
}

func TestTickRemovesATreeByItsBranchOnceItsPairIsGone(t *testing.T) {
	f := newFixture(t, nil)
	app := f.newRepo("app")
	web := f.newRepo("web")
	f.addTask("issue-1", "T-1",
		pairSpec{branch: "main", repo: app, tree: "t1", state: StatePending},
		pairSpec{branch: "main", repo: web, tree: "t2", state: StatePending},
	)
	f.tick()
	// The task keeps one pair, which alone would be a single-tree task, while
	// the tree of the pair that left still has to go.
	f.addTask("issue-1", "T-1", pairSpec{branch: "main", repo: web, tree: "t2", state: StateReady})
	f.api.mu.Lock()
	f.api.work = []WorkItem{{
		TreeID: "t1", IssueID: "issue-1", TaskKey: "T-1", Action: ActionRemove,
		TaskBranch: "task/T-1/app", BaseBranch: "main", LocalPath: app,
	}}
	f.api.mu.Unlock()

	if !f.tick() {
		t.Fatal("a tick that removed a tree should be followed by the steady interval")
	}

	// Only the tree the item named by its branch goes; the remaining one is where it was.
	mustNotExist(t, f.tree("T-1", "app"))
	assertBranch(t, f.tree("T-1", "web"), "task/T-1/web")
	f.wantReports(2, sentReport{"issue-1", "t1", TreeReport{State: StateDeleted}})
}

func TestTickRemovesASingleTreeTaskAndKeepsItsWork(t *testing.T) {
	f := newFixture(t, nil)
	repo := f.newRepo("app")
	f.addTask("issue-1", "T-1", pairSpec{branch: "main", repo: repo, tree: "t1", state: StatePending})
	f.tick()
	writeFile(t, filepath.Join(f.tree("T-1"), "draft.txt"), "not committed yet\n")

	f.setTree("issue-1", "t1", StateDeleted)
	if !f.tick() {
		t.Fatal("a tick that removed a tree should be followed by the steady interval")
	}

	mustNotExist(t, f.tree("T-1"))
	f.wantReports(1, sentReport{"issue-1", "t1", TreeReport{State: StateDeleted}})
	// What was left uncommitted went onto the task branch, which stays.
	if got, err := gitTry(repo, "show", "refs/heads/task/T-1:draft.txt"); err != nil || got != "not committed yet" {
		t.Fatalf("the task branch should hold the uncommitted work, got %q, %v", got, err)
	}
	assertBranch(t, repo, "main")
	assertClean(t, repo)
}

func TestTickRebuildsATreeFromItsTaskBranch(t *testing.T) {
	f := newFixture(t, nil)
	repo := f.newRepo("app")
	f.addTask("issue-1", "T-1", pairSpec{branch: "main", repo: repo, tree: "t1", state: StatePending})
	f.tick()
	writeFile(t, filepath.Join(f.tree("T-1"), "work.txt"), "done so far\n")
	commitAll(t, f.tree("T-1"), "work")

	// Someone deletes the directory by hand, and the server asks for the tree again.
	if err := os.RemoveAll(f.tree("T-1")); err != nil {
		t.Fatalf("delete the tree by hand: %v", err)
	}
	f.setTree("issue-1", "t1", StatePending)
	if !f.tick() {
		t.Fatal("a tick that rebuilt a tree should be followed by the steady interval")
	}

	// The tree continues its task branch; the work is not cut from main again.
	assertBranch(t, f.tree("T-1"), "task/T-1")
	mustExist(t, filepath.Join(f.tree("T-1"), "work.txt"))
	assertWorktreeOf(t, repo, f.tree("T-1"))
	f.wantReports(1, sentReport{"issue-1", "t1", ready(f.tree("T-1"), "task/T-1")})
}

func TestTickReportsWhyATreeCouldNotBeBuilt(t *testing.T) {
	f := newFixture(t, nil)
	repo := f.newRepo("app")
	missing := filepath.Join(f.base, "missing-folder")
	f.addTask("issue-branch", "T-1", pairSpec{branch: "nope", repo: repo, tree: "t1", state: StatePending})
	f.addTask("issue-folder", "T-2", pairSpec{branch: "main", repo: missing, tree: "t2", state: StatePending})
	// A task that cannot be built does not stop another.
	f.addTask("issue-good", "T-3", pairSpec{branch: "main", repo: repo, tree: "t3", state: StatePending})

	f.tick()

	reports := f.api.reported()
	states := make(map[string]sentReport, len(reports))
	for _, r := range reports {
		states[r.treeID] = r
	}
	if len(reports) != 3 {
		t.Fatalf("got %d reports, want one per tree: %v", len(reports), reports)
	}
	for treeID, want := range map[string]string{
		"t1": `no local or remote-tracking branch "nope"`,
		"t2": "repository " + missing + " does not exist",
	} {
		got := states[treeID]
		// The reason is short: one line, no stack of wrappers.
		if got.report.State != StateFailed || !strings.Contains(got.report.Error, want) || strings.Contains(got.report.Error, "\n") {
			t.Errorf("tree %s was reported %+v, want failed with %q in one line", treeID, got.report, want)
		}
	}
	mustNotExist(t, f.tree("T-1"))
	mustNotExist(t, f.tree("T-2"))
	assertBranch(t, f.tree("T-3"), "task/T-3")

	// A failed tree leaves the work list, so nothing is retried until the server asks again.
	if f.tick() {
		t.Fatal("the failures were reported; the next tick has nothing to do")
	}
}

func TestTickKeepsATreeWithAnUnresolvedMerge(t *testing.T) {
	f := newFixture(t, nil)
	repo := f.newRepo("app")
	f.addTask("issue-1", "T-1", pairSpec{branch: "main", repo: repo, tree: "t1", state: StatePending})
	f.tick()
	// Both branches change the same line, so merging main into the task branch cannot resolve.
	writeFile(t, filepath.Join(f.tree("T-1"), "README"), "task\n")
	commitAll(t, f.tree("T-1"), "task work")
	writeFile(t, filepath.Join(repo, "README"), "main\n")
	commitAll(t, repo, "main moved")
	if out, err := gitTry(f.tree("T-1"), "merge", "main"); err == nil {
		t.Fatalf("the merge should have conflicted: %s", out)
	}

	f.setTree("issue-1", "t1", StateDeleted)
	f.tick()

	// The tree stays for someone to finish the merge, and the server is not told it is gone.
	mustExist(t, filepath.Join(f.tree("T-1"), "README"))
	if reports := f.api.reported(); len(reports) != 1 {
		t.Fatalf("the removal should not have been reported, got %v", reports[1:])
	}
	if !strings.Contains(f.logs.String(), "was kept") {
		t.Fatalf("the kept tree should be logged:\n%s", f.logs.String())
	}
}

func TestTickGoesByThePairsNotTheStaleList(t *testing.T) {
	f := newFixture(t, nil)
	repo := f.newRepo("app", "dev")
	f.addTask("issue-1", "T-1",
		pairSpec{branch: "main", repo: repo, tree: "t1", state: StateFailed},
		pairSpec{branch: "dev", repo: repo, tree: "t2", state: StateReady},
	)
	// The list is older than the pairs: t1 was given up on, and t2 is wanted again.
	item := func(treeID, action string) WorkItem {
		return WorkItem{TreeID: treeID, IssueID: "issue-1", TaskKey: "T-1", Action: action, LocalPath: repo}
	}
	f.api.mu.Lock()
	f.api.work = []WorkItem{item("t1", ActionCreate), item("t2", ActionRemove)}
	f.api.mu.Unlock()
	defer func() {
		f.api.mu.Lock()
		f.api.work = nil
		f.api.mu.Unlock()
	}()

	if f.tick() {
		t.Fatal("nothing was left to build or remove")
	}

	mustNotExist(t, f.tree("T-1"))
	if reports := f.api.reported(); len(reports) != 0 {
		t.Fatalf("nothing should have been reported, got %v", reports)
	}
}

// The server decides which trees are mature; the worker reads its list on
// every tick and removes what it names.
func TestMatureSweepRemovesCompletedTasksTrees(t *testing.T) {
	f := newFixture(t, nil)
	repo := f.newRepo("app")
	f.addTask("issue-1", "T-1", pairSpec{branch: "main", repo: repo, tree: "t1", state: StatePending})
	f.tick()
	f.api.mu.Lock()
	f.api.mature = []WorkItem{{
		TreeID: "t1", IssueID: "issue-1", TaskKey: "T-1", Action: ActionRemove,
		BaseBranch: "main", LocalPath: repo,
	}}
	f.api.mu.Unlock()

	if !f.tick() {
		t.Fatal("a tick that removed trees should be followed by the steady interval")
	}

	mustNotExist(t, f.tree("T-1"))
	f.wantReports(1, sentReport{"issue-1", "t1", TreeReport{State: StateDeleted}})
	if got := f.api.callCount("mature"); got != 2 {
		t.Fatalf("the mature list was read %d times, want one read on each of the 2 ticks", got)
	}
}

func TestTickTouchesNothingOutsideTheRoot(t *testing.T) {
	f := newFixture(t, nil)
	repo := f.newRepo("app")
	f.addTask("issue-1", "T-1", pairSpec{branch: "main", repo: repo, tree: "t1", state: StatePending})
	f.tick()

	// The task directory is replaced by a link to a worktree somewhere else.
	moved := filepath.Join(f.base, "moved-tree")
	if err := os.Rename(f.tree("T-1"), moved); err != nil {
		t.Fatalf("move the tree out of the root: %v", err)
	}
	if err := os.Symlink(moved, f.tree("T-1")); err != nil {
		t.Fatalf("link the task directory out of the root: %v", err)
	}
	f.setTree("issue-1", "t1", StateDeleted)

	// A task key that tries to name a directory outside the root.
	f.addTask("issue-2", "../../outside", pairSpec{branch: "main", repo: repo, tree: "t2", state: StatePending})
	f.addTask("issue-3", "a/b", pairSpec{branch: "main", repo: repo, tree: "t3", state: StatePending})

	f.tick()

	mustExist(t, filepath.Join(moved, "README"))
	for _, r := range f.api.reported() {
		if r.treeID == "t1" && r.report.State != StateReady {
			t.Fatalf("the tree behind the link should not have been reported %s", r.report.State)
		}
	}
	mustNotExist(t, filepath.Join(f.base, "outside"))
	// A key with a separator names a directory of its own inside the root.
	mustExist(t, filepath.Join(f.tree("a-b"), "README"))
	// The user's repository is as it was.
	assertBranch(t, repo, "main")
	assertClean(t, repo)
}

func TestRunStaysQuietWhileTheServerHasNoTaskTrees(t *testing.T) {
	f := newFixture(t, func(cfg *Config) {
		cfg.InitialDelay = time.Millisecond
		cfg.SteadyInterval = 10 * time.Millisecond
		cfg.SlowInterval = time.Hour
	})
	f.api.mu.Lock()
	f.api.workErr = fmt.Errorf("%w: GET /api/daemon/issues/project-scope/work returned 404", ErrNotFound)
	f.api.mature = []WorkItem{{TreeID: "t9", IssueID: "issue-9", TaskKey: "T-9", Action: ActionRemove}}
	f.api.mu.Unlock()
	ctx, cancel := context.WithCancel(f.ctx)
	var pauses []time.Duration
	f.worker.pause = func(ctx context.Context, d time.Duration) error {
		pauses = append(pauses, d)
		if len(pauses) == 4 {
			cancel()
			return ctx.Err()
		}
		return nil
	}

	f.worker.Run(ctx)

	if want := []time.Duration{time.Millisecond, time.Hour, time.Hour, time.Hour}; !reflect.DeepEqual(pauses, want) {
		t.Fatalf("pauses = %v, want %v", pauses, want)
	}
	for name, want := range map[string]int{"work": 3, "mature": 0, "scope": 0, "report": 0} {
		if got := f.api.callCount(name); got != want {
			t.Errorf("%s was called %d times over three ticks, want %d", name, got, want)
		}
	}
	if n := strings.Count(f.logs.String(), "does not offer task trees"); n != 1 {
		t.Errorf("the missing routes should be logged once, got %d times:\n%s", n, f.logs.String())
	}
}

func TestRunSwitchesBetweenSteadyAndSlowIntervals(t *testing.T) {
	f := newFixture(t, func(cfg *Config) {
		cfg.InitialDelay = time.Second
		cfg.SteadyInterval = 30 * time.Second
		cfg.SlowInterval = 5 * time.Minute
	})
	repo := f.newRepo("app")
	f.api.mu.Lock()
	f.api.workErr = errors.New("server away")
	f.api.mu.Unlock()
	ctx, cancel := context.WithCancel(f.ctx)
	var pauses []time.Duration
	f.worker.pause = func(ctx context.Context, d time.Duration) error {
		pauses = append(pauses, d)
		f.api.mu.Lock()
		switch len(pauses) {
		case 1: // before the first tick: the server is back and lists a tree to build
			f.api.workErr = nil
		case 2: // before the second: the server is away again
			f.api.workErr = errors.New("server away")
		case 3: // before the third: back, with nothing to do
			f.api.workErr = nil
		case 5:
			f.api.mu.Unlock()
			cancel()
			return ctx.Err()
		}
		f.api.mu.Unlock()
		return nil
	}
	f.addTask("issue-1", "T-1", pairSpec{branch: "main", repo: repo, tree: "t1", state: StatePending})

	f.worker.Run(ctx)

	// Work, then a failure, keep the steady interval; a quiet tick goes slow and stays there.
	want := []time.Duration{time.Second, 30 * time.Second, 30 * time.Second, 5 * time.Minute, 5 * time.Minute}
	if !reflect.DeepEqual(pauses, want) {
		t.Fatalf("pauses = %v, want %v", pauses, want)
	}
	assertBranch(t, f.tree("T-1"), "task/T-1")
	f.wantReports(0, sentReport{"issue-1", "t1", ready(f.tree("T-1"), "task/T-1")})
}

func TestEnsureTreeBuildsTheTreeAndReportsIt(t *testing.T) {
	f := newFixture(t, nil)
	app := f.newRepo("app")
	web := f.newRepo("web")
	f.addTask("issue-1", "T-1",
		pairSpec{branch: "main", repo: app, tree: "t1", state: StatePending},
		pairSpec{branch: "main", repo: web, tree: "t2", state: StatePending},
	)

	path, err := f.worker.EnsureTree(f.ctx, "T-1", f.scope("issue-1"), "t2")
	if err != nil {
		t.Fatalf("EnsureTree: %v", err)
	}
	if path != f.tree("T-1", "web") {
		t.Fatalf("EnsureTree returned %q, want %q", path, f.tree("T-1", "web"))
	}
	mustExist(t, filepath.Join(path, "README"))
	assertBranch(t, path, "task/T-1/web")
	// The task's other tree is laid out with it, but only the one asked for is reported.
	assertBranch(t, f.tree("T-1", "app"), "task/T-1/app")
	f.wantReports(0, sentReport{"issue-1", "t2", ready(path, "task/T-1/web")})

	// Asking again changes nothing.
	if _, err := f.worker.EnsureTree(f.ctx, "T-1", f.scope("issue-1"), "t2"); err != nil {
		t.Fatalf("EnsureTree again: %v", err)
	}
	assertBranch(t, path, "task/T-1/web")

	if _, err := f.worker.EnsureTree(f.ctx, "T-1", f.scope("issue-1"), "no-such-tree"); err == nil {
		t.Fatal("a tree that is not one of the task's should be an error")
	}
	if _, err := f.worker.EnsureTree(f.ctx, "T-1", nil, "t2"); err == nil {
		t.Fatal("without the task's pairs should be an error")
	}
	f.wantReports(1, sentReport{"issue-1", "t2", ready(path, "task/T-1/web")})
}

func TestEnsureTreeReportsWhyATreeCouldNotBeBuilt(t *testing.T) {
	f := newFixture(t, nil)
	repo := f.newRepo("app")
	f.addTask("issue-1", "T-1", pairSpec{branch: "nope", repo: repo, tree: "t1", state: StatePending})

	_, err := f.worker.EnsureTree(f.ctx, "T-1", f.scope("issue-1"), "t1")
	if err == nil || !strings.Contains(err.Error(), `branch "nope"`) {
		t.Fatalf("EnsureTree should say why the tree could not be built, got %v", err)
	}
	f.wantReports(0, sentReport{"issue-1", "t1", TreeReport{State: StateFailed, Error: `repository ` + repo + ` has no local or remote-tracking branch "nope"`}})
}

func TestNewRefusesARootInsideACheckout(t *testing.T) {
	f := newFixture(t, nil)
	repo := f.newRepo("app")

	for name, root := range map[string]string{
		"inside a checkout":                           filepath.Join(repo, "trees"),
		"below a missing directory inside a checkout": filepath.Join(repo, "not-there-yet", "trees"),
		"the checkout itself":                         repo,
		"relative":                                    "trees",
		"empty":                                       "",
		"a filesystem root":                           "/",
	} {
		if _, err := New(f.api, Config{Root: root}, nil); err == nil {
			t.Errorf("%s: root %q should have been refused", name, root)
		}
	}
	// A root that does not exist yet is fine, as long as nothing above it is a checkout.
	if _, err := New(f.api, Config{Root: filepath.Join(f.base, "not-there-yet", "trees")}, nil); err != nil {
		t.Errorf("a root below an ordinary directory: %v", err)
	}
}

func TestNewFillsInTheDefaults(t *testing.T) {
	w, err := New(&fakeAPI{}, Config{Root: "/tmp/ayanami-test-root"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := Config{
		Root: "/tmp/ayanami-test-root", SteadyInterval: DefaultSteadyInterval, SlowInterval: DefaultSlowInterval,
		InitialDelay: DefaultInitialDelay, Parallelism: DefaultParallelism,
	}
	if w.cfg != want {
		t.Fatalf("config = %+v, want %+v", w.cfg, want)
	}
	if _, err := New(nil, Config{Root: "/tmp/ayanami-test-root"}, nil); err == nil {
		t.Fatal("a worker without an API should be refused")
	}
}
