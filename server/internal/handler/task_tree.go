package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/daemon/tasktree"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The task-tree lifecycle: a person asks for a tree to go or to be built
// again, and the daemon lists the trees it has to create or remove.

// Behind the task_sessions_v1 flag: every route here answers 404 while it is
// off.
const (
	codeTreeReady        = "tree_ready"
	codeTreePending      = "tree_pending"
	codeTreeLayoutChange = "tree_layout_change"
	codeTreeStateChanged = "tree_state_changed"

	taskTreeNotFoundMsg = "tree not found"

	// taskTreeRetentionKey is the workspace setting with how many days a
	// completed task keeps its trees.
	taskTreeRetentionKey = "task_tree_retention_days"

	// DefaultTaskTreeRetentionDays is how many days a completed task keeps
	// its trees when its workspace has not set it.
	DefaultTaskTreeRetentionDays = 5
	// MaxTaskTreeRetentionDays is the most a workspace may set: a hundred
	// years. A number large enough no longer names a date the mature list
	// can compare against.
	MaxTaskTreeRetentionDays = 36500
)

// requireTaskSessionsV1 answers 404 while the task_sessions_v1 flag is off.
func (h *Handler) requireTaskSessionsV1(w http.ResponseWriter, r *http.Request) bool {
	if !featureflags.TaskSessionsV1Enabled(r.Context(), h.FeatureFlags) {
		writeError(w, http.StatusNotFound, "not found")
		return false
	}
	return true
}

