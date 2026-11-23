package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// newTaskTreeFixture turns the flag on and gives the test user a daemon, an
// agent and two projects.
func newTaskTreeFixture(t *testing.T) projectScopeFixture {
	t.Helper()
	withFeatureFlag(t, testHandler, featureflags.TaskSessionsV1, true)
	return newProjectScopeFixture(t)
}

// taskTreeLocalDirectory gives a project a folder on one daemon's machine.
func taskTreeLocalDirectory(t *testing.T, workspaceID, projectID, daemonID, path string) {
	t.Helper()
	id := dbfx.Insert(t, "project_resource", testutil.Cols{
		"workspace_id": workspaceID, "project_id": projectID, "resource_type": "local_directory",
		"resource_ref": testutil.Raw(fmt.Sprintf(`'{"local_path":%q,"daemon_id":%q}'::jsonb`, path, daemonID)),
		"label":        "", "position": 0, "created_by": testUserID,
	})
	dbfx.Cleanup(t, `DELETE FROM project_resource WHERE id = $1`, id)
}

type taskTestTree struct{ issueID, treeID string }

// reportTaskTreeReady is what the daemon says about a tree it built.
func reportTaskTreeReady(t *testing.T, issueID, treeID, daemonID, path, taskBranch string) {
	t.Helper()
	reportTreeState(t, issueID, treeID, map[string]any{
		"daemon_id": daemonID, "state": "ready", "path": path, "task_branch": taskBranch,
	}).Want(http.StatusOK)
}

// taskTreeInState gives a task of its own one tree of the fixture's project,
// put in state by what the daemon reports.
func taskTreeInState(t *testing.T, f projectScopeFixture, daemonID, state string) taskTestTree {
	t.Helper()
	return taskTreeIn(t, testWorkspaceID, f.projectID, daemonID, state)
}

// taskTreeIn is taskTreeInState in a given workspace and project.
func taskTreeIn(t *testing.T, workspaceID, projectID, daemonID, state string) taskTestTree {
	t.Helper()
	tree := taskTestTree{issueID: dbfx.Issue(t, "Task tree issue "+state, testutil.Cols{"workspace_id": workspaceID})}
	var scope IssueProjectScopeResponse
	testutil.Call(t, testHandler.PutIssueProjectScope, testutil.WithURLParams(
		scopeRequestIn(workspaceID, testUserID, http.MethodPut, "/api/issues/"+tree.issueID+"/project-scope",
			map[string]any{"items": scopeItems([]scopeTestPair{{projectID, "main"}}, "base_branch")}),
		"id", tree.issueID)).Want(http.StatusOK).JSON(&scope)
	tree.treeID = scope.Items[0].Tree.ID
	switch state {
	case projectTreeReady:
		reportTaskTreeReady(t, tree.issueID, tree.treeID, daemonID, "/srv/tasks/"+tree.issueID[:8], "task/HAN-1")
	case projectTreeFailed:
		reportTreeState(t, tree.issueID, tree.treeID, map[string]any{"daemon_id": daemonID, "state": state, "error": "no disk"}).Want(http.StatusOK)
	case projectTreeDeleted:
		reportTaskTreeReady(t, tree.issueID, tree.treeID, daemonID, "/srv/tasks/"+tree.issueID[:8], "task/HAN-1")
		reportTreeState(t, tree.issueID, tree.treeID, map[string]any{"daemon_id": daemonID, "state": state}).Want(http.StatusOK)
	}
	return tree
}

func daemonTreePath(query string) string {
	return "/api/daemon/issues/project-scope/" + query
}

// treeRequest builds a member request that names a tree of a task.
func treeRequest(userID, issueID, treeID, method, suffix string, body any) *http.Request {
	return testutil.WithURLParams(scopeRequest(userID, method, "/api/issues/"+issueID+"/project-scope/"+treeID+suffix, body), "id", issueID, "treeId", treeID)
}

func treeOf(t *testing.T, issueID, treeID string) IssueProjectTree {
	t.Helper()
	for _, item := range getIssueScope(t, issueID).Items {
		if item.Tree != nil && item.Tree.ID == treeID {
			return *item.Tree
		}
	}
	t.Fatalf("tree %s is not one of task %s", treeID, issueID)
	return IssueProjectTree{}
}

func treeDaemon(t *testing.T, treeID string) string {
	t.Helper()
	var stored string
	dbfx.QueryRow(t, `SELECT COALESCE(daemon_id, '') FROM issue_project_scope WHERE id = $1`, treeID).Scan(&stored)
	return stored
}

// taskKeyOf is the key the daemon's layout names the task by: its identifier.
func taskKeyOf(t *testing.T, issueID string) string {
	t.Helper()
	var number int
	dbfx.QueryRow(t, `SELECT number FROM issue WHERE id = $1`, issueID).Scan(&number)
	return fmt.Sprintf("HAN-%d", number)
}

