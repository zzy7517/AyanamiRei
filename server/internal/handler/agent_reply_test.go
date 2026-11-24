package handler

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const agentReplyMissingIssueID = "00000000-0000-0000-0000-00000000dead"

func agentReplyPath(issueID string) string {
	return "/api/daemon/issues/" + issueID + "/agent-reply"
}

// reportAgentReply is what the test user's daemon says about an issue.
func reportAgentReply(issueID string, body any) *http.Request {
	return testutil.WithURLParams(newOwnerDaemonRequest(http.MethodPost, agentReplyPath(issueID), body), "issueId", issueID)
}

// markAgentReplySeen is a member saying they have seen the reply.
func reportReplyByMember(userID, issueID string) *http.Request {
	return withURLParam(scopeRequest(userID, http.MethodPost, "/api/issues/"+issueID+"/agent-reply", nil), "id", issueID)
}

func markAgentReplySeen(userID, issueID string) *http.Request {
	return withURLParam(scopeRequest(userID, http.MethodPost, "/api/issues/"+issueID+"/agent-reply/seen", nil), "id", issueID)
}

type agentReplyAnswer struct {
	AgentRepliedAt *string `json:"agent_replied_at"`
}

// storedAgentReply is the marker as the row of an issue holds it.
func storedAgentReply(t *testing.T, issueID string) *time.Time {
	t.Helper()
	var at *time.Time
	dbfx.QueryRow(t, `SELECT agent_replied_at FROM issue WHERE id = $1`, issueID).Scan(&at)
	return at
}

// wireAgentReply reads the marker off the wire.
func wireAgentReply(t *testing.T, at *string) *time.Time {
	t.Helper()
	if at == nil {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *at)
	if err != nil {
		t.Fatalf("agent_replied_at %q is not RFC 3339: %v", *at, err)
	}
	return &parsed
}

// agentReplyMoment reads a marker that must hold a moment.
func agentReplyMoment(t *testing.T, at *string) time.Time {
	t.Helper()
	parsed := wireAgentReply(t, at)
	if parsed == nil {
		t.Fatal("agent_replied_at is null, want a moment")
	}
	return *parsed
}

// wantAgentReply checks that the row of an issue and an answer carry the same
// marker.
func wantAgentReply(t *testing.T, issueID string, answered *string) {
	t.Helper()
	stored, wire := storedAgentReply(t, issueID), wireAgentReply(t, answered)
	if (stored == nil) != (wire == nil) || (stored != nil && !stored.Equal(*wire)) {
		t.Fatalf("the row holds %v and the answer says %v, want the same", stored, answered)
	}
}

// agentReplyHandler is the test handler with a bus of its own, which keeps the
// reply events it publishes.
func agentReplyHandler(t *testing.T) (h *Handler, published *[]events.Event) {
	t.Helper()
	local := *testHandler
	local.Bus = events.New()
	published = &[]events.Event{}
	local.Bus.SubscribeAll(func(event events.Event) {
		if event.Type == protocol.EventIssueAgentReplied {
			*published = append(*published, event)
		}
	})
	return &local, published
}

// agentReplyEvent is the n-th reply event, which must be about the issue and
// addressed to the workspace.
func agentReplyEvent(t *testing.T, published *[]events.Event, n int, issueID string) map[string]any {
	t.Helper()
	if n >= len(*published) {
		t.Fatalf("%d reply events, want at least %d", len(*published), n+1)
	}
	event := (*published)[n]
	payload, _ := event.Payload.(map[string]any)
	if event.WorkspaceID != testWorkspaceID || payload["issue_id"] != issueID {
		t.Fatalf("reply event %d is for workspace %q and issue %v, want %q and %q",
			n, event.WorkspaceID, payload["issue_id"], testWorkspaceID, issueID)
	}
	return payload
}

