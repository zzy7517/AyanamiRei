package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const projectScopeDaemonID = "project-scope-daemon"

type projectScopeFixture struct {
	runtimeID, projectID, otherProjectID string
}

// newProjectScopeFixture gives a runtime on a daemon the test user owns and
// two projects of the test workspace.
func newProjectScopeFixture(t *testing.T) projectScopeFixture {
	t.Helper()
	dbfx.Cleanup(t, `DELETE FROM issue_project_scope WHERE workspace_id = $1`, testWorkspaceID)
	return projectScopeFixture{
		runtimeID:      dbfx.Runtime(t, "Project scope runtime", testutil.Cols{"daemon_id": projectScopeDaemonID}),
		projectID:      dbfx.Project(t, "Project scope project"),
		otherProjectID: dbfx.Project(t, "Project scope other project"),
	}
}

func (f projectScopeFixture) member(t *testing.T, name, role string) string {
	t.Helper()
	userID := dbfx.User(t, name, "project-scope-"+uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, userID, role)
	return userID
}

type scopeTestPair struct{ projectID, branch string }

func scopeItems(pairs []scopeTestPair, branchKey string) []map[string]string {
	items := make([]map[string]string, 0, len(pairs))
	for _, pair := range pairs {
		items = append(items, map[string]string{"project_id": pair.projectID, branchKey: pair.branch})
	}
	return items
}

// scopeRequest builds a workspace member request. body is nil, raw JSON, or a
// value to encode.
func scopeRequest(userID, method, path string, body any) *http.Request {
	var buf bytes.Buffer
	switch body := body.(type) {
	case nil:
	case string:
		buf.WriteString(body)
	default:
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	return req
}

func issueScopeRequest(userID, issueID, method, suffix string, body any) *http.Request {
	return withURLParam(scopeRequest(userID, method, "/api/issues/"+issueID+"/project-scope"+suffix, body), "id", issueID)
}

func putIssueScope(t *testing.T, issueID string, pairs ...scopeTestPair) IssueProjectScopeResponse {
	t.Helper()
	var out IssueProjectScopeResponse
	testutil.Call(t, testHandler.PutIssueProjectScope, issueScopeRequest(testUserID, issueID, http.MethodPut, "",
		map[string]any{"items": scopeItems(pairs, "base_branch")})).Want(http.StatusOK).JSON(&out)
	return out
}

func getIssueScope(t *testing.T, issueID string) IssueProjectScopeResponse {
	t.Helper()
	var out IssueProjectScopeResponse
	testutil.Call(t, testHandler.GetIssueProjectScope, issueScopeRequest(testUserID, issueID, http.MethodGet, "", nil)).
		Want(http.StatusOK).JSON(&out)
	return out
}

func daemonScopePath(issueID, query string) string {
	return "/api/daemon/issues/" + issueID + "/project-scope" + query
}

// getDaemonScope reads a task's scope as the runtime owner's credential does.
func getDaemonScope(t *testing.T, issueID, query string) daemonProjectScopeResponse {
	t.Helper()
	var out daemonProjectScopeResponse
	testutil.Call(t, testHandler.DaemonGetIssueProjectScope,
		withURLParam(newOwnerDaemonRequest(http.MethodGet, daemonScopePath(issueID, query), nil), "issueId", issueID)).
		Want(http.StatusOK).JSON(&out)
	return out
}

func reportTreeState(t *testing.T, issueID, treeID string, body map[string]any) *testutil.Response {
	t.Helper()
	req := testutil.WithURLParams(newOwnerDaemonRequest(http.MethodPost, daemonScopePath(issueID, "")+"/"+treeID+"/state", body),
		"issueId", issueID, "treeId", treeID)
	return testutil.Call(t, testHandler.DaemonReportIssueProjectScopeState, req)
}

func TestProjectScopeHidesProjectsTheCallerCannotSee(t *testing.T) {
	f := newProjectScopeFixture(t)
	privateID := dbfx.Project(t, "Project scope private project", testutil.Cols{"visibility": "private"})
	// A private project is seen by its members only, whatever the workspace role.
	dbfx.InsertNoID(t, "project_member", testutil.Cols{"workspace_id": testWorkspaceID, "project_id": privateID, "user_id": testUserID},
		"project_id = $1 AND user_id = $2", privateID, testUserID)
	issueID := dbfx.Issue(t, "Project scope issue")
	memberID := f.member(t, "Project scope outsider", "member")

	putIssueScope(t, issueID, scopeTestPair{privateID, "main"}, scopeTestPair{f.projectID, "main"})
	memberScope := func(issueID string) IssueProjectScopeResponse {
		t.Helper()
		var got IssueProjectScopeResponse
		testutil.Call(t, testHandler.GetIssueProjectScope, issueScopeRequest(memberID, issueID, http.MethodGet, "", nil)).
			Want(http.StatusOK).JSON(&got)
		return got
	}
	if got := memberScope(issueID); len(got.Items) != 1 || got.Items[0].ProjectID != f.projectID {
		t.Fatalf("a member outside the private project read the task's pairs as %+v, want only %s", got.Items, f.projectID)
	}
	// A task whose only pairs are hidden answers as one without pairs.
	hiddenOnly := dbfx.Issue(t, "Project scope issue in a private project")
	putIssueScope(t, hiddenOnly, scopeTestPair{privateID, "main"})
	if got := memberScope(hiddenOnly); len(got.Items) != 0 {
		t.Fatalf("a member outside the private project read the task's pairs as %+v, want none", got.Items)
	}

	// Writing a project the caller cannot see answers as a missing one.
	resp := testutil.Call(t, testHandler.PutIssueProjectScope, issueScopeRequest(memberID, issueID, http.MethodPut, "",
		map[string]any{"items": scopeItems([]scopeTestPair{{privateID, "main"}}, "base_branch")}))
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Text(), "project not found in this workspace") {
		t.Fatalf("naming a hidden project got %d %s, want 400 project not found in this workspace", resp.Code, resp.Text())
	}
}

