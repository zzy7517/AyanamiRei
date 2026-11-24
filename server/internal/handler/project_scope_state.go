package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/projectaccess"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// What became of the tree built for one of a task's pairs, as the desktop app
// that built or removed it reports it. The server only stores what it is told.

type projectTreeStateRequest struct {
	State      string `json:"state"`
	Path       string `json:"path"`
	TaskBranch string `json:"task_branch"`
	Error      string `json:"error"`
}

// projectTreeTransitions lists the states a tree may be reported in from each
// one, its own included. A ready tree is on disk, and that is what keeps its
// pair from being removed, so it cannot go back to pending without being
// deleted or failing first.
var projectTreeTransitions = map[string][]string{
	projectTreePending: {projectTreePending, projectTreeReady, projectTreeFailed, projectTreeDeleted},
	projectTreeReady:   {projectTreeReady, projectTreeFailed, projectTreeDeleted},
	projectTreeFailed:  {projectTreePending, projectTreeReady, projectTreeFailed, projectTreeDeleted},
	projectTreeDeleted: {projectTreePending, projectTreeReady, projectTreeFailed, projectTreeDeleted},
}

// ReportIssueProjectTreeState records what the caller's desktop app says
// became of the tree of one of a task's pairs: built, failed, or removed. A
// report the tree's state does not allow is refused and changes nothing.
func (h *Handler) ReportIssueProjectTreeState(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	treeID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "treeId"), "tree_id")
	if !ok {
		return
	}
	hidden, ok := h.requestHiddenProjects(w, r, uuidToString(issue.WorkspaceID))
	if !ok {
		return
	}
	var req projectTreeStateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	h.writeProjectTreeState(w, r, issue.ID, issue.WorkspaceID, hidden, treeID, req, pgtype.Text{})
}

// writeProjectTreeState moves the tree treeID of a task to the state a report
// names, as the report describes it. daemonID is the machine that reported,
// when a daemon did; empty keeps the one stored.
func (h *Handler) writeProjectTreeState(
	w http.ResponseWriter,
	r *http.Request,
	issueID, workspaceID pgtype.UUID,
	hidden projectaccess.Hidden,
	treeID pgtype.UUID,
	req projectTreeStateRequest,
	daemonID pgtype.Text,
) {
	row, err := h.Queries.GetIssueProjectScope(r.Context(), db.GetIssueProjectScopeParams{
		ID: treeID, IssueID: issueID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tree not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load tree")
		return
	}
	if !hidden.CanSeeProject(row.ProjectID) {
		writeError(w, http.StatusNotFound, "tree not found")
		return
	}
	if _, known := projectTreeTransitions[req.State]; !known {
		writeError(w, http.StatusBadRequest, "invalid state")
		return
	}
	if !slices.Contains(projectTreeTransitions[row.State], req.State) {
		writeError(w, http.StatusConflict, fmt.Sprintf("a %s tree cannot become %s", row.State, req.State))
		return
	}
	path, taskBranch, errText, ok := projectTreeReport(w, req.State, req.Path, req.TaskBranch, row.TaskBranch, req.Error)
	if !ok {
		return
	}
	updated, err := h.Queries.UpdateIssueProjectScopeTree(r.Context(), db.UpdateIssueProjectScopeTreeParams{
		State: req.State,
		Path:  path,
		// FromStates holds the state this request read: a report racing
		// another one changes nothing and is told to read again.
		TaskBranch:  taskBranch,
		Error:       errText,
		ID:          treeID,
		IssueID:     issueID,
		WorkspaceID: workspaceID,
		FromStates:  []string{row.State},
		DaemonID:    daemonID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "the tree's state changed; read it again")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update tree")
		return
	}
	writeJSON(w, http.StatusOK, issueProjectTreeResponse{Tree: *issueProjectTree(updated)})
}

// projectTreeReport settles what a state report stores. The task branch
// outlives the tree, so a report that names none keeps the stored one. The
// path and the error belong to a tree that exists, so a tree that is pending
// or was deleted loses both.
func projectTreeReport(w http.ResponseWriter, state, path, taskBranch, storedBranch, errText string) (string, string, string, bool) {
	badRequest := func(msg string) (string, string, string, bool) {
		writeError(w, http.StatusBadRequest, msg)
		return "", "", "", false
	}
	if taskBranch != "" {
		if err := validateGitRef(taskBranch); err != nil {
			return badRequest(err.Error())
		}
	}
	if len(path) > maxProjectScopePathLength {
		return badRequest(fmt.Sprintf("path must be at most %d characters", maxProjectScopePathLength))
	}
	if taskBranch == "" {
		taskBranch = storedBranch
	}
	switch state {
	case projectTreeReady:
		if !isAbsoluteLocalPath(path) {
			return badRequest("a ready tree needs an absolute path")
		}
		if taskBranch == "" {
			return badRequest("a ready tree needs its task branch")
		}
		return path, taskBranch, "", true
	case projectTreeFailed, projectTreeDeleted:
		errText = truncateRunes(redact.Text(strings.TrimSpace(errText)), maxProjectScopeErrorLength)
		if state == projectTreeDeleted {
			path = ""
		}
		return path, taskBranch, errText, true
	default: // pending
		return "", taskBranch, "", true
	}
}
