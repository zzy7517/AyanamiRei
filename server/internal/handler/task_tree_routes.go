package handler

import (
	"github.com/go-chi/chi/v5"
)

// RegisterTaskTreeRoutes mounts the task-tree lifecycle routes of the
// workspace member group (task_sessions_v1: every handler answers 404 while
// the flag is off). Only a human may write: what happens to a tree happens on
// a member's machine.
func RegisterTaskTreeRoutes(r chi.Router, h *Handler) {
	r.With(RequireHumanActor).Delete("/api/issues/{id}/project-scope/{treeId}/tree", h.DeleteIssueProjectTree)
	r.With(RequireHumanActor).Post("/api/issues/{id}/project-scope/{treeId}/rebuild", h.RebuildIssueProjectTree)
}

// RegisterDaemonTaskTreeRoutes mounts the daemon's task-tree routes inside
// the daemon group, whose middleware authenticates the daemon.
func RegisterDaemonTaskTreeRoutes(r chi.Router, h *Handler) {
	r.Get("/issues/project-scope/work", h.DaemonListIssueProjectTreeWork)
	r.Get("/issues/project-scope/mature", h.DaemonListIssueProjectMatureTrees)
}