func TestIssueProjectScopeReplaceKeepsTreesAndRefusesRemovingAReadyOne(t *testing.T) {
	f := newProjectScopeFixture(t)
	issueID := dbfx.Issue(t, "Project scope issue")

	created := putIssueScope(t, issueID, scopeTestPair{f.projectID, "main"}, scopeTestPair{f.otherProjectID, "main"})
	if len(created.Items) != 2 {
		t.Fatalf("put returned %d pairs, want 2", len(created.Items))
	}
	for i, item := range created.Items {
		if item.Tree == nil || item.Tree.ID == "" || item.Tree.State != "pending" || item.Tree.Path != "" || item.Position != int32(i) {
			t.Fatalf("pair %d = %+v, want a pending tree at position %d", i, item, i)
		}
	}
	readyTree := created.Items[0].Tree.ID
	pendingTree := created.Items[1].Tree.ID

	reportTreeState(t, issueID, readyTree, map[string]any{
		"daemon_id": projectScopeDaemonID, "state": "ready", "path": "/srv/tasks/demo/project-scope-project", "task_branch": "task/HAN-1",
	}).Want(http.StatusOK)

	// A pair the new list keeps keeps its tree, and only its position moves.
	reordered := putIssueScope(t, issueID, scopeTestPair{f.otherProjectID, "main"}, scopeTestPair{f.projectID, "main"})
	if reordered.Items[0].Tree.ID != pendingTree || reordered.Items[0].Position != 0 {
		t.Fatalf("first pair after a reorder = %+v, want the pending tree first", reordered.Items[0])
	}
	if reordered.Items[1].Tree.ID != readyTree || reordered.Items[1].Position != 1 || reordered.Items[1].Tree.State != "ready" || reordered.Items[1].Tree.Path != "/srv/tasks/demo/project-scope-project" {
		t.Fatalf("second pair after a reorder = %+v, want the ready tree last with what the daemon reported", reordered.Items[1])
	}

	// Removing a pair whose tree is ready is refused and changes nothing.
	resp := testutil.Call(t, testHandler.PutIssueProjectScope, issueScopeRequest(testUserID, issueID, http.MethodPut, "",
		map[string]any{"items": scopeItems([]scopeTestPair{{f.otherProjectID, "main"}}, "base_branch")}))
	if resp.Code != http.StatusConflict {
		t.Fatalf("removing a ready pair got %d %s, want 409", resp.Code, resp.Text())
	}
	after := getIssueScope(t, issueID)
	if len(after.Items) != 2 || after.Items[0].Tree.ID != pendingTree || after.Items[1].Tree.ID != readyTree {
		t.Fatalf("a refused replace left the scope as %+v", after.Items)
	}

	// Leaving one of two trees would change the layout on disk, whatever state
	// the other tree is in.
	resp = testutil.Call(t, testHandler.PutIssueProjectScope, issueScopeRequest(testUserID, issueID, http.MethodPut, "",
		map[string]any{"items": scopeItems([]scopeTestPair{{f.projectID, "main"}}, "base_branch")}))
	if resp.Code != http.StatusConflict || !strings.Contains(resp.Text(), codeTreeLayoutChange) {
		t.Fatalf("leaving one of two trees got %d %s, want 409 tree_layout_change", resp.Code, resp.Text())
	}
	// Once the daemon has removed the other tree from disk, its pair goes.
	reportTreeState(t, issueID, pendingTree, map[string]any{"daemon_id": projectScopeDaemonID, "state": "deleted"}).Want(http.StatusOK)
	kept := putIssueScope(t, issueID, scopeTestPair{f.projectID, "main"})
	if len(kept.Items) != 1 || kept.Items[0].Tree.ID != readyTree || kept.Items[0].Position != 0 || kept.Items[0].Tree.State != "ready" {
		t.Fatalf("keeping the ready pair left the scope as %+v", kept.Items)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_project_scope WHERE issue_id = $1`, issueID); n != 1 {
		t.Fatalf("%d pairs left, want 1", n)
	}
}

func TestIssueProjectScopeRejectsInvalidLists(t *testing.T) {
	f := newProjectScopeFixture(t)
	issueID := dbfx.Issue(t, "Project scope issue")
	for name, body := range map[string]string{
		"no items key":                    `{}`,
		"project is not in the workspace": fmt.Sprintf(`{"items":[{"project_id":%q,"base_branch":"main"}]}`, uuid.NewString()),
		"branch is empty":                 fmt.Sprintf(`{"items":[{"project_id":%q,"base_branch":""}]}`, f.projectID),
		"branch is not a git ref":         fmt.Sprintf(`{"items":[{"project_id":%q,"base_branch":"a..b"}]}`, f.projectID),
	} {
		t.Run(name, func(t *testing.T) {
			resp := testutil.Call(t, testHandler.PutIssueProjectScope, issueScopeRequest(testUserID, issueID, http.MethodPut, "", body))
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("got %d %s, want 400", resp.Code, resp.Text())
			}
		})
	}
	testutil.Call(t, testHandler.PutIssueProjectScope, issueScopeRequest(testUserID, uuid.NewString(), http.MethodPut, "",
		map[string]any{"items": []any{}})).Want(http.StatusNotFound)
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_project_scope WHERE issue_id = $1`, issueID); n != 0 {
		t.Fatalf("%d pairs written by rejected requests, want 0", n)
	}
}

