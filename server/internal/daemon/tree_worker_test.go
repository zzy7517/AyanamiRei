package daemon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/treeworker"
)

func TestTreeWorkerAPISpeaksTheRoutes(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/daemon/issues/project-scope/work":
			io.WriteString(w, `{"items":[{"tree_id":"t1","issue_id":"issue-1","task_key":"T-1","project_id":"p1","project_name":"App",
				"base_branch":"main","task_branch":"task/T-1","action":"create","local_path":"/repos/app"}]}`)
		case "/api/daemon/issues/project-scope/mature":
			io.WriteString(w, `{"items":[]}`)
		case "/api/daemon/issues/issue-1/project-scope":
			io.WriteString(w, `{"issue_id":"issue-1","items":[{"project_id":"p1","base_branch":"main","position":0,
				"tree":{"id":"t1","state":"pending"},"local_path":"/repos/app"}]}`)
		case "/api/daemon/issues/issue-1/project-scope/t1/state":
			io.WriteString(w, `{"tree":{"id":"t1"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	api := &treeWorkerAPI{client: NewClient(srv.URL), daemonID: "daemon-1"}
	ctx := context.Background()

	items, err := api.Work(ctx)
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	want := []treeworker.WorkItem{{
		TreeID: "t1", IssueID: "issue-1", TaskKey: "T-1", ProjectID: "p1", ProjectName: "App",
		BaseBranch: "main", TaskBranch: "task/T-1", Action: "create", LocalPath: "/repos/app",
	}}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("Work = %+v, want %+v", items, want)
	}
	if _, err := api.Mature(ctx); err != nil {
		t.Fatalf("Mature: %v", err)
	}
	scope, err := api.Scope(ctx, "issue-1")
	if err != nil {
		t.Fatalf("Scope: %v", err)
	}
	if scope.IssueID != "issue-1" || len(scope.Items) != 1 || scope.Items[0].Tree == nil || scope.Items[0].Tree.ID != "t1" {
		t.Fatalf("Scope = %+v, want the pending tree t1", scope)
	}
	report := treeworker.TreeReport{State: "ready", Path: "/trees/T-1", TaskBranch: "task/T-1"}
	if err := api.ReportState(ctx, "issue-1", "t1", report); err != nil {
		t.Fatalf("ReportState: %v", err)
	}

	// The recorded calls are compared without the whitespace of the bodies split over lines.
	got := make([]string, len(calls))
	for i, call := range calls {
		got[i] = strings.Join(strings.Fields(call), " ")
	}
	wantCalls := []string{
		`GET /api/daemon/issues/project-scope/work?daemon_id=daemon-1`,
		`GET /api/daemon/issues/project-scope/mature?daemon_id=daemon-1`,
		`GET /api/daemon/issues/issue-1/project-scope?daemon_id=daemon-1`,
		`POST /api/daemon/issues/issue-1/project-scope/t1/state {"daemon_id":"daemon-1","state":"ready","path":"/trees/T-1","task_branch":"task/T-1","error":""}`,
	}
	if !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantCalls, "\n"))
	}
}

func TestTreeWorkerErrorMarksNotFoundAndConflict(t *testing.T) {
	for _, tc := range []struct {
		status   int
		notFound bool
		conflict bool
	}{
		{status: http.StatusNotFound, notFound: true},
		{status: http.StatusConflict, conflict: true},
		{status: http.StatusInternalServerError},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			io.WriteString(w, `{"error":"nope"}`)
		}))
		err := treeWorkerError(NewClient(srv.URL).getJSON(context.Background(), "/api/daemon/issues/project-scope/work", nil))
		srv.Close()
		if got := errors.Is(err, treeworker.ErrNotFound); got != tc.notFound {
			t.Errorf("%d: ErrNotFound = %v, want %v (%v)", tc.status, got, tc.notFound, err)
		}
		if got := errors.Is(err, treeworker.ErrConflict); got != tc.conflict {
			t.Errorf("%d: ErrConflict = %v, want %v (%v)", tc.status, got, tc.conflict, err)
		}
	}
}

func TestLoadConfigTaskTrees(t *testing.T) {
	stageFakeAgent(t)
	home := t.TempDir()
	redirectTestHome(t, home)
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "missing-shell"))
	t.Setenv("MULTICA_TASK_ROOT", "")
	base := Overrides{ServerURL: "http://localhost:0", WorkspacesRoot: t.TempDir()}

	cfg, err := LoadConfig(base)
	if err != nil {
		t.Fatalf("LoadConfig default: %v", err)
	}
	if want := filepath.Join(home, "ayanami-tasks"); cfg.TaskRoot != want {
		t.Errorf("TaskRoot = %q, want %q", cfg.TaskRoot, want)
	}

	t.Setenv("MULTICA_TASK_ROOT", filepath.Join(home, "elsewhere")+string(os.PathSeparator))
	cfg, err = LoadConfig(base)
	if err != nil {
		t.Fatalf("LoadConfig override: %v", err)
	}
	if cfg.TaskRoot != filepath.Join(home, "elsewhere") {
		t.Errorf("TaskRoot = %q, want the MULTICA_TASK_ROOT cleaned", cfg.TaskRoot)
	}
	if want := filepath.Join(home, "ayanami-projects"); cfg.ProjectRoot != want {
		t.Errorf("ProjectRoot = %q, want %q: the task root does not move the default project folder", cfg.ProjectRoot, want)
	}
}

// The default project folder follows the same rule as the task trees: the
// environment variable, else a directory under the home directory.
func TestLoadConfigProjectRoot(t *testing.T) {
	stageFakeAgent(t)
	home := t.TempDir()
	redirectTestHome(t, home)
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "missing-shell"))
	t.Setenv(ProjectRootEnv, "")
	base := Overrides{ServerURL: "http://localhost:0", WorkspacesRoot: t.TempDir()}

	cfg, err := LoadConfig(base)
	if err != nil {
		t.Fatalf("LoadConfig default: %v", err)
	}
	if want := filepath.Join(home, DefaultProjectRootName); cfg.ProjectRoot != want {
		t.Errorf("ProjectRoot = %q, want %q", cfg.ProjectRoot, want)
	}

	t.Setenv(ProjectRootEnv, filepath.Join(home, "code")+string(os.PathSeparator))
	cfg, err = LoadConfig(base)
	if err != nil {
		t.Fatalf("LoadConfig override: %v", err)
	}
	if cfg.ProjectRoot != filepath.Join(home, "code") {
		t.Errorf("ProjectRoot = %q, want the %s cleaned", cfg.ProjectRoot, ProjectRootEnv)
	}
	if want := filepath.Join(home, "ayanami-tasks"); cfg.TaskRoot != want {
		t.Errorf("TaskRoot = %q, want %q: the project root does not move the task trees", cfg.TaskRoot, want)
	}
}
