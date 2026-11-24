package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The "new reply" marker of the task list. The agent of an issue ends a turn,
// whoever ran it reports that (a daemon, or the desktop app the agent's command
// line ran in), and the marker stays on the issue until a member says they saw
// the reply. It is a moment on the issue that only moves forward, until
// clearing it makes room for the next reply.

type daemonAgentReplyRequest struct {
	DaemonID string `json:"daemon_id"`
	// RepliedAt is when the turn ended, ISO 8601; now when empty.
	RepliedAt string `json:"replied_at"`
}

type agentReplyResponse struct {
	AgentRepliedAt *string `json:"agent_replied_at"`
}

// DaemonReportIssueAgentReply raises the marker of an issue. A report that is
// not newer than the marker changes nothing; either way the answer is the
// marker as it stands afterwards.
func (h *Handler) DaemonReportIssueAgentReply(w http.ResponseWriter, r *http.Request) {
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
	var req daemonAgentReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, ok := h.projectScopeDaemonID(w, r, strings.TrimSpace(req.DaemonID), workspaceID); !ok {
		return
	}
	repliedAt := time.Now()
	if req.RepliedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, req.RepliedAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "replied_at must be an ISO 8601 time")
			return
		}
		repliedAt = parsed
	}

	h.raiseIssueAgentReply(w, r, issueID, workspaceID, repliedAt)
}

// ReportIssueAgentReply raises the marker of an issue on a member's word: the
// desktop app that ran the agent's command line saw the agent end a turn. A
// report that is not newer than the marker changes nothing.
func (h *Handler) ReportIssueAgentReply(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	h.raiseIssueAgentReply(w, r, issue.ID, issue.WorkspaceID, time.Now())
}

// raiseIssueAgentReply moves the marker of an issue to repliedAt, when that is
// newer than what stands, tells the workspace if it moved, and answers with the
// marker as it stands afterwards.
func (h *Handler) raiseIssueAgentReply(
	w http.ResponseWriter,
	r *http.Request,
	issueID, workspaceID pgtype.UUID,
	repliedAt time.Time,
) {
	stored, err := h.Queries.SetIssueAgentRepliedAt(r.Context(), db.SetIssueAgentRepliedAtParams{
		AgentRepliedAt: pgtype.Timestamptz{Time: repliedAt, Valid: true},
		ID:             issueID,
		WorkspaceID:    workspaceID,
	})
	moved := err == nil
	if !moved {
		if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to set agent reply")
			return
		}
		// The report is not newer than the marker.
		if stored, err = h.Queries.GetIssueAgentRepliedAt(r.Context(), db.GetIssueAgentRepliedAtParams{
			ID: issueID, WorkspaceID: workspaceID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "issue not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to load agent reply")
			return
		}
	}
	if moved {
		actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(workspaceID))
		h.publish(protocol.EventIssueAgentReplied, uuidToString(workspaceID), actorType, actorID, map[string]any{
			"issue_id": uuidToString(issueID), "agent_replied_at": timestampToNanoPtr(stored),
		})
	}
	writeJSON(w, http.StatusOK, agentReplyResponse{AgentRepliedAt: timestampToNanoPtr(stored)})
}

// MarkIssueAgentReplySeen clears the marker of an issue: a member has seen the
// reply.
func (h *Handler) MarkIssueAgentReplySeen(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	cleared, err := h.Queries.ClearIssueAgentRepliedAt(r.Context(), db.ClearIssueAgentRepliedAtParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear agent reply")
		return
	}
	if cleared > 0 {
		actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(issue.WorkspaceID))
		h.publish(protocol.EventIssueAgentReplied, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{
			"issue_id": uuidToString(issue.ID), "agent_replied_at": nil,
		})
	}
	writeJSON(w, http.StatusOK, agentReplyResponse{})
}

// RegisterDaemonAgentReplyRoutes registers what a daemon reports about the
// replies of an issue's agent, on the daemon-authenticated router.
func RegisterDaemonAgentReplyRoutes(r chi.Router, h *Handler) {
	r.Post("/issues/{issueId}/agent-reply", h.DaemonReportIssueAgentReply)
}

// RegisterAgentReplyRoutes registers what a member says about an agent's
// reply, on the workspace member router: that there is one, and that it was
// seen. Neither is a machine's to say.
func RegisterAgentReplyRoutes(r chi.Router, h *Handler) {
	r.With(RequireHumanActor).Post("/api/issues/{id}/agent-reply", h.ReportIssueAgentReply)
	r.With(RequireHumanActor).Post("/api/issues/{id}/agent-reply/seen", h.MarkIssueAgentReplySeen)
}