func TestDaemonIssueProjectScopeReturnsLocalFoldersOfTheDaemon(t *testing.T) {
	f := newProjectScopeFixture(t)
	localDirectory := func(projectID, daemonID, path string, position int) {
		dbfx.Insert(t, "project_resource", testutil.Cols{
			"workspace_id": testWorkspaceID, "project_id": projectID, "resource_type": "local_directory",
			"resource_ref": testutil.Raw(fmt.Sprintf(`'{"local_path":%q,"daemon_id":%q}'::jsonb`, path, daemonID)),
			"label":        "", "position": position, "created_by": testUserID,
		})
	}
	localDirectory(f.projectID, projectScopeDaemonID, "/srv/repos/project-scope-project", 0)
	// Another machine's folder of the same project is not this daemon's.
	localDirectory(f.projectID, "another-daemon", "/Users/someone/repos/project-scope-project", 1)
	issueID := dbfx.Issue(t, "Project scope issue")
	created := putIssueScope(t, issueID, scopeTestPair{f.projectID, "main"}, scopeTestPair{f.otherProjectID, "develop"})

	got := getDaemonScope(t, issueID, "?daemon_id="+projectScopeDaemonID)
	if got.IssueID != issueID || len(got.Items) != 2 {
		t.Fatalf("daemon scope = %+v, want the task's two pairs", got)
	}
	if item := got.Items[0]; item.LocalPath != "/srv/repos/project-scope-project" || item.Tree == nil || item.Tree.ID != created.Items[0].Tree.ID {
		t.Fatalf("first pair = %+v, want this daemon's folder and its tree", item)
	}
	if item := got.Items[1]; item.LocalPath != "" || item.Tree == nil || item.Tree.ID != created.Items[1].Tree.ID {
		t.Fatalf("a project without a folder on this daemon came back as %+v, want an empty local_path and its tree", item)
	}

	// A task without pairs has none to work on.
	empty := getDaemonScope(t, dbfx.Issue(t, "Project scope issue without pairs"), "?daemon_id="+projectScopeDaemonID)
	if len(empty.Items) != 0 {
		t.Fatalf("a task without pairs came back as %+v, want none", empty.Items)
	}
}