func listTreeWork(t *testing.T, path string) daemonTaskTreeWorkResponse {
	t.Helper()
	var out daemonTaskTreeWorkResponse
	testutil.Call(t, testHandler.DaemonListIssueProjectTreeWork, newOwnerDaemonRequest(http.MethodGet, path, nil)).Want(http.StatusOK).JSON(&out)
	return out
}

// scopeRequestIn is scopeRequest for a workspace other than the suite's.
func scopeRequestIn(workspaceID, userID, method, path string, body any) *http.Request {
	req := scopeRequest(userID, method, path, body)
	req.Header.Set("X-Workspace-ID", workspaceID)
	return req
}

// newTaskTreeWorkspace gives a second workspace for the fixture's daemon to
// act in: the test user owns it, the daemon holds a runtime there, and one of
// its projects has a folder on the daemon's machine.
func newTaskTreeWorkspace(t *testing.T) (workspaceID, projectID string) {
	t.Helper()
	workspaceID = dbfx.Workspace(t, "Task tree workspace", "task-tree-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	dbfx.Runtime(t, "Task tree runtime", testutil.Cols{"workspace_id": workspaceID, "daemon_id": projectScopeDaemonID})
	projectID = dbfx.Project(t, "Task tree project", testutil.Cols{"workspace_id": workspaceID})
	taskTreeLocalDirectory(t, workspaceID, projectID, projectScopeDaemonID, "/srv/repos/task-tree-project")
	return workspaceID, projectID
}

// setTaskTreeRetention stores a workspace's task tree retention.
func setTaskTreeRetention(t *testing.T, workspaceID string, days int) {
	t.Helper()
	dbfx.Exec(t, `UPDATE workspace SET settings = COALESCE(settings, '{}'::jsonb) || jsonb_build_object('task_tree_retention_days', $2::int) WHERE id = $1`,
		workspaceID, days)
}

func maturePath() string {
	return daemonTreePath("mature?daemon_id=" + projectScopeDaemonID)
}

func listMatureTrees(t *testing.T, path string) daemonTaskTreeWorkResponse {
	t.Helper()
	var out daemonTaskTreeWorkResponse
	testutil.Call(t, testHandler.DaemonListIssueProjectMatureTrees, newOwnerDaemonRequest(http.MethodGet, path, nil)).Want(http.StatusOK).JSON(&out)
	return out
}

func completedAt(t *testing.T, issueID string) *string {
	t.Helper()
	var at *string
	dbfx.QueryRow(t, `SELECT completed_at::text FROM issue WHERE id = $1`, issueID).Scan(&at)
	return at
}

// The routes sit next to the scope routes without shadowing them, answer 404
// while the flag is off, and keep machine credentials out of the member
// writes.
func TestTaskTreeRoutesResolveNextToTheScopeRoutes(t *testing.T) {
	r := chi.NewRouter()
	h := &Handler{}
	RegisterProjectScopeRoutes(r, h)
	RegisterTaskTreeRoutes(r, h)
	RegisterDaemonProjectScopeRoutes(r, h)
	RegisterDaemonTaskTreeRoutes(r, h)

	routes := map[string]int{}
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+route]++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		"DELETE /api/issues/{id}/project-scope/{treeId}/tree", "POST /api/issues/{id}/project-scope/{treeId}/rebuild",
		"GET /issues/project-scope/work", "GET /issues/project-scope/mature",
		// The scope routes they sit next to.
		"GET /api/issues/{id}/project-scope", "PUT /api/issues/{id}/project-scope",
		"GET /issues/{issueId}/project-scope", "POST /issues/{issueId}/project-scope/{treeId}/state",
	} {
		if routes[route] != 1 {
			t.Fatalf("%s is mounted %d times, want once", route, routes[route])
		}
	}
	for route := range routes {
		if strings.Contains(route, "/sessions") || strings.Contains(route, "lookup") {
			t.Fatalf("%s is still mounted", route)
		}
	}

	reads := []string{"GET /issues/project-scope/work", "GET /issues/project-scope/mature"}
	writes := []string{
		"DELETE /api/issues/x/project-scope/t/tree", "POST /api/issues/x/project-scope/t/rebuild",
	}
	call := func(route string, machine bool) *httptest.ResponseRecorder {
		method, path, _ := strings.Cut(route, " ")
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if machine {
			req.Header.Set("X-Actor-Source", "task_token")
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	for _, route := range append(reads, writes...) {
		if rec := call(route, false); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"error":"not found"`) {
			t.Fatalf("%s with the flag off got %d %q, want 404 not found", route, rec.Code, rec.Body.String())
		}
	}
	for _, route := range writes {
		if rec := call(route, true); rec.Code != http.StatusForbidden {
			t.Fatalf("%s by a machine credential got %d %q, want 403", route, rec.Code, rec.Body.String())
		}
	}
}

// With the flag off no tree route answers, and nothing changes.
func TestTaskTreeRoutesAnswer404WhileTheFlagIsOff(t *testing.T) {
	f := newProjectScopeFixture(t)
	withFeatureFlag(t, testHandler, featureflags.TaskSessionsV1, false)
	issueID := dbfx.Issue(t, "Task tree flag off issue")
	treeID := putIssueScope(t, issueID, scopeTestPair{f.projectID, "main"}).Items[0].Tree.ID
	calls := map[string]func() *testutil.Response{
		"delete tree": func() *testutil.Response {
			return testutil.Call(t, testHandler.DeleteIssueProjectTree, treeRequest(testUserID, issueID, treeID, http.MethodDelete, "/tree", nil))
		},
		"rebuild tree": func() *testutil.Response {
			return testutil.Call(t, testHandler.RebuildIssueProjectTree, treeRequest(testUserID, issueID, treeID, http.MethodPost, "/rebuild", nil))
		},
		"tree work": func() *testutil.Response {
			return testutil.Call(t, testHandler.DaemonListIssueProjectTreeWork, newOwnerDaemonRequest(http.MethodGet, daemonTreePath("work?daemon_id="+projectScopeDaemonID), nil))
		},
		"mature trees": func() *testutil.Response {
			return testutil.Call(t, testHandler.DaemonListIssueProjectMatureTrees, newOwnerDaemonRequest(http.MethodGet, maturePath(), nil))
		},
	}
	for name, call := range calls {
		if resp := call(); resp.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d %s with the flag off, want 404", name, resp.Code, resp.Text())
		}
	}
	if tree := treeOf(t, issueID, treeID).State; tree != "pending" {
		t.Fatalf("with the flag off the tree became %s", tree)
	}
}

func TestTaskTreeLifecycle(t *testing.T) {
	f := newTaskTreeFixture(t)
	issueID := dbfx.Issue(t, "Task tree lifecycle issue")
	created := putIssueScope(t, issueID, scopeTestPair{f.projectID, "main"}, scopeTestPair{f.otherProjectID, "develop"})
	if len(created.Items) != 2 {
		t.Fatalf("%d trees after the replace, want 2", len(created.Items))
	}
	first, second := created.Items[0].Tree.ID, created.Items[1].Tree.ID
	refusedRebuild := func(treeID, code string) {
		t.Helper()
		resp := testutil.Call(t, testHandler.RebuildIssueProjectTree, treeRequest(testUserID, issueID, treeID, http.MethodPost, "/rebuild", nil))
		if resp.Code != http.StatusConflict || !strings.Contains(resp.Text(), code) {
			t.Fatalf("rebuilding tree %s got %d %s, want 409 %s", treeID, resp.Code, resp.Text(), code)
		}
	}

	// A tree that is already waiting to be built is on its way.
	refusedRebuild(first, codeTreePending)
	// A built tree cannot be rebuilt either: it is deleted first.
	reportTaskTreeReady(t, issueID, first, projectScopeDaemonID, "/srv/tasks/one", "task/HAN-1")
	refusedRebuild(first, codeTreeReady)

	// Deleting a tree keeps its branch, for a rebuild continues it, and loses
	// where it was. Asking again is not an error and changes nothing.
	testutil.Call(t, testHandler.DeleteIssueProjectTree, treeRequest(testUserID, issueID, first, http.MethodDelete, "/tree", nil)).Want(http.StatusNoContent)
	testutil.Call(t, testHandler.DeleteIssueProjectTree, treeRequest(testUserID, issueID, first, http.MethodDelete, "/tree", nil)).Want(http.StatusNoContent)
	if tree := treeOf(t, issueID, first); tree.State != "deleted" || tree.Path != "" || tree.Error != "" || tree.TaskBranch != "task/HAN-1" {
		t.Fatalf("a deleted tree = %+v", tree)
	}
	var rebuilt issueProjectTreeResponse
	testutil.Call(t, testHandler.RebuildIssueProjectTree, treeRequest(testUserID, issueID, first, http.MethodPost, "/rebuild", nil)).
		Want(http.StatusAccepted).JSON(&rebuilt)
	if rebuilt.Tree.ID != first || rebuilt.Tree.State != "pending" || rebuilt.Tree.Path != "" || rebuilt.Tree.Error != "" || rebuilt.Tree.TaskBranch != "task/HAN-1" {
		t.Fatalf("a rebuilt tree = %+v", rebuilt.Tree)
	}

	// A tree the daemon could not build is rebuilt the same way and loses
	// its error.
	reportTreeState(t, issueID, second, map[string]any{"daemon_id": projectScopeDaemonID, "state": "failed", "error": "no space left on device"}).Want(http.StatusOK)
	testutil.Call(t, testHandler.RebuildIssueProjectTree, treeRequest(testUserID, issueID, second, http.MethodPost, "/rebuild", nil)).Want(http.StatusAccepted)
	if tree := treeOf(t, issueID, second); tree.State != "pending" || tree.Error != "" {
		t.Fatalf("a rebuilt failed tree = %+v", tree)
	}

	// A tree is one task's: another task cannot delete or rebuild it, and an
	// id that names no tree is not found.
	other := taskTreeInState(t, f, projectScopeDaemonID, projectTreePending)
	testutil.Call(t, testHandler.DeleteIssueProjectTree, treeRequest(testUserID, other.issueID, second, http.MethodDelete, "/tree", nil)).Want(http.StatusNotFound)
	testutil.Call(t, testHandler.RebuildIssueProjectTree, treeRequest(testUserID, other.issueID, second, http.MethodPost, "/rebuild", nil)).Want(http.StatusNotFound)
	testutil.Call(t, testHandler.DeleteIssueProjectTree, treeRequest(testUserID, issueID, "00000000-0000-0000-0000-00000000dead", http.MethodDelete, "/tree", nil)).Want(http.StatusNotFound)
	testutil.Call(t, testHandler.DeleteIssueProjectTree, treeRequest(testUserID, issueID, "not-a-uuid", http.MethodDelete, "/tree", nil)).Want(http.StatusBadRequest)
	if tree := treeOf(t, issueID, second).State; tree != "pending" {
		t.Fatalf("a refused delete left the tree %s", tree)
	}
}

// The layout on disk depends on how many trees a task has, so a replace
// cannot cross from one tree to several, or back, while the trees exist.
func TestIssueProjectScopeRefusesALayoutChange(t *testing.T) {
	f := newTaskTreeFixture(t)
	issueID := dbfx.Issue(t, "Task layout issue")
	main, develop, release := scopeTestPair{f.projectID, "main"}, scopeTestPair{f.projectID, "develop"}, scopeTestPair{f.projectID, "release"}

	refused := func(pairs ...scopeTestPair) {
		t.Helper()
		resp := testutil.Call(t, testHandler.PutIssueProjectScope, issueScopeRequest(testUserID, issueID, http.MethodPut, "",
			map[string]any{"items": scopeItems(pairs, "base_branch")}))
		if resp.Code != http.StatusConflict || !strings.Contains(resp.Text(), codeTreeLayoutChange) {
			t.Fatalf("replacing with %d pairs got %d %s, want 409 tree_layout_change", len(pairs), resp.Code, resp.Text())
		}
	}

	// With no tree yet the list is free.
	treeID := putIssueScope(t, issueID, main).Items[0].Tree.ID
	// One existing tree: it cannot gain a neighbour, and the list cannot
	// drop to none.
	refused(main, develop)
	refused()
	if tree := treeOf(t, issueID, treeID).State; tree != "pending" {
		t.Fatalf("a refused replace left the tree %s", tree)
	}
	// Once the tree is deleted the list is free again. A pair the list keeps
	// keeps its deleted tree, so two live trees come from a list without it.
	testutil.Call(t, testHandler.DeleteIssueProjectTree, treeRequest(testUserID, issueID, treeID, http.MethodDelete, "/tree", nil)).Want(http.StatusNoContent)
	two := putIssueScope(t, issueID, develop, release)
	if len(two.Items) != 2 || two.Items[0].Tree.State != "pending" || two.Items[1].Tree.State != "pending" {
		t.Fatalf("after the only tree was deleted the scope = %+v, want two pending trees", two.Items)
	}
	// Several trees may gain or lose neighbours, but not become one.
	putIssueScope(t, issueID, main, develop, release)
	refused(main)
	putIssueScope(t, issueID, develop, release)
	putIssueScope(t, issueID)
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_project_scope WHERE issue_id = $1`, issueID); n != 0 {
		t.Fatalf("%d pairs left after removing them all, want 0", n)
	}
	// A tree that is built still cannot be removed by a replace, whichever
	// side of the line its task is on.
	kept := putIssueScope(t, issueID, main, develop)
	reportTaskTreeReady(t, issueID, kept.Items[0].Tree.ID, projectScopeDaemonID, "/srv/tasks/one", "task/HAN-1")
	resp := testutil.Call(t, testHandler.PutIssueProjectScope, issueScopeRequest(testUserID, issueID, http.MethodPut, "",
		map[string]any{"items": scopeItems([]scopeTestPair{develop}, "base_branch")}))
	if resp.Code != http.StatusConflict || strings.Contains(resp.Text(), codeTreeLayoutChange) {
		t.Fatalf("removing a built pair got %d %s, want the ready pair's 409", resp.Code, resp.Text())
	}
}

// The daemon that reports about a tree is the one stored as hosting it.
func TestDaemonReportStoresItsDaemonOnTheTree(t *testing.T) {
	f := newTaskTreeFixture(t)
	tree := taskTreeInState(t, f, projectScopeDaemonID, projectTreePending)
	if stored := treeDaemon(t, tree.treeID); stored != "" {
		t.Fatalf("a tree nobody reported about is hosted by %q", stored)
	}
	reportTreeState(t, tree.issueID, tree.treeID, map[string]any{"daemon_id": projectScopeDaemonID, "state": "failed", "error": "no disk"}).Want(http.StatusOK)
	if stored := treeDaemon(t, tree.treeID); stored != projectScopeDaemonID {
		t.Fatalf("a reported tree is hosted by %q, want the reporting daemon", stored)
	}
	// A token's own daemon is what its report stores.
	tokenTree := taskTreeInState(t, f, projectScopeDaemonID, projectTreePending)
	req := testutil.WithURLParams(newDaemonTokenRequest(http.MethodPost, daemonScopePath(tokenTree.issueID, "")+"/"+tokenTree.treeID+"/state",
		map[string]any{"state": "failed"}, testWorkspaceID, "token-daemon"), "issueId", tokenTree.issueID, "treeId", tokenTree.treeID)
	testutil.Call(t, testHandler.DaemonReportIssueProjectScopeState, req).Want(http.StatusOK)
	if stored := treeDaemon(t, tokenTree.treeID); stored != "token-daemon" {
		t.Fatalf("a tree a token reported about is hosted by %q, want the token's daemon", stored)
	}
}

func TestDaemonTreeWorkListsWhatTheDaemonHasToDo(t *testing.T) {
	f := newTaskTreeFixture(t)
	localPath := "/srv/repos/project-scope-project"
	taskTreeLocalDirectory(t, testWorkspaceID, f.projectID, projectScopeDaemonID, localPath)
	pending := taskTreeInState(t, f, projectScopeDaemonID, projectTreePending)
	removed := taskTreeInState(t, f, projectScopeDaemonID, projectTreeDeleted)
	// Built and failed trees are on disk and are no work.
	taskTreeInState(t, f, projectScopeDaemonID, projectTreeReady)
	taskTreeInState(t, f, projectScopeDaemonID, projectTreeFailed)
	// A tree another daemon holds is not this daemon's work: the one that
	// daemon could not build, once rebuilt, and the one it removed.
	dbfx.Runtime(t, "Task tree holding runtime", testutil.Cols{"daemon_id": "another-daemon"})
	held := taskTreeInState(t, f, "another-daemon", projectTreeFailed)
	testutil.Call(t, testHandler.RebuildIssueProjectTree, treeRequest(testUserID, held.issueID, held.treeID, http.MethodPost, "/rebuild", nil)).Want(http.StatusAccepted)
	taskTreeInState(t, f, "another-daemon", projectTreeDeleted)
	// A project this daemon holds no folder for.
	putIssueScope(t, dbfx.Issue(t, "Task tree issue without a folder"), scopeTestPair{f.otherProjectID, "main"})
	// A project whose folder is on another daemon's machine.
	elsewhere := dbfx.Project(t, "Task tree project elsewhere")
	taskTreeLocalDirectory(t, testWorkspaceID, elsewhere, "another-daemon", "/Users/someone/repos/elsewhere")
	putIssueScope(t, dbfx.Issue(t, "Task tree issue elsewhere"), scopeTestPair{elsewhere, "main"})

	got := listTreeWork(t, daemonTreePath("work?daemon_id="+projectScopeDaemonID))
	byTree := map[string]DaemonTaskTreeWorkItem{}
	for _, item := range got.Items {
		byTree[item.TreeID] = item
	}
	if len(got.Items) != 2 {
		t.Fatalf("the daemon's work = %+v, want only the pending and the deleted tree", got.Items)
	}
	for tree, want := range map[taskTestTree]struct{ action, taskBranch string }{
		pending: {"create", ""}, removed: {"remove", "task/HAN-1"},
	} {
		item := byTree[tree.treeID]
		if item.IssueID != tree.issueID || item.Action != want.action || item.TaskKey != taskKeyOf(t, tree.issueID) || item.TaskBranch != want.taskBranch ||
			item.ProjectID != f.projectID || item.ProjectName != "Project scope project" || item.BaseBranch != "main" || item.LocalPath != localPath {
			t.Fatalf("tree %s = %+v, want task %s to be %s, on %s", tree.treeID, item, tree.issueID, want.action, localPath)
		}
	}

	// Only the member whose daemon it is, or the daemon's own token, reads it.
	anotherOwner := f.member(t, "Task tree another owner", "member")
	testutil.Call(t, testHandler.DaemonListIssueProjectTreeWork, scopeRequest(anotherOwner, http.MethodGet, daemonTreePath("work?daemon_id="+projectScopeDaemonID), nil)).Want(http.StatusNotFound)
	outsider := dbfx.User(t, "Task tree outsider", "task-tree-outsider-"+pending.treeID[:8]+"@example.test")
	testutil.Call(t, testHandler.DaemonListIssueProjectTreeWork, scopeRequest(outsider, http.MethodGet, daemonTreePath("work?daemon_id="+projectScopeDaemonID), nil)).Want(http.StatusNotFound)
	testutil.Call(t, testHandler.DaemonListIssueProjectTreeWork, newOwnerDaemonRequest(http.MethodGet, daemonTreePath("work"), nil)).Want(http.StatusBadRequest)

	dbfx.Runtime(t, "Task tree token runtime", testutil.Cols{"daemon_id": "token-daemon"})
	tokenPath := "/srv/repos/project-scope-project-token"
	taskTreeLocalDirectory(t, testWorkspaceID, f.projectID, "token-daemon", tokenPath)
	token := func(path string) daemonTaskTreeWorkResponse {
		t.Helper()
		var out daemonTaskTreeWorkResponse
		testutil.Call(t, testHandler.DaemonListIssueProjectTreeWork, newDaemonTokenRequest(http.MethodGet, path, nil, testWorkspaceID, "token-daemon")).Want(http.StatusOK).JSON(&out)
		return out
	}
	// The token's daemon has a folder for the project too, so the pending
	// tree is its work as well; it reads the folder of its own machine.
	if got := token(daemonTreePath("work")); len(got.Items) != 1 || got.Items[0].TreeID != pending.treeID || got.Items[0].Action != "create" || got.Items[0].LocalPath != tokenPath {
		t.Fatalf("the token daemon's work = %+v, want the pending tree at its own folder", got.Items)
	}
	testutil.Call(t, testHandler.DaemonListIssueProjectTreeWork, newDaemonTokenRequest(http.MethodGet, daemonTreePath("work?daemon_id="+projectScopeDaemonID), nil, testWorkspaceID, "token-daemon")).
		Want(http.StatusForbidden)
}

// A workspace keeps a completed task's trees for its task_tree_retention_days,
// 5 unless it set them. The daemon sends no number and gets one list for
// every workspace it acts in.
func TestDaemonMatureTreesFollowEachWorkspacesRetention(t *testing.T) {
	f := newTaskTreeFixture(t)
	localPath := "/srv/repos/project-scope-project"
	taskTreeLocalDirectory(t, testWorkspaceID, f.projectID, projectScopeDaemonID, localPath)
	otherWorkspace, otherProject := newTaskTreeWorkspace(t)
	// complete closes the task, which stamps when, then moves that moment
	// back by days.
	complete := func(tree taskTestTree, days int) {
		t.Helper()
		dbfx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, tree.issueID)
		dbfx.Exec(t, `UPDATE issue SET completed_at = now() - make_interval(days => $2) WHERE id = $1`, tree.issueID, days)
	}
	old := taskTreeInState(t, f, projectScopeDaemonID, projectTreeReady)
	complete(old, 10)
	fresh := taskTreeInState(t, f, projectScopeDaemonID, projectTreeReady)
	complete(fresh, 3)
	taskTreeInState(t, f, projectScopeDaemonID, projectTreeReady)
	// A tree that is not on disk is not removed, however old its task is.
	gone := taskTreeInState(t, f, projectScopeDaemonID, projectTreeDeleted)
	complete(gone, 10)
	// The same daemon holds a tree in a workspace that keeps them for 30 days.
	kept := taskTreeIn(t, otherWorkspace, otherProject, projectScopeDaemonID, projectTreeReady)
	complete(kept, 10)
	setTaskTreeRetention(t, otherWorkspace, 30)

	mature := func(path string) []string {
		t.Helper()
		got := listMatureTrees(t, path)
		ids := make([]string, 0, len(got.Items))
		for _, item := range got.Items {
			ids = append(ids, item.TreeID)
		}
		return ids
	}
	if ids := mature(maturePath()); len(ids) != 1 || ids[0] != old.treeID {
		t.Fatalf("trees of tasks past their workspace's retention = %v, want only the one completed 10 days ago in the workspace that keeps trees for 5 days", ids)
	}
	if item := listMatureTrees(t, maturePath()).Items[0]; item.Action != "remove" || item.LocalPath != localPath || item.TaskKey != taskKeyOf(t, old.issueID) {
		t.Fatalf("a mature tree = %+v", item)
	}
	// A task that is back to work keeps its tree.
	dbfx.Exec(t, `UPDATE issue SET status = 'backlog' WHERE id = $1`, old.issueID)
	if ids := mature(maturePath()); len(ids) != 0 {
		t.Fatalf("trees of tasks past their workspace's retention after a reopen = %v, want none", ids)
	}
	complete(old, 10)
	// A closed task that was never stamped, as one closed before the column
	// existed, is dated by its next status write, and keeps its tree for now.
	dbfx.Exec(t, `UPDATE issue SET completed_at = NULL WHERE id = $1`, fresh.issueID)
	if ids := mature(maturePath()); len(ids) != 1 || ids[0] != old.treeID {
		t.Fatalf("trees of tasks past their workspace's retention, one of them undated = %v, want only the old one", ids)
	}

	// Kept for 2 days now, the other workspace's 10-day-old tree is mature
	// as well: the list reaches every tree the shortest retention runs out
	// of.
	setTaskTreeRetention(t, otherWorkspace, 2)
	both := func(ids []string) bool {
		return len(ids) == 2 && (ids[0] == old.treeID || ids[1] == old.treeID) && (ids[0] == kept.treeID || ids[1] == kept.treeID)
	}
	if ids := mature(maturePath()); !both(ids) {
		t.Fatalf("trees of tasks past their workspace's retention = %v, want the old tree and the other workspace's 10-day-old one", ids)
	}
	// A tree is mature by its own workspace's retention alone: closed 3 days
	// ago, the other workspace's is past its 2 days while the first
	// workspace's is not past its 5.
	complete(fresh, 3)
	complete(kept, 3)
	if ids := mature(maturePath()); !both(ids) {
		t.Fatalf("trees of tasks past their workspace's retention = %v, want the old tree and the other workspace's 3-day-old one, not the first workspace's", ids)
	}
	// No request carries the number: a days query changes nothing.
	if withDays, plain := mature(maturePath()+"&days=30"), mature(maturePath()); !slices.Equal(withDays, plain) {
		t.Fatalf("trees listed with a days query = %v, want the %v the workspaces' retention decides", withDays, plain)
	}
}

