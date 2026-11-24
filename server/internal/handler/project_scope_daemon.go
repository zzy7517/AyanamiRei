package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The daemon reads the pairs a task works on, each with its project's local
// folder on the daemon's machine, and reports the state of the tree it builds
// for a pair.

type daemonProjectScopeItem struct {
	ProjectScopeItem
	// LocalPath is the project's local_directory on this daemon's machine,
	// empty when the project has none there.
	LocalPath string `json:"local_path"`
}

type daemonProjectScopeResponse struct {
	IssueID string                   `json:"issue_id"`
	Items   []daemonProjectScopeItem `json:"items"`
}

type daemonProjectScopeStateRequest struct {
	projectTreeStateRequest
	DaemonID string `json:"daemon_id"`
}

type issueProjectTreeResponse struct {
	Tree IssueProjectTree `json:"tree"`
}

// loadDaemonProjectScopeIssue loads the workspace of the task a daemon route
// names by id alone, and gates the caller on it.
func (h *Handler) loadDaemonProjectScopeIssue(w http.ResponseWriter, r *http.Request, issueID pgtype.UUID) (pgtype.UUID, bool) {
	workspaceID, err := h.Queries.GetIssueProjectScopeWorkspace(r.Context(), issueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue not found")
			return pgtype.UUID{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load issue")
		return pgtype.UUID{}, false
	}
	if !h.requireDaemonWorkspaceAccess(w, r, uuidToString(workspaceID)) {
		return pgtype.UUID{}, false
	}
	return workspaceID, true
}

// requireDaemonProjectScopeIssueVisible answers 404 for a task whose own
// project the caller may not see, as the other daemon routes that load an
// issue do.
func (h *Handler) requireDaemonProjectScopeIssueVisible(w http.ResponseWriter, r *http.Request, issueID pgtype.UUID, hidden projectaccess.Hidden) bool {
	if hidden.Empty() {
		return true
	}
	projectID, err := h.Queries.GetIssueProjectID(r.Context(), issueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue not found")
			return false
		}
		writeError(w, http.StatusInternalServerError, "failed to resolve project access")
		return false
	}
	if !hidden.CanSeeProject(projectID) {
		writeError(w, http.StatusNotFound, "issue not found")
		return false
	}
	return true
}

// projectScopeDaemonID names the machine the caller acts for. A daemon token
// is bound to one daemon and that id wins. A user credential has to name a
// daemon of its own: one with a runtime in the workspace that the caller
// owns, which is the only thing tying a daemon id to the member behind it.
func (h *Handler) projectScopeDaemonID(w http.ResponseWriter, r *http.Request, supplied string, workspaceID pgtype.UUID) (string, bool) {
	if tokenDaemonID := middleware.DaemonIDFromContext(r.Context()); tokenDaemonID != "" {
		if supplied != "" && supplied != tokenDaemonID {
			writeError(w, http.StatusForbidden, "daemon identity mismatch")
			return "", false
		}
		return tokenDaemonID, true
	}
	if supplied == "" {
		writeError(w, http.StatusBadRequest, "daemon_id is required")
		return "", false
	}
	owners, err := h.Queries.ListRuntimeOwnersByDaemon(r.Context(), db.ListRuntimeOwnersByDaemonParams{
		WorkspaceID: workspaceID, DaemonID: strToText(supplied),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load runtimes")
		return "", false
	}
	if len(owners) == 0 {
		writeError(w, http.StatusNotFound, "machine not found")
		return "", false
	}
	for _, owner := range owners {
		if !daemonRuntimeBindingAllowed(r, strToText(supplied), owner.OwnerID) {
			writeError(w, http.StatusNotFound, "machine not found")
			return "", false
		}
	}
	return supplied, true
}

// projectLocalPathForDaemon is the project's local_directory on the daemon's
// machine, or empty when the project has none there.
func (h *Handler) projectLocalPathForDaemon(ctx context.Context, projectID, workspaceID pgtype.UUID, daemonID string) (string, error) {
	resources, err := h.Queries.ListProjectResourcesInWorkspace(ctx, db.ListProjectResourcesInWorkspaceParams{
		ProjectID: projectID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return "", err
	}
	for _, resource := range resources {
		if resource.ResourceType != "local_directory" {
			continue
		}
		var ref localDirectoryRef
		if json.Unmarshal(resource.ResourceRef, &ref) == nil && ref.DaemonID == daemonID {
			return ref.LocalPath, nil
		}
	}
	return "", nil
}

// DaemonGetIssueProjectScope returns the pairs a task works on, each with the
// project's local folder on the daemon's machine.
func (h *Handler) DaemonGetIssueProjectScope(w http.ResponseWriter, r *http.Request) {
	issueID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "issueId"), "issue_id")
	if !ok {
		return
	}
	workspaceID, ok := h.loadDaemonProjectScopeIssue(w, r, issueID)
	if !ok {
		return
	}
	hidden, ok := h.daemonHiddenProjects(w, r, uuidToString(workspaceID))
	if !ok {
		return
	}
	if !h.requireDaemonProjectScopeIssueVisible(w, r, issueID, hidden) {
		return
	}
	daemonID, ok := h.projectScopeDaemonID(w, r, strings.TrimSpace(r.URL.Query().Get("daemon_id")), workspaceID)
	if !ok {
		return
	}
	rows, err := h.Queries.ListIssueProjectScopes(r.Context(), db.ListIssueProjectScopesParams{
		IssueID: issueID, WorkspaceID: workspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue project scope")
		return
	}
	resp := daemonProjectScopeResponse{
		IssueID: uuidToString(issueID),
		Items:   make([]daemonProjectScopeItem, 0),
	}
	for _, row := range rows {
		// Pairs the daemon may not see do not exist for it.
		if !hidden.CanSeeProject(row.ProjectID) {
			continue
		}
		localPath, err := h.projectLocalPathForDaemon(r.Context(), row.ProjectID, workspaceID, daemonID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load project resources")
			return
		}
		resp.Items = append(resp.Items, daemonProjectScopeItem{
			ProjectScopeItem: projectScopeItem(row),
			LocalPath:        localPath,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// DaemonReportIssueProjectScopeState records what the daemon reports about
// the tree it built for one of a task's own pairs. A report the tree's state
// does not allow is refused and changes nothing.
func (h *Handler) DaemonReportIssueProjectScopeState(w http.ResponseWriter, r *http.Request) {
	issueID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "issueId"), "issue_id")
	if !ok {
		return
	}
	treeID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "treeId"), "tree_id")
	if !ok {
		return
	}
	workspaceID, ok := h.loadDaemonProjectScopeIssue(w, r, issueID)
	if !ok {
		return
	}
	hidden, ok := h.daemonHiddenProjects(w, r, uuidToString(workspaceID))
	if !ok {
		return
	}
	if !h.requireDaemonProjectScopeIssueVisible(w, r, issueID, hidden) {
		return
	}
	var req daemonProjectScopeStateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// The reporting daemon stays on the row: it is the daemon that hosts the
	// tree from now on.
	daemonID, ok := h.projectScopeDaemonID(w, r, strings.TrimSpace(req.DaemonID), workspaceID)
	if !ok {
		return
	}
	h.writeProjectTreeState(w, r, issueID, workspaceID, hidden, treeID, req.projectTreeStateRequest, strToText(daemonID))
}
