package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/multica-ai/multica/server/internal/daemon/treeworker"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// treeWorkerLoop builds, removes and cleans up the task trees under
// cfg.TaskRoot for as long as the daemon runs.
func (d *Daemon) treeWorkerLoop(ctx context.Context) {
	worker, err := treeworker.New(&treeWorkerAPI{client: d.client, daemonID: d.cfg.DaemonID}, treeworker.Config{
		Root: d.cfg.TaskRoot,
	}, d.logger)
	if err != nil {
		// A root inside a git checkout ends up here rather than failing
		// every task later.
		d.logger.Warn("task trees: worker not started", "root", d.cfg.TaskRoot, "error", err)
		return
	}
	worker.Run(ctx)
}

// treeWorkerAPI is treeworker.API over the daemon's client.
type treeWorkerAPI struct {
	client   *Client
	daemonID string
}

func (a *treeWorkerAPI) Work(ctx context.Context) ([]treeworker.WorkItem, error) {
	var resp struct {
		Items []treeworker.WorkItem `json:"items"`
	}
	err := a.client.getJSON(ctx, "/api/daemon/issues/project-scope/work?"+a.query(), &resp)
	return resp.Items, treeWorkerError(err)
}

func (a *treeWorkerAPI) Mature(ctx context.Context) ([]treeworker.WorkItem, error) {
	var resp struct {
		Items []treeworker.WorkItem `json:"items"`
	}
	err := a.client.getJSON(ctx, "/api/daemon/issues/project-scope/mature?"+a.query(), &resp)
	return resp.Items, treeWorkerError(err)
}

func (a *treeWorkerAPI) Scope(ctx context.Context, issueID string) (*treeworker.IssueScope, error) {
	var scope treeworker.IssueScope
	path := "/api/daemon/issues/" + url.PathEscape(issueID) + "/project-scope?" + a.query()
	if err := a.client.getJSON(ctx, path, &scope); err != nil {
		return nil, treeWorkerError(err)
	}
	return &scope, nil
}

func (a *treeWorkerAPI) ReportState(ctx context.Context, issueID, treeID string, report treeworker.TreeReport) error {
	path := fmt.Sprintf("/api/daemon/issues/%s/project-scope/%s/state", url.PathEscape(issueID), url.PathEscape(treeID))
	body := treeStateReport{
		DaemonID:   a.daemonID,
		State:      report.State,
		Path:       report.Path,
		TaskBranch: report.TaskBranch,
		Error:      report.Error,
	}
	return treeWorkerError(a.client.postJSON(ctx, path, body, nil))
}

func (a *treeWorkerAPI) query() string {
	return url.Values{"daemon_id": []string{a.daemonID}}.Encode()
}

// treeStateReport is the body of a state report.
type treeStateReport struct {
	DaemonID   string `json:"daemon_id"`
	State      string `json:"state"`
	Path       string `json:"path"`
	TaskBranch string `json:"task_branch"`
	Error      string `json:"error"`
}

// treeWorkerError marks the two answers the worker treats by kind: 404, for
// a task it does not know or the routes a server keeps behind its flag, and
// 409, for a report that found the tree's state changed meanwhile.
func treeWorkerError(err error) error {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return err
	}
	switch reqErr.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s %s", treeworker.ErrNotFound, reqErr.Method, reqErr.Path)
	case http.StatusConflict:
		return fmt.Errorf("%w: %s", treeworker.ErrConflict, redact.Text(reqErr.Body))
	}
	return err
}