// task_tree_retention_days reads as the default unless it is a whole number
// of days from 1 to the most a settings write stores.
func TestTaskTreeRetentionDays(t *testing.T) {
	for _, tc := range []struct{ settings, want string }{
		{settings: "", want: "5"},
		{settings: `{}`, want: "5"},
		{settings: `{"github_enabled": false}`, want: "5"},
		{settings: `{"task_tree_retention_days": 30}`, want: "30"},
		{settings: `{"task_tree_retention_days": 1}`, want: "1"},
		{settings: `{"task_tree_retention_days": 36500}`, want: "36500"},
		{settings: `{"task_tree_retention_days": null}`, want: "5"},
		{settings: `{"task_tree_retention_days": 0}`, want: "5"},
		{settings: `{"task_tree_retention_days": 2.5}`, want: "5"},
		{settings: `{"task_tree_retention_days": "30"}`, want: "5"},
		{settings: `{"task_tree_retention_days": 36501}`, want: "5"},
		{settings: `not json`, want: "5"},
	} {
		if got := strconv.Itoa(taskTreeRetentionDays([]byte(tc.settings))); got != tc.want {
			t.Errorf("settings %s: retention = %s days, want %s", tc.settings, got, tc.want)
		}
	}
}

// A settings write carries a task tree retention only as a whole number of
// days from 1 to the most the mature list can compare against, as a JSON
// body decodes it.
func TestCheckTaskTreeRetention(t *testing.T) {
	for _, tc := range []struct {
		value any
		ok    bool
	}{
		{value: nil},
		{value: 1.0, ok: true},
		{value: 30.0, ok: true},
		{value: 36500.0, ok: true},
		{value: 0.0},
		{value: -3.0},
		{value: 2.5},
		{value: 36501.0},
		{value: "7"},
		{value: true},
	} {
		err := checkTaskTreeRetention(map[string]any{"task_tree_retention_days": tc.value})
		if tc.ok {
			if err != nil {
				t.Errorf("retention %v should be stored, got %v", tc.value, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), taskTreeRetentionKey) {
			t.Errorf("retention %v should be refused naming the setting, got %v", tc.value, err)
		}
	}
	if err := checkTaskTreeRetention(map[string]any{"github_enabled": false}); err != nil {
		t.Errorf("settings without a retention should be stored, got %v", err)
	}
	if err := checkTaskTreeRetention(map[string]any{}); err != nil {
		t.Errorf("empty settings should be stored, got %v", err)
	}
}

// UpdateWorkspace refuses a task tree retention it cannot store and leaves
// the settings as they were; a whole number of days is stored with the rest.
func TestUpdateWorkspaceChecksTheTaskTreeRetention(t *testing.T) {
	wsID := dbfx.Workspace(t, "Task tree retention workspace", "task-tree-retention-"+uuid.NewString())
	dbfx.Member(t, wsID, testUserID, "owner")
	stored := func() string {
		t.Helper()
		var settings string
		dbfx.QueryRow(t, `SELECT COALESCE(settings::text, '') FROM workspace WHERE id = $1`, wsID).Scan(&settings)
		return settings
	}
	patch := func(settings map[string]any) *testutil.Response {
		t.Helper()
		return testutil.Call(t, testHandler.UpdateWorkspace, withURLParam(
			newRequest(http.MethodPatch, "/api/workspaces/"+wsID, map[string]any{"settings": settings}), "id", wsID))
	}

	before := stored()
	resp := patch(map[string]any{"task_tree_retention_days": 0})
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Text(), taskTreeRetentionKey) {
		t.Fatalf("a retention of 0 got %d %s, want 400 naming the setting", resp.Code, resp.Text())
	}
	if got := stored(); got != before {
		t.Fatalf("a refused retention left the settings %s, want them as they were: %s", got, before)
	}

	patch(map[string]any{"github_enabled": false, "task_tree_retention_days": 30}).Want(http.StatusOK)
	if got := stored(); got != `{"github_enabled": false, "task_tree_retention_days": 30}` {
		t.Fatalf("the stored settings = %s", got)
	}
}