func TestDaemonIssueProjectScopeNamesADaemonOfTheCaller(t *testing.T) {
	f := newProjectScopeFixture(t)
	issueID := dbfx.Issue(t, "Project scope issue")
	otherID := f.member(t, "Project scope other owner", "member")
	// another-daemon has runtimes, but of another member.
	dbfx.Runtime(t, "Project scope other runtime", testutil.Cols{"daemon_id": "another-daemon", "owner_id": otherID})

	read := func(t *testing.T, req *http.Request) *testutil.Response {
		t.Helper()
		return testutil.Call(t, testHandler.DaemonGetIssueProjectScope, withURLParam(req, "issueId", issueID))
	}
	for name, tc := range map[string]struct {
		req    func() *http.Request
		status int
	}{
		"no daemon named": {
			req:    func() *http.Request { return newOwnerDaemonRequest(http.MethodGet, daemonScopePath(issueID, ""), nil) },
			status: http.StatusBadRequest,
		},
		"a daemon without a runtime here": {
			req: func() *http.Request {
				return newOwnerDaemonRequest(http.MethodGet, daemonScopePath(issueID, "?daemon_id=no-such-daemon"), nil)
			},
			status: http.StatusNotFound,
		},
		"another member's daemon": {
			req: func() *http.Request {
				return newOwnerDaemonRequest(http.MethodGet, daemonScopePath(issueID, "?daemon_id=another-daemon"), nil)
			},
			status: http.StatusNotFound,
		},
		"a token for another daemon": {
			req: func() *http.Request {
				return newDaemonTokenRequest(http.MethodGet, daemonScopePath(issueID, "?daemon_id="+projectScopeDaemonID), nil, testWorkspaceID, "token-daemon")
			},
			status: http.StatusForbidden,
		},
		"a token for another workspace": {
			req: func() *http.Request {
				return newDaemonTokenRequest(http.MethodGet, daemonScopePath(issueID, ""), nil, uuid.NewString(), "token-daemon")
			},
			status: http.StatusNotFound,
		},
	} {
		t.Run(name, func(t *testing.T) {
			read(t, tc.req()).Want(tc.status)
		})
	}
	// A task that does not exist is not found to a daemon that is otherwise allowed.
	missingID := uuid.NewString()
	missing := withURLParam(newOwnerDaemonRequest(http.MethodGet, daemonScopePath(missingID, "?daemon_id="+projectScopeDaemonID), nil), "issueId", missingID)
	testutil.Call(t, testHandler.DaemonGetIssueProjectScope, missing).Want(http.StatusNotFound)
	// A member outside the workspace may not read a task's scope at all.
	outsiderID := dbfx.User(t, "Project scope outsider", "project-scope-"+uuid.NewString()+"@example.test")
	read(t, scopeRequest(outsiderID, http.MethodGet, daemonScopePath(issueID, "?daemon_id="+projectScopeDaemonID), nil)).Want(http.StatusNotFound)

	// The daemon's own token and the owner of its runtimes both name it.
	read(t, newDaemonTokenRequest(http.MethodGet, daemonScopePath(issueID, ""), nil, testWorkspaceID, "token-daemon")).Want(http.StatusOK)
	read(t, scopeRequest(otherID, http.MethodGet, daemonScopePath(issueID, "?daemon_id=another-daemon"), nil)).Want(http.StatusOK)
}

