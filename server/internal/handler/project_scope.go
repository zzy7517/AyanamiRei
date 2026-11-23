package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/projectaccess"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The "project + branch" pairs a task (issue) works on: the ones its author
// chose for it, each with the tree a daemon builds for it and reports about.

// States of the tree a daemon builds for a task's pair, as the daemon reports them.
const (
	projectTreePending = "pending"
	projectTreeReady   = "ready"
	projectTreeFailed  = "failed"
	projectTreeDeleted = "deleted"
)

const (
	// maxProjectScopeItems caps one list of pairs.
	maxProjectScopeItems = 50
	// maxProjectScopeErrorLength caps the error a daemon reports for a tree.
	maxProjectScopeErrorLength = 500
	// maxProjectScopePathLength caps the tree path a daemon reports.
	maxProjectScopePathLength = 4096
)

const projectScopeReadyRemoval = "a pair whose tree is ready cannot be removed; delete the tree first"

// projectScopeLayoutChange refuses a replace that moves a tree on disk: a
// task with one tree cannot gain or lose it, and one with several cannot be
// left with a single tree, while its trees exist.
const projectScopeLayoutChange = "a task with one tree cannot gain or lose a pair, and one with several trees cannot be left with one; delete its trees first"

// IssueProjectTree is what the daemon reported about the tree built for one pair.
type IssueProjectTree struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	Path       string `json:"path"`
	TaskBranch string `json:"task_branch"`
	Error      string `json:"error"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// ProjectScopeItem is one pair a task works on. Tree is what the daemon last
// reported about the tree built for the pair.
type ProjectScopeItem struct {
	ProjectID  string            `json:"project_id"`
	BaseBranch string            `json:"base_branch"`
	Position   int32             `json:"position"`
	Tree       *IssueProjectTree `json:"tree"`
}

type IssueProjectScopeResponse struct {
	Items []ProjectScopeItem `json:"items"`
}

type issueProjectScopeItemInput struct {
	ProjectID  string `json:"project_id"`
	BaseBranch string `json:"base_branch"`
}

// scopeItemInput is one pair of a replace request, before validation.
type scopeItemInput struct {
	projectID string
	branch    string
}

type projectScopePair struct {
	projectID pgtype.UUID
	branch    string
}

// decodeProjectScopeBody reads a replace request into req, a struct whose
// items field is a pointer to a slice. A body without "items" leaves it nil:
// reading that as an empty list would clear a scope the caller meant to change.
func decodeProjectScopeBody(w http.ResponseWriter, r *http.Request, req any) bool {
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func parseProjectScopePairs(w http.ResponseWriter, items []scopeItemInput) ([]projectScopePair, bool) {
	if len(items) > maxProjectScopeItems {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("a project scope holds at most %d pairs", maxProjectScopeItems))
		return nil, false
	}
	pairs := make([]projectScopePair, 0, len(items))
	seen := make(map[projectScopePair]struct{}, len(items))
	for _, item := range items {
		projectID, ok := parseUUIDOrBadRequest(w, item.projectID, "project_id")
		if !ok {
			return nil, false
		}
		if item.branch == "" {
			writeError(w, http.StatusBadRequest, "branch is required")
			return nil, false
		}
		if err := validateGitRef(item.branch); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return nil, false
		}
		pair := projectScopePair{projectID: projectID, branch: item.branch}
		if _, duplicate := seen[pair]; duplicate {
			writeError(w, http.StatusBadRequest, "a project appears twice with the same branch")
			return nil, false
		}
		seen[pair] = struct{}{}
		pairs = append(pairs, pair)
	}
	return pairs, true
}

func (h *Handler) beginProjectScopeWrite(w http.ResponseWriter, r *http.Request) (pgx.Tx, *db.Queries, bool) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return nil, nil, false
	}
	return tx, h.Queries.WithTx(tx), true
}

// lockProjectScopeProjects checks that every pair's project is a project of
// the workspace the caller may see, and locks it FOR KEY SHARE so a project
// deletion cannot sweep the pairs this transaction is about to write. A
// project the caller may not see answers like a missing one.
func (h *Handler) lockProjectScopeProjects(w http.ResponseWriter, r *http.Request, qtx *db.Queries, workspaceID pgtype.UUID, pairs []projectScopePair) bool {
	notFound := func() bool {
		writeError(w, http.StatusBadRequest, "project not found in this workspace")
		return false
	}
	distinct := make([]pgtype.UUID, 0, len(pairs))
	byID := make(map[pgtype.UUID]struct{}, len(pairs))
	for _, pair := range pairs {
		if _, seen := byID[pair.projectID]; seen {
			continue
		}
		byID[pair.projectID] = struct{}{}
		distinct = append(distinct, pair.projectID)
	}
	if len(distinct) == 0 {
		return true
	}
	found, err := qtx.ListWorkspaceProjectIDs(r.Context(), db.ListWorkspaceProjectIDsParams{
		WorkspaceID: workspaceID, ProjectIds: distinct,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check projects")
		return false
	}
	if len(found) != len(distinct) {
		return notFound()
	}
	hidden, ok := h.requestHiddenProjects(w, r, uuidToString(workspaceID))
	if !ok {
		return false
	}
	for _, id := range distinct {
		if !hidden.CanSeeProject(id) {
			return notFound()
		}
	}
	for _, id := range distinct {
		if _, err := qtx.LockProjectForChatSessionCreate(r.Context(), db.LockProjectForChatSessionCreateParams{
			ID: id, WorkspaceID: workspaceID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound()
			}
			writeError(w, http.StatusInternalServerError, "failed to lock project")
			return false
		}
	}
	return true
}

func issueProjectTree(row db.IssueProjectScope) *IssueProjectTree {
	return &IssueProjectTree{
		ID:         uuidToString(row.ID),
		State:      row.State,
		Path:       row.Path,
		TaskBranch: row.TaskBranch,
		Error:      row.Error,
		CreatedAt:  timestampToString(row.CreatedAt),
		UpdatedAt:  timestampToString(row.UpdatedAt),
	}
}

func projectScopeItem(row db.IssueProjectScope) ProjectScopeItem {
	return ProjectScopeItem{
		ProjectID:  uuidToString(row.ProjectID),
		BaseBranch: row.BaseBranch,
		Position:   row.Position,
		Tree:       issueProjectTree(row),
	}
}

// projectScopeItems lists a task's pairs the request may see, in order. Pairs
// it may not see do not exist for it.
func projectScopeItems(rows []db.IssueProjectScope, hidden projectaccess.Hidden) []ProjectScopeItem {
	items := make([]ProjectScopeItem, 0, len(rows))
	for _, row := range rows {
		if hidden.CanSeeProject(row.ProjectID) {
			items = append(items, projectScopeItem(row))
		}
	}
	return items
}

func (h *Handler) GetIssueProjectScope(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	hidden, ok := h.requestHiddenProjects(w, r, uuidToString(issue.WorkspaceID))
	if !ok {
		return
	}
	rows, err := h.Queries.ListIssueProjectScopes(r.Context(), db.ListIssueProjectScopesParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue project scope")
		return
	}
	writeJSON(w, http.StatusOK, IssueProjectScopeResponse{Items: projectScopeItems(rows, hidden)})
}

// PutIssueProjectScope replaces a task's pairs with the request's, in its
// order. A pair the new list keeps keeps its tree and the state reported for
// it; a new one starts pending. Removing a pair whose tree is ready is
// refused: that tree exists on disk until the daemon deletes it.
func (h *Handler) PutIssueProjectScope(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req struct {
		Items *[]issueProjectScopeItemInput `json:"items"`
	}
	if !decodeProjectScopeBody(w, r, &req) {
		return
	}
	if req.Items == nil {
		writeError(w, http.StatusBadRequest, "items is required")
		return
	}
	inputs := make([]scopeItemInput, 0, len(*req.Items))
	for _, item := range *req.Items {
		inputs = append(inputs, scopeItemInput{projectID: item.ProjectID, branch: item.BaseBranch})
	}
	pairs, ok := parseProjectScopePairs(w, inputs)
	if !ok {
		return
	}
	kept := make(map[projectScopePair]struct{}, len(pairs))
	for _, pair := range pairs {
		kept[pair] = struct{}{}
	}

	tx, qtx, ok := h.beginProjectScopeWrite(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := qtx.LockIssueForProjectScopeWrite(r.Context(), db.LockIssueForProjectScopeWriteParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock issue")
		return
	}
	if !h.lockProjectScopeProjects(w, r, qtx, issue.WorkspaceID, pairs) {
		return
	}
	existing, err := qtx.ListIssueProjectScopes(r.Context(), db.ListIssueProjectScopesParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue project scope")
		return
	}
	var removed []pgtype.UUID
	for _, row := range existing {
		if _, keep := kept[projectScopePair{projectID: row.ProjectID, branch: row.BaseBranch}]; keep {
			continue
		}
		if row.State == projectTreeReady {
			writeError(w, http.StatusConflict, projectScopeReadyRemoval)
			return
		}
		removed = append(removed, row.ID)
	}
	// The layout on disk depends on how many trees a task has: with one, the
	// task directory is that worktree; with more, each tree is a subdirectory
	// on another branch name. So a task whose trees exist cannot cross from
	// one to several or back: the trees are deleted first, and then the list
	// is free again.
	existingTrees := 0
	for _, row := range existing {
		if row.State != projectTreeDeleted {
			existingTrees++
		}
	}
	if existingTrees > 0 && (existingTrees == 1) != (len(pairs) == 1) {
		writeErrorCode(w, http.StatusConflict, codeTreeLayoutChange, projectScopeLayoutChange)
		return
	}
	if len(removed) > 0 {
		// The state guard makes a tree that turned ready after the read above
		// survive the delete; fewer deletions than removals means re-reading
		// to tell that tree from one another transaction already deleted.
		deleted, err := qtx.DeleteIssueProjectScopesReadyGuarded(r.Context(), db.DeleteIssueProjectScopesReadyGuardedParams{
			Ids: removed, IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to replace issue project scope")
			return
		}
		if int(deleted) != len(removed) {
			remaining, err := qtx.ListIssueProjectScopes(r.Context(), db.ListIssueProjectScopesParams{
				IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to replace issue project scope")
				return
			}
			stillRemoved := make(map[pgtype.UUID]struct{}, len(removed))
			for _, id := range removed {
				stillRemoved[id] = struct{}{}
			}
			for _, row := range remaining {
				if _, gone := stillRemoved[row.ID]; !gone {
					continue
				}
				writeError(w, http.StatusConflict, projectScopeReadyRemoval)
				return
			}
		}
	}
	for i, pair := range pairs {
		if _, err := qtx.UpsertIssueProjectScope(r.Context(), db.UpsertIssueProjectScopeParams{
			WorkspaceID: issue.WorkspaceID,
			IssueID:     issue.ID,
			ProjectID:   pair.projectID,
			BaseBranch:  pair.branch,
			Position:    int32(i),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to replace issue project scope")
			return
		}
	}
	rows, err := qtx.ListIssueProjectScopes(r.Context(), db.ListIssueProjectScopesParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue project scope")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return
	}
	writeJSON(w, http.StatusOK, IssueProjectScopeResponse{Items: projectScopeItems(rows, projectaccess.Hidden{})})
}