// Where a task's status enters the done or closed category its completed_at
// is set, and where it moves to another category that is cleared. A trigger
// does it, so the path that wrote the status does not matter.
func TestIssueCompletedAtFollowsItsStatusCategory(t *testing.T) {
	newTaskTreeFixture(t)
	issueID := dbfx.Issue(t, "Task completed at issue")
	if at := completedAt(t, issueID); at != nil {
		t.Fatalf("a new task is already completed at %s", *at)
	}
	shipped := "shipped_" + issueID[:8]
	inReview := "in_review_" + issueID[:8]
	abandoned := "abandoned_" + issueID[:8]
	insertCustomStatus(t, shipped, "done", 9, false)
	insertCustomStatus(t, inReview, "started", 9, false)
	insertCustomStatus(t, abandoned, "closed", 9, false)

	// move sets the status the way a member does, and checks that what a
	// client reads agrees with the row.
	move := func(status string) *string {
		t.Helper()
		var out IssueResponse
		testutil.Call(t, testHandler.UpdateIssue, withURLParam(scopeRequest(testUserID, http.MethodPut, "/api/issues/"+issueID, map[string]any{"status": status}), "id", issueID)).
			Want(http.StatusOK).JSON(&out)
		at := completedAt(t, issueID)
		if (at != nil) != (out.CompletedAt != nil) {
			t.Fatalf("status %q: the row's completed_at is set = %v, the response's = %v", status, at != nil, out.CompletedAt != nil)
		}
		return at
	}
	completed := func(status string, want bool) {
		t.Helper()
		if at := move(status); (at != nil) != want {
			t.Fatalf("status %q left completed_at = %v, want set = %v", status, at, want)
		}
	}
	// Built-in statuses.
	completed("done", true)
	completed("backlog", false)
	completed("cancelled", true)
	completed("todo", false)
	// Custom statuses follow their category.
	completed(shipped, true)
	completed(inReview, false)
	completed(abandoned, true)
	// Writing the same status again, or another field, keeps the moment.
	move(shipped)
	first := completedAt(t, issueID)
	var renamed IssueResponse
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(scopeRequest(testUserID, http.MethodPut, "/api/issues/"+issueID, map[string]any{"title": "Renamed completed task"}), "id", issueID)).
		Want(http.StatusOK).JSON(&renamed)
	if again := completedAt(t, issueID); renamed.CompletedAt == nil || first == nil || again == nil || *again != *first {
		t.Fatalf("renaming a completed task moved its completed_at from %v to %v (the response says %v)", first, again, renamed.CompletedAt)
	}

	// The bulk path stamps too, and so does a write that skips every handler.
	batch := dbfx.Issue(t, "Task completed at batch issue")
	testutil.Call(t, testHandler.BatchUpdateIssues, scopeRequest(testUserID, http.MethodPost, "/api/issues/batch-update",
		map[string]any{"issue_ids": []string{batch}, "updates": map[string]any{"status": "done"}})).Want(http.StatusOK)
	if at := completedAt(t, batch); at == nil {
		t.Fatalf("a task closed by a bulk update has no completed_at")
	}
	raw := dbfx.Issue(t, "Task completed at raw issue")
	dbfx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, raw)
	if at := completedAt(t, raw); at == nil {
		t.Fatalf("a task closed by a direct status write has no completed_at")
	}
	// A closed task with no completion time, as one closed before the column
	// existed, is dated by its next status write and by nothing else.
	dbfx.Exec(t, `UPDATE issue SET completed_at = NULL WHERE id = $1`, raw)
	dbfx.Exec(t, `UPDATE issue SET title = 'Renamed by hand' WHERE id = $1`, raw)
	if at := completedAt(t, raw); at != nil {
		t.Fatalf("a write that leaves the status alone stamped completed_at")
	}
	dbfx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, raw)
	if at := completedAt(t, raw); at == nil {
		t.Fatalf("a closed task with no completion time was not dated by its next status write")
	}
}