func TestDaemonProjectScopeStateTransitions(t *testing.T) {
	f := newProjectScopeFixture(t)
	issueID := dbfx.Issue(t, "Project scope issue")
	otherIssueID := dbfx.Issue(t, "Project scope other issue")
	readyTree := putIssueScope(t, issueID, scopeTestPair{f.projectID, "main"}).Items[0].Tree.ID
	putIssueScope(t, otherIssueID, scopeTestPair{f.projectID, "main"})

	tree := func() IssueProjectTree {
		t.Helper()
		return *getIssueScope(t, issueID).Items[0].Tree
	}
	report := func(body map[string]any) *testutil.Response {
		t.Helper()
		return reportTreeState(t, issueID, readyTree, body)
	}

	// A ready tree is at an absolute path on its task branch.
	report(map[string]any{"daemon_id": projectScopeDaemonID, "state": "ready", "path": "/srv/tasks/demo/tree"}).Want(http.StatusBadRequest)
	report(map[string]any{"daemon_id": projectScopeDaemonID, "state": "ready", "path": "tasks/demo/tree", "task_branch": "task/HAN-1"}).Want(http.StatusBadRequest)
	report(map[string]any{"daemon_id": projectScopeDaemonID, "state": "ready", "path": "/srv/tasks/demo/tree", "task_branch": "task/HAN-1", "error": "stale"}).Want(http.StatusOK)
	if got := tree(); got.State != "ready" || got.Path != "/srv/tasks/demo/tree" || got.TaskBranch != "task/HAN-1" || got.Error != "" {
		t.Fatalf("ready tree = %+v", got)
	}

	// A tree on disk cannot go back to never having been built, and an
	// unknown state is not a transition.
	report(map[string]any{"daemon_id": projectScopeDaemonID, "state": "pending"}).Want(http.StatusConflict)
	report(map[string]any{"daemon_id": projectScopeDaemonID, "state": "creating"}).Want(http.StatusBadRequest)

	report(map[string]any{"daemon_id": projectScopeDaemonID, "state": "failed", "path": "/srv/tasks/demo/tree", "error": " disk full "}).Want(http.StatusOK)
	if got := tree(); got.State != "failed" || got.Error != "disk full" || got.Path != "/srv/tasks/demo/tree" {
		t.Fatalf("failed tree = %+v", got)
	}
	// Deleting the tree keeps its branch: the branch outlives the tree.
	report(map[string]any{"daemon_id": projectScopeDaemonID, "state": "deleted"}).Want(http.StatusOK)
	if got := tree(); got.State != "deleted" || got.Path != "" || got.TaskBranch != "task/HAN-1" || got.Error != "" {
		t.Fatalf("deleted tree = %+v", got)
	}
	report(map[string]any{"daemon_id": projectScopeDaemonID, "state": "pending"}).Want(http.StatusOK)
	if got := tree(); got.State != "pending" || got.TaskBranch != "task/HAN-1" {
		t.Fatalf("pending tree = %+v", got)
	}
	// A tree can be rebuilt from the task branch.
	report(map[string]any{"daemon_id": projectScopeDaemonID, "state": "ready", "path": "/srv/tasks/demo/tree-2", "task_branch": "task/HAN-1"}).Want(http.StatusOK)
	if got := tree(); got.State != "ready" || got.Path != "/srv/tasks/demo/tree-2" {
		t.Fatalf("rebuilt tree = %+v", got)
	}

	// A tree belongs to one task: another task's id does not reach it.
	req := testutil.WithURLParams(newOwnerDaemonRequest(http.MethodPost, daemonScopePath(otherIssueID, "")+"/"+readyTree+"/state",
		map[string]any{"daemon_id": projectScopeDaemonID, "state": "deleted"}), "issueId", otherIssueID, "treeId", readyTree)
	testutil.Call(t, testHandler.DaemonReportIssueProjectScopeState, req).Want(http.StatusNotFound)

	report(map[string]any{"state": "failed"}).Want(http.StatusBadRequest)
	report(map[string]any{"daemon_id": "no-such-daemon", "state": "failed"}).Want(http.StatusNotFound)
}

