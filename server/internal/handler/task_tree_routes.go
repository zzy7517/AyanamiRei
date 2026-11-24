package handler

import (
	"github.com/go-chi/chi/v5"
)

// RegisterTaskTreeRoutes mounts the task-tree lifecycle routes of the
// workspace member group. Only a human may write: what happens to a tree
// happens on a member's machine, and its desktop app reports the outcome.
// The list of a workspace's tree work is mounted with the issue routes.
func RegisterTaskTreeRoutes(r chi.Router, h *Handler) {
	r.With(RequireHumanActor).Post("/api/issues/{id}/project-scope/{treeId}/rebuild", h.RebuildIssueProjectTree)
	r.With(RequireHumanActor).Post("/api/issues/{id}/project-scope/{treeId}/state", h.ReportIssueProjectTreeState)
}

// RegisterDaemonTaskTreeRoutes mounts the daemon's task-tree routes inside
// the daemon group, whose middleware authenticates the daemon.
func RegisterDaemonTaskTreeRoutes(r chi.Router, h *Handler) {
	r.Get("/issues/project-scope/work", h.DaemonListIssueProjectTreeWork)
	r.Get("/issues/project-scope/mature", h.DaemonListIssueProjectMatureTrees)
}