func TestDaemonReportSetsTheReplyMarkerForwardOnly(t *testing.T) {
	f := newProjectScopeFixture(t)
	issueID := dbfx.Issue(t, "Agent reply issue")
	h, published := agentReplyHandler(t)

	// Every success answers with the marker as it stands afterwards.
	reported := func(body map[string]any) time.Time {
		t.Helper()
		var answer agentReplyAnswer
		testutil.Call(t, h.DaemonReportIssueAgentReply, reportAgentReply(issueID, body)).Want(http.StatusOK).JSON(&answer)
		wantAgentReply(t, issueID, answer.AgentRepliedAt)
		return agentReplyMoment(t, answer.AgentRepliedAt)
	}

	// A report without a moment means now, and raises the marker.
	before := time.Now()
	now := reported(map[string]any{"daemon_id": projectScopeDaemonID})
	if now.Before(before.Add(-time.Second)) || now.After(time.Now()) {
		t.Fatalf("a report without a moment set the marker to %v, want about now", now)
	}
	if said, _ := agentReplyEvent(t, published, 0, issueID)["agent_replied_at"].(*string); said == nil || !agentReplyMoment(t, said).Equal(now) {
		t.Fatal("the event does not carry the moment the marker was set to")
	}

	// A report that is not newer changes nothing, and says what stands.
	for _, stale := range []string{"2026-01-02T03:04:05Z", now.Format(time.RFC3339Nano)} {
		if got := reported(map[string]any{"daemon_id": projectScopeDaemonID, "replied_at": stale}); !got.Equal(now) {
			t.Fatalf("a report of %s moved the marker to %v, want it to stay at %v", stale, got, now)
		}
	}
	if len(*published) != 1 {
		t.Fatalf("reports that were not newer published %d events, want none", len(*published)-1)
	}

	// A newer report moves it.
	newer := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	if got := reported(map[string]any{"daemon_id": projectScopeDaemonID, "replied_at": newer.Format(time.RFC3339Nano)}); !got.Equal(newer) {
		t.Fatalf("a newer report left the marker at %v, want %v", got, newer)
	}
	if said, _ := agentReplyEvent(t, published, 1, issueID)["agent_replied_at"].(*string); said == nil || !agentReplyMoment(t, said).Equal(newer) {
		t.Fatal("the event does not carry the moment the marker was moved to")
	}

	// What is not a moment, or not a daemon of the caller, changes nothing.
	for name, req := range map[string]*http.Request{
		"a moment that is not ISO 8601": reportAgentReply(issueID, map[string]any{"daemon_id": projectScopeDaemonID, "replied_at": "yesterday"}),
		"no daemon":                     reportAgentReply(issueID, map[string]any{"replied_at": newer.Format(time.RFC3339Nano)}),
		"no body":                       reportAgentReply(issueID, nil),
	} {
		if res := testutil.Call(t, h.DaemonReportIssueAgentReply, req); res.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", name, res.Code)
		}
	}
	outsider := f.member(t, "Agent reply other member", "member")
	dbfx.Runtime(t, "Agent reply other runtime", testutil.Cols{"daemon_id": "someone-elses-daemon", "owner_id": outsider})
	if res := testutil.Call(t, h.DaemonReportIssueAgentReply,
		reportAgentReply(issueID, map[string]any{"daemon_id": "someone-elses-daemon"})); res.Code != http.StatusNotFound {
		t.Fatalf("naming a daemon of another member: status %d, want 404", res.Code)
	}
	if stored := storedAgentReply(t, issueID); stored == nil || !stored.Equal(newer) || len(*published) != 2 {
		t.Fatalf("a refused report changed the marker to %v or published (%d events)", stored, len(*published))
	}

	// A daemon's token reports without naming a daemon; naming another is refused.
	dbfx.Runtime(t, "Agent reply token runtime", testutil.Cols{"daemon_id": "token-daemon"})
	tokenReport := func(body map[string]any) *testutil.Response {
		req := newDaemonTokenRequest(http.MethodPost, agentReplyPath(issueID), body, testWorkspaceID, "token-daemon")
		return testutil.Call(t, h.DaemonReportIssueAgentReply, testutil.WithURLParams(req, "issueId", issueID))
	}
	tokenTime := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Microsecond)
	var answer agentReplyAnswer
	tokenReport(map[string]any{"replied_at": tokenTime.Format(time.RFC3339Nano)}).Want(http.StatusOK).JSON(&answer)
	wantAgentReply(t, issueID, answer.AgentRepliedAt)
	if got := agentReplyMoment(t, answer.AgentRepliedAt); !got.Equal(tokenTime) || len(*published) != 3 {
		t.Fatalf("the token's report left the marker at %v (%d events), want %v and 3 events", got, len(*published), tokenTime)
	}
	if res := tokenReport(map[string]any{"daemon_id": projectScopeDaemonID}); res.Code != http.StatusForbidden {
		t.Fatalf("a token naming another daemon: status %d, want 403", res.Code)
	}
}