// Deleting a task takes its own pairs, built or not. They have no foreign key
// to it, so the delete statement sweeps them in the task's own transaction.
// Nothing happens on disk: the daemon finds the tree by its next work listing.
func TestDeletingAnIssueTakesItsTrees(t *testing.T) {
	f := newTaskTreeFixture(t)
	gone := dbfx.Issue(t, "Task trees deleted with their task")
	kept := dbfx.Issue(t, "Task trees kept with their task")
	built := putIssueScope(t, gone, scopeTestPair{f.projectID, "main"}, scopeTestPair{f.otherProjectID, "develop"}).Items[0].Tree.ID
	reportTaskTreeReady(t, gone, built, projectScopeDaemonID, "/srv/tasks/"+gone[:8], "task/HAN-1")
	putIssueScope(t, kept, scopeTestPair{f.projectID, "main"})
	pairs := func(issueID string) int {
		t.Helper()
		return dbfx.Count(t, `SELECT count(*) FROM issue_project_scope WHERE issue_id = $1`, issueID)
	}
	if n := pairs(gone); n != 2 {
		t.Fatalf("%d pairs before the delete, want 2", n)
	}

	testutil.Call(t, testHandler.DeleteIssue, withURLParam(scopeRequest(testUserID, http.MethodDelete, "/api/issues/"+gone, nil), "id", gone)).
		Want(http.StatusNoContent)
	if n := pairs(gone); n != 0 {
		t.Fatalf("%d pairs outlived their task, want none", n)
	}
	if n := pairs(kept); n != 1 {
		t.Fatalf("the other task kept %d pairs, want 1", n)
	}
}