func TestIssueProjectScopeGoesWithItsIssue(t *testing.T) {
	f := newProjectScopeFixture(t)
	issueID := dbfx.Issue(t, "Project scope issue")
	keptID := dbfx.Issue(t, "Project scope kept issue")
	putIssueScope(t, issueID, scopeTestPair{f.projectID, "main"})
	putIssueScope(t, keptID, scopeTestPair{f.projectID, "main"})

	wsUUID, err := util.ParseUUID(testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	issueUUID, err := util.ParseUUID(issueID)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := testHandler.Queries.GetIssueInWorkspace(context.Background(), db.GetIssueInWorkspaceParams{ID: issueUUID, WorkspaceID: wsUUID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.deleteIssueAndCollectAttachmentURLs(context.Background(), issue, nil); err != nil {
		t.Fatalf("delete issue: %v", err)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_project_scope WHERE issue_id = $1`, issueID); n != 0 {
		t.Fatalf("%d pairs outlived their issue, want 0", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_project_scope WHERE issue_id = $1`, keptID); n != 1 {
		t.Fatalf("the other task kept %d pairs, want 1", n)
	}
}

func TestIssueProjectScopeGoesWithItsProject(t *testing.T) {
	f := newProjectScopeFixture(t)
	issueID := dbfx.Issue(t, "Project scope issue")
	putIssueScope(t, issueID, scopeTestPair{f.projectID, "main"}, scopeTestPair{f.otherProjectID, "main"})

	testutil.Call(t, testHandler.DeleteProject, withURLParam(scopeRequest(testUserID, http.MethodDelete, "/api/projects/"+f.projectID, nil), "id", f.projectID)).
		Want(http.StatusNoContent)
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_project_scope WHERE project_id = $1`, f.projectID); n != 0 {
		t.Fatalf("%d pairs outlived their project, want 0", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_project_scope WHERE project_id = $1`, f.otherProjectID); n != 1 {
		t.Fatalf("the other project kept %d pairs, want 1", n)
	}
}

func TestDeleteWorkspace_SweepsProjectScopes(t *testing.T) {
	f := testutil.New(testPool, testWorkspaceID, testUserID)
	f.WorkspaceID = f.Workspace(t, "Project scope teardown", "project-scope-teardown-"+uuid.NewString())
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	issueID := f.Issue(t, "Project scope teardown issue")
	projectID := f.Project(t, "Project scope teardown project")
	f.Insert(t, "issue_project_scope", testutil.Cols{
		"workspace_id": f.WorkspaceID, "issue_id": issueID, "project_id": projectID, "base_branch": "main", "position": 0,
	})

	testutil.Call(t, testHandler.DeleteWorkspace, withURLParam(scopeRequest(testUserID, http.MethodDelete, "/api/workspaces/"+f.WorkspaceID, nil), "id", f.WorkspaceID)).
		Want(http.StatusNoContent)
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_project_scope WHERE workspace_id = $1`, f.WorkspaceID); n != 0 {
		t.Fatalf("%d pairs survived workspace delete, want 0", n)
	}
}

func TestProjectScopeWritesRefuseMachineCredentials(t *testing.T) {
	f := newProjectScopeFixture(t)
	issueID := dbfx.Issue(t, "Project scope issue")
	putIssueScope(t, issueID, scopeTestPair{f.projectID, "main"})

	req := issueScopeRequest(testUserID, issueID, http.MethodPut, "", map[string]any{"items": []any{}})
	req.Header.Set("X-Actor-Source", "task_token")
	rec := httptest.NewRecorder()
	RequireHumanActor(http.HandlerFunc(testHandler.PutIssueProjectScope)).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a machine credential got %d %s, want 403", rec.Code, rec.Body.String())
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_project_scope WHERE issue_id = $1`, issueID); n != 1 {
		t.Fatalf("the task kept %d pairs, want 1", n)
	}
}

// A daemon can report a tree ready between a replace reading the task's pairs
// and deleting the ones the new list drops. The delete must leave that tree
// alone and the replace must be refused, as it is when the read saw it ready.
func TestIssueProjectScopeRemovalRacesAReportThatTurnsTheTreeReady(t *testing.T) {
	f := newProjectScopeFixture(t)
	issueID := dbfx.Issue(t, "Project scope issue")
	created := putIssueScope(t, issueID, scopeTestPair{f.projectID, "main"}, scopeTestPair{f.otherProjectID, "main"}, scopeTestPair{f.otherProjectID, "develop"})
	treeID := created.Items[0].Tree.ID
	// The new list keeps two of the three trees: a task may lose trees, but
	// not down to one, and the refused replace must leave the third alone.
	kept := scopeItems([]scopeTestPair{{f.otherProjectID, "main"}, {f.otherProjectID, "develop"}}, "base_branch")

	ctx := context.Background()
	report, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = report.Rollback(ctx) })
	if _, err := report.Exec(ctx, `UPDATE issue_project_scope SET state = 'ready' WHERE id = $1`, treeID); err != nil {
		t.Fatalf("write the uncommitted report: %v", err)
	}

	type answer struct {
		status int
		body   string
	}
	done := make(chan answer, 1)
	go func() {
		rec := httptest.NewRecorder()
		testHandler.PutIssueProjectScope(rec, issueScopeRequest(testUserID, issueID, http.MethodPut, "", map[string]any{"items": kept}))
		done <- answer{rec.Code, rec.Body.String()}
	}()

	// The replace has read the pairs once it waits for the row the report holds.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		err := testPool.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND usename = current_user
			  AND wait_event_type = 'Lock' AND pid <> pg_backend_pid()`).Scan(&waiting)
		if err != nil {
			t.Fatalf("look for the waiting replace: %v", err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the replace never waited for the reported tree")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := report.Commit(ctx); err != nil {
		t.Fatalf("commit the report: %v", err)
	}

	got := <-done
	if got.status != http.StatusConflict {
		t.Fatalf("removing a pair whose tree turned ready got %d %s, want 409", got.status, got.body)
	}
	after := getIssueScope(t, issueID)
	if len(after.Items) != 3 || after.Items[0].Tree.ID != treeID || after.Items[0].Tree.State != "ready" {
		t.Fatalf("the scope after the refused replace = %+v, want all three trees, the reported one ready", after.Items)
	}
}