func TestIssueAgentReplySeenClearsTheMarker(t *testing.T) {
	newProjectScopeFixture(t)
	issueID := dbfx.Issue(t, "Agent reply seen issue")
	h, published := agentReplyHandler(t)
	replied := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)
	var answer agentReplyAnswer
	testutil.Call(t, h.DaemonReportIssueAgentReply, reportAgentReply(issueID, map[string]any{
		"daemon_id": projectScopeDaemonID, "replied_at": replied.Format(time.RFC3339Nano),
	})).Want(http.StatusOK).JSON(&answer)

	// The issue, and the rows the task lists are made of, carry the marker.
	var issue IssueResponse
	testutil.Call(t, h.GetIssue, withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID, nil), "id", issueID)).
		Want(http.StatusOK).JSON(&issue)
	at := pgtype.Timestamptz{Time: replied, Valid: true}
	for name, response := range map[string]IssueResponse{
		"the issue":              issue,
		"issueListRowToResponse": issueListRowToResponse(db.ListIssuesRow{AgentRepliedAt: at}, "HAN"),
		"openIssueRowToResponse": openIssueRowToResponse(db.ListOpenIssuesRow{AgentRepliedAt: at}, "HAN"),
	} {
		if response.AgentRepliedAt == nil {
			t.Fatalf("%s carries no marker, want %v", name, replied)
		}
		if got := agentReplyMoment(t, response.AgentRepliedAt); !got.Equal(replied) {
			t.Fatalf("%s says agent_replied_at is %v, want %v", name, response.AgentRepliedAt, replied)
		}
	}

	r := chi.NewRouter()
	RegisterAgentReplyRoutes(r, h)

	// A machine cannot say that a member saw the reply.
	machine := markAgentReplySeen(testUserID, issueID)
	machine.Header.Set("X-Actor-Source", "task_token")
	if res := testutil.Call(t, r.ServeHTTP, machine); res.Code != http.StatusForbidden || storedAgentReply(t, issueID) == nil {
		t.Fatalf("a machine marking the reply seen: status %d and marker %v, want 403 and the marker kept", res.Code, storedAgentReply(t, issueID))
	}

	testutil.Call(t, r.ServeHTTP, markAgentReplySeen(testUserID, issueID)).Want(http.StatusOK).JSON(&answer)
	if answer.AgentRepliedAt != nil || storedAgentReply(t, issueID) != nil {
		t.Fatalf("after seen the answer is %v and the row %v, want no marker", answer.AgentRepliedAt, storedAgentReply(t, issueID))
	}
	if event := agentReplyEvent(t, published, 1, issueID); event["agent_replied_at"] != nil {
		t.Fatalf("the event of a seen reply carries agent_replied_at %v, want null", event["agent_replied_at"])
	}

	// With nothing to clear it still answers, and says nothing new.
	testutil.Call(t, r.ServeHTTP, markAgentReplySeen(testUserID, issueID)).Want(http.StatusOK).JSON(&answer)
	if answer.AgentRepliedAt != nil || len(*published) != 2 {
		t.Fatalf("seen with nothing to clear answered %v and published %d events in all, want null and 2", answer.AgentRepliedAt, len(*published))
	}
}

