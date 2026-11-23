package handler

import (
	"github.com/go-chi/chi/v5"
)

// RegisterProjectScopeRoutes mounts the routes of a task's project + branch
// scope in the workspace member group. Only a human may replace the scope: it
// decides which repositories a task's agent runs get, so machine credentials
// are kept out.
func RegisterProjectScopeRoutes(r chi.Router, h *Handler) {
	r.Get("/api/issues/{id}/project-scope", h.GetIssueProjectScope)
	r.With(RequireHumanActor).Put("/api/issues/{id}/project-scope", h.PutIssueProjectScope)
}

// RegisterDaemonProjectScopeRoutes mounts the daemon's project + branch scope
// routes inside the daemon group, whose middleware authenticates the daemon.
func RegisterDaemonProjectScopeRoutes(r chi.Router, h *Handler) {
	r.Get("/issues/{issueId}/project-scope", h.DaemonGetIssueProjectScope)
	r.Post("/issues/{issueId}/project-scope/{treeId}/state", h.DaemonReportIssueProjectScopeState)
}