// DaemonTaskTreeWorkItem is one tree a daemon has to act on. TaskKey is the
// key its tree layout names the task's directory and branches by. LocalPath is
// the project's local_directory on that daemon.
type DaemonTaskTreeWorkItem struct {
	TreeID      string `json:"tree_id"`
	IssueID     string `json:"issue_id"`
	TaskKey     string `json:"task_key"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	BaseBranch  string `json:"base_branch"`
	TaskBranch  string `json:"task_branch"`
	Action      string `json:"action"`
	LocalPath   string `json:"local_path"`
}

type daemonTaskTreeWorkResponse struct {
	Items []DaemonTaskTreeWorkItem `json:"items"`
}

// DeleteIssueProjectTree marks a tree deleted: it must not exist. The task
// branch is kept, for a rebuild continues it; the daemon makes it true on
// disk. Asking again for a deleted tree is not an error.
func (h *Handler) DeleteIssueProjectTree(w http.ResponseWriter, r *http.Request) {
	if !h.requireTaskSessionsV1(w, r) {
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	treeID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "treeId"), "tree_id")
	if !ok {
		return
	}
	updated, ok := h.setIssueProjectTree(w, r, issue, treeID, projectTreeDeleted)
	if !ok {
		return
	}
	_ = updated
	w.WriteHeader(http.StatusNoContent)
}

// RebuildIssueProjectTree asks for a deleted or failed tree to be built again,
// from its base branch and continuing its task branch.
func (h *Handler) RebuildIssueProjectTree(w http.ResponseWriter, r *http.Request) {
	if !h.requireTaskSessionsV1(w, r) {
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	treeID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "treeId"), "tree_id")
	if !ok {
		return
	}
	updated, ok := h.setIssueProjectTree(w, r, issue, treeID, projectTreePending)
	if !ok {
		return
	}
	writeJSON(w, http.StatusAccepted, issueProjectTreeResponse{Tree: *issueProjectTree(updated)})
}

// setIssueProjectTree moves a task's tree to state. Rebuilding is refused
// while the tree is ready or already waiting to be built.
func (h *Handler) setIssueProjectTree(w http.ResponseWriter, r *http.Request, issue db.Issue, treeID pgtype.UUID, state string) (db.IssueProjectScope, bool) {
	tx, qtx, ok := h.beginProjectScopeWrite(w, r)
	if !ok {
		return db.IssueProjectScope{}, false
	}
	defer tx.Rollback(r.Context())
	tree, err := qtx.GetIssueProjectScope(r.Context(), db.GetIssueProjectScopeParams{
		ID: treeID, IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, taskTreeNotFoundMsg)
			return db.IssueProjectScope{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load tree")
		return db.IssueProjectScope{}, false
	}
	hidden, ok := h.requestHiddenProjects(w, r, uuidToString(issue.WorkspaceID))
	if !ok {
		return db.IssueProjectScope{}, false
	}
	if !hidden.CanSeeProject(tree.ProjectID) {
		writeError(w, http.StatusNotFound, taskTreeNotFoundMsg)
		return db.IssueProjectScope{}, false
	}
	if state == projectTreePending {
		switch tree.State {
		case projectTreeReady:
			writeErrorCode(w, http.StatusConflict, codeTreeReady, "the tree is built")
			return db.IssueProjectScope{}, false
		case projectTreePending:
			writeErrorCode(w, http.StatusConflict, codeTreePending, "the tree is already waiting to be built")
			return db.IssueProjectScope{}, false
		}
	}
	// A pending or deleted tree is not on disk, so it loses its path and error
	// and keeps its branch.
	path, taskBranch, errText, ok := projectTreeReport(w, state, "", "", tree.TaskBranch, "")
	if !ok {
		return db.IssueProjectScope{}, false
	}
	updated, err := qtx.UpdateIssueProjectScopeTree(r.Context(), db.UpdateIssueProjectScopeTreeParams{
		State: state, Path: path, TaskBranch: taskBranch, Error: errText,
		ID: tree.ID, IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
		DaemonID: tree.DaemonID, FromStates: []string{tree.State},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorCode(w, http.StatusConflict, codeTreeStateChanged, "the tree's state changed; read it again")
		return db.IssueProjectScope{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update tree")
		return db.IssueProjectScope{}, false
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return db.IssueProjectScope{}, false
	}
	return updated, true
}

// daemonTaskTreeScope is one workspace a daemon acts in for the caller, with
// the projects the caller cannot see there.
type daemonTaskTreeScope struct {
	workspaceID pgtype.UUID
	hidden      projectaccess.Hidden
}

// daemonTaskTreeScopes names the daemon the caller acts for and lists the
// workspaces it acts in: those where it holds a runtime row that the caller
// may reach. A user credential has to own every runtime the daemon holds in a
// workspace, as on the other daemon routes. A daemon without one is not found.
func (h *Handler) daemonTaskTreeScopes(w http.ResponseWriter, r *http.Request, supplied string) (string, []daemonTaskTreeScope, bool) {
	daemonID := strings.TrimSpace(supplied)
	tokenDaemonID := middleware.DaemonIDFromContext(r.Context())
	if tokenDaemonID != "" {
		if daemonID != "" && daemonID != tokenDaemonID {
			writeError(w, http.StatusForbidden, "daemon identity mismatch")
			return "", nil, false
		}
		daemonID = tokenDaemonID
	} else if daemonID == "" {
		writeError(w, http.StatusBadRequest, "daemon_id is required")
		return "", nil, false
	}
	rows, err := h.Queries.ListRuntimeWorkspacesByDaemon(r.Context(), strToText(daemonID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load runtimes")
		return "", nil, false
	}
	userID := requestUserID(r)
	scopes := make([]daemonTaskTreeScope, 0, len(rows))
	for _, workspaceID := range rows {
		workspace := uuidToString(workspaceID)
		if tokenDaemonID != "" {
			// A token is bound to one workspace of the daemon.
			if workspace != middleware.DaemonWorkspaceIDFromContext(r.Context()) {
				continue
			}
			if !h.requireDaemonWorkspaceAccess(w, r, workspace) {
				return "", nil, false
			}
			scopes = append(scopes, daemonTaskTreeScope{workspaceID: workspaceID})
			continue
		}
		owners, err := h.Queries.ListRuntimeOwnersByDaemon(r.Context(), db.ListRuntimeOwnersByDaemonParams{
			WorkspaceID: workspaceID, DaemonID: strToText(daemonID),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load runtimes")
			return "", nil, false
		}
		if len(owners) == 0 {
			continue
		}
		bound := true
		for _, owner := range owners {
			if !daemonRuntimeBindingAllowed(r, strToText(daemonID), owner.OwnerID) {
				bound = false
			}
		}
		if !bound {
			continue
		}
		if _, err := h.getWorkspaceMember(r.Context(), userID, workspace); err != nil {
			continue
		}
		hidden, ok := h.requestHiddenProjects(w, r, workspace)
		if !ok {
			return "", nil, false
		}
		scopes = append(scopes, daemonTaskTreeScope{workspaceID: workspaceID, hidden: hidden})
	}
	if len(scopes) == 0 {
		writeError(w, http.StatusNotFound, "machine not found")
		return "", nil, false
	}
	return daemonID, scopes, true
}

// taskTreeKey is the key the daemon's tree layout names a task's directory
// and branches by: its identifier with the characters a name cannot hold
// replaced.
func taskTreeKey(issuePrefix, workspaceName string, number int32) string {
	if issuePrefix == "" {
		issuePrefix = legacyIssuePrefixFromName(workspaceName)
	}
	return tasktree.TaskKey(service.IssueIdentifier(issuePrefix, number))
}

// DaemonListIssueProjectTreeWork returns the trees the daemon has to create
// (pending) or remove from disk (deleted), among the projects it holds a
// local_directory for. A pending tree goes to a daemon that may build it: one
// with no daemon recorded yet, or the one that held it before a rebuild. A
// deleted tree goes only to the daemon that built it.
func (h *Handler) DaemonListIssueProjectTreeWork(w http.ResponseWriter, r *http.Request) {
	if !h.requireTaskSessionsV1(w, r) {
		return
	}
	daemonID, scopes, ok := h.daemonTaskTreeScopes(w, r, r.URL.Query().Get("daemon_id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListIssueProjectScopeTreeWork(r.Context(), db.ListIssueProjectScopeTreeWorkParams{
		WorkspaceIds: scopeWorkspaceIDs(scopes), DaemonID: daemonID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tree work")
		return
	}
	work := make([]taskTreeWorkRow, 0, len(rows))
	for _, row := range rows {
		work = append(work, taskTreeWorkRow{item: DaemonTaskTreeWorkItem{
			TreeID:      uuidToString(row.ID),
			IssueID:     uuidToString(row.IssueID),
			TaskKey:     taskTreeKey(row.IssuePrefix, row.WorkspaceName, row.IssueNumber),
			ProjectID:   uuidToString(row.ProjectID),
			ProjectName: row.ProjectName,
			BaseBranch:  row.BaseBranch,
			TaskBranch:  row.TaskBranch,
			Action:      row.Action,
			LocalPath:   row.LocalPath,
		}, workspaceID: row.WorkspaceID, projectID: row.ProjectID})
	}
	writeJSON(w, http.StatusOK, daemonTaskTreeWorkResponse{Items: visibleTaskTreeItems(work, scopes)})
}

// taskTreeRetentionDays reads how many days a completed task keeps its trees
// in the workspace with these settings. Absent means the default, and so does
// anything but a whole number from 1 to MaxTaskTreeRetentionDays, which a
// settings write never stores (see checkTaskTreeRetention).
func taskTreeRetentionDays(settings []byte) int {
	var s struct {
		Days *int `json:"task_tree_retention_days"`
	}
	if len(settings) == 0 || json.Unmarshal(settings, &s) != nil || s.Days == nil ||
		*s.Days < 1 || *s.Days > MaxTaskTreeRetentionDays {
		return DefaultTaskTreeRetentionDays
	}
	return *s.Days
}

// checkTaskTreeRetention refuses a task tree retention that is not a whole
// number of days from 1 to MaxTaskTreeRetentionDays: a daemon removes trees
// after this long, and the mature list cannot compare against a longer time.
// The key may be left out, which leaves the workspace on the default.
func checkTaskTreeRetention(settings map[string]any) error {
	raw, sent := settings[taskTreeRetentionKey]
	if !sent {
		return nil
	}
	days, ok := raw.(float64)
	if !ok || days < 1 || days > MaxTaskTreeRetentionDays || days != math.Trunc(days) {
		return fmt.Errorf("%s must be a whole number of days from 1 to %d", taskTreeRetentionKey, MaxTaskTreeRetentionDays)
	}
	return nil
}

// taskTreeRetentionCutoffs gives, keyed by workspace ID, when a completed
// task's trees stop being kept there: now, less the workspace's retention. A
// workspace whose settings could not be read has no cutoff and keeps
// nothing listed as mature.
func (h *Handler) taskTreeRetentionCutoffs(w http.ResponseWriter, r *http.Request, scopes []daemonTaskTreeScope) (map[string]time.Time, bool) {
	rows, err := h.Queries.ListWorkspaceSettingsByIds(r.Context(), scopeWorkspaceIDs(scopes))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workspace settings")
		return nil, false
	}
	now := time.Now()
	cutoffs := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		cutoffs[uuidToString(row.ID)] = now.AddDate(0, 0, -taskTreeRetentionDays(row.Settings))
	}
	return cutoffs, true
}

// DaemonListIssueProjectMatureTrees returns the ready trees of tasks that
// were completed before their workspace's tree retention ran out, for the
// daemon to remove.
func (h *Handler) DaemonListIssueProjectMatureTrees(w http.ResponseWriter, r *http.Request) {
	if !h.requireTaskSessionsV1(w, r) {
		return
	}
	daemonID, scopes, ok := h.daemonTaskTreeScopes(w, r, r.URL.Query().Get("daemon_id"))
	if !ok {
		return
	}
	cutoffs, ok := h.taskTreeRetentionCutoffs(w, r, scopes)
	if !ok {
		return
	}
	// One list for every workspace the daemon acts in. It is read up to the
	// latest cutoff among them, so that it holds every tree any of them no
	// longer keeps; each tree is then kept to its own workspace's cutoff.
	var latest time.Time
	for _, cutoff := range cutoffs {
		if latest.IsZero() || cutoff.After(latest) {
			latest = cutoff
		}
	}
	rows, err := h.Queries.ListIssueProjectScopeMatureTrees(r.Context(), db.ListIssueProjectScopeMatureTreesParams{
		WorkspaceIds: scopeWorkspaceIDs(scopes), DaemonID: daemonID,
		CompletedBefore: pgtype.Timestamptz{Time: latest, Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list mature trees")
		return
	}
	work := make([]taskTreeWorkRow, 0, len(rows))
	for _, row := range rows {
		if !row.CompletedAt.Time.Before(cutoffs[uuidToString(row.WorkspaceID)]) {
			continue
		}
		work = append(work, taskTreeWorkRow{item: DaemonTaskTreeWorkItem{
			TreeID:      uuidToString(row.ID),
			IssueID:     uuidToString(row.IssueID),
			TaskKey:     taskTreeKey(row.IssuePrefix, row.WorkspaceName, row.IssueNumber),
			ProjectID:   uuidToString(row.ProjectID),
			ProjectName: row.ProjectName,
			BaseBranch:  row.BaseBranch,
			TaskBranch:  row.TaskBranch,
			Action:      row.Action,
			LocalPath:   row.LocalPath,
		}, workspaceID: row.WorkspaceID, projectID: row.ProjectID})
	}
	writeJSON(w, http.StatusOK, daemonTaskTreeWorkResponse{Items: visibleTaskTreeItems(work, scopes)})
}

func scopeWorkspaceIDs(scopes []daemonTaskTreeScope) []pgtype.UUID {
	ids := make([]pgtype.UUID, 0, len(scopes))
	for _, scope := range scopes {
		ids = append(ids, scope.workspaceID)
	}
	return ids
}

// taskTreeWorkRow is one listed tree with what places it: its workspace, for
// the caller's view of it, and its project.
type taskTreeWorkRow struct {
	item        DaemonTaskTreeWorkItem
	workspaceID pgtype.UUID
	projectID   pgtype.UUID
}

// visibleTaskTreeItems drops the trees of projects the caller cannot see.
func visibleTaskTreeItems(rows []taskTreeWorkRow, scopes []daemonTaskTreeScope) []DaemonTaskTreeWorkItem {
	hiddenByWorkspace := make(map[string]projectaccess.Hidden, len(scopes))
	for _, scope := range scopes {
		hiddenByWorkspace[uuidToString(scope.workspaceID)] = scope.hidden
	}
	items := make([]DaemonTaskTreeWorkItem, 0, len(rows))
	for _, row := range rows {
		if !hiddenByWorkspace[uuidToString(row.workspaceID)].CanSeeProject(row.projectID) {
			continue
		}
		items = append(items, row.item)
	}
	return items
}