func TestDaemonReportOfAReplyHidesWhatTheUserCannotSee(t *testing.T) {
	f := newProjectScopeFixture(t)
	// A private project is seen by its members only, whatever the workspace role.
	privateID := dbfx.Project(t, "Agent reply private project", testutil.Cols{"visibility": "private"})
	dbfx.InsertNoID(t, "project_member", testutil.Cols{"workspace_id": testWorkspaceID, "project_id": privateID, "user_id": testUserID},
		"project_id = $1 AND user_id = $2", privateID, testUserID)
	hiddenID := dbfx.Issue(t, "Agent reply issue in a private project", testutil.Cols{"project_id": privateID})
	publicID := dbfx.Issue(t, "Agent reply issue in a public project", testutil.Cols{"project_id": f.projectID})

	outsider := f.member(t, "Agent reply outsider", "member")
	dbfx.Runtime(t, "Agent reply outsider runtime", testutil.Cols{"daemon_id": "outsider-daemon", "owner_id": outsider})
	asOutsider := func(issueID string) int {
		req := newRequestAs(outsider, http.MethodPost, agentReplyPath(issueID), map[string]any{"daemon_id": "outsider-daemon"})
		return testutil.Call(t, testHandler.DaemonReportIssueAgentReply, testutil.WithURLParams(req, "issueId", issueID)).Code
	}
	if code := asOutsider(hiddenID); code != http.StatusNotFound || storedAgentReply(t, hiddenID) != nil {
		t.Fatalf("the daemon of a member outside the private project: status %d and marker %v, want 404 and none", code, storedAgentReply(t, hiddenID))
	}
	if code := asOutsider(publicID); code != http.StatusOK || storedAgentReply(t, publicID) == nil {
		t.Fatalf("the daemon of a member on a public issue: status %d and marker %v, want 200 and a marker", code, storedAgentReply(t, publicID))
	}

	// The test user is a member of the private project, so their daemon may
	// report there; an issue that does not exist answers 404.
	testutil.Call(t, testHandler.DaemonReportIssueAgentReply, reportAgentReply(hiddenID, map[string]any{"daemon_id": projectScopeDaemonID})).Want(http.StatusOK)
	if storedAgentReply(t, hiddenID) == nil {
		t.Fatal("the daemon of a member of the private project raised no marker")
	}
	testutil.Call(t, testHandler.DaemonReportIssueAgentReply, reportAgentReply(agentReplyMissingIssueID, map[string]any{"daemon_id": projectScopeDaemonID})).
		Want(http.StatusNotFound)
}

// TestAgentReplyRoutes checks each route reaches its handler through the
// router, which hands the issue over under the name each router uses.
func TestAgentReplyRoutes(t *testing.T) {
	r := chi.NewRouter()
	r.Route("/api/daemon", func(r chi.Router) { RegisterDaemonAgentReplyRoutes(r, testHandler) })
	RegisterAgentReplyRoutes(r, testHandler)

	for name, req := range map[string]*http.Request{
		"the daemon report": reportAgentReply(agentReplyMissingIssueID, map[string]any{"daemon_id": projectScopeDaemonID}),
		"the member report": reportReplyByMember(testUserID, agentReplyMissingIssueID),
		"the member seen":   markAgentReplySeen(testUserID, agentReplyMissingIssueID),
	} {
		res := testutil.Call(t, r.ServeHTTP, req)
		if res.Code != http.StatusNotFound || !strings.HasPrefix(strings.TrimSpace(res.Text()), "{") {
			t.Fatalf("%s: status %d %q, want the handler's 404 for an issue that does not exist", name, res.Code, res.Text())
		}
	}
}
