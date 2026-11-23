package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/agenthooks"
)

// agentReplyRequest is one call the fake route was asked for.
type agentReplyRequest struct {
	method, path, daemonID, authorization string
}

// fakeAgentReply stands in for the server's agent-reply route: it keeps what
// it is asked and answers every request alike, with status after delay.
type fakeAgentReply struct {
	mu       sync.Mutex
	status   int
	delay    time.Duration
	requests []agentReplyRequest
}

func (f *fakeAgentReply) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DaemonID string `json:"daemon_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.requests = append(f.requests, agentReplyRequest{r.Method, r.URL.Path, body.DaemonID, r.Header.Get("Authorization")})
	status, delay := f.status, f.delay
	f.mu.Unlock()
	time.Sleep(delay)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprint(w, `{"agent_replied_at":"2026-11-22T10:00:00Z"}`)
}

func (f *fakeAgentReply) answer(status int, delay time.Duration) {
	f.mu.Lock()
	f.status, f.delay = status, delay
	f.mu.Unlock()
}

// request waits until the route has been asked for at least n times and
// returns the nth request.
func (f *fakeAgentReply) request(t *testing.T, n int) agentReplyRequest {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		got := len(f.requests)
		var last agentReplyRequest
		if got > 0 {
			last = f.requests[got-1]
		}
		f.mu.Unlock()
		if got >= n {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("the route was asked %d times, want at least %d", got, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fakeAgentReply) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// endTurn makes the hook receiver see session end a turn in dir.
func endTurn(t *testing.T, hooks *agenthooks.Manager, session, dir string) {
	t.Helper()
	token, err := hooks.MintToken()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	body := fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q,"cwd":%q}`, session, dir)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/hook", hooks.Port()), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post Stop hook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("post Stop hook: status %d", resp.StatusCode)
	}
}

// A turn that ends in an agent terminal is reported to the server for the
// issue of that terminal. What cannot be reported is logged and dropped, and
// the turns after it are reported all the same.
func TestReportTurnEndsTellsTheServerOfEachTurn(t *testing.T) {
	route := &fakeAgentReply{status: http.StatusOK}
	srv := httptest.NewServer(route)
	t.Cleanup(srv.Close)

	hooks, err := agenthooks.New(agenthooks.Options{Logger: quietLogger()})
	if err != nil {
		t.Fatalf("hook receiver: %v", err)
	}
	t.Cleanup(func() { _ = hooks.Close() })
	var logs bytes.Buffer
	d := &Daemon{
		cfg:        Config{ServerBaseURL: srv.URL, DaemonID: "daemon-1"},
		client:     NewClient(srv.URL),
		logger:     slog.New(slog.NewTextHandler(&logs, nil)),
		agentHooks: hooks,
	}
	d.client.SetToken("tok")
	tree := t.TempDir()
	hooks.MapIssue(tree, "issue-1")

	// A daemon without a hook receiver has nothing to report.
	bare := make(chan struct{})
	go func() {
		defer close(bare)
		new(Daemon).reportTurnEnds(context.Background())
	}()
	select {
	case <-bare:
	case <-time.After(time.Second):
		t.Fatal("without a hook receiver the reporting did not end")
	}

	agentReplyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { agentReplyTimeout = 5 * time.Second })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.reportTurnEnds(ctx)
	}()
	stop := func() {
		cancel()
		<-done
	}

	endTurn(t, hooks, "s1", tree)
	want := agentReplyRequest{method: "POST", path: "/api/daemon/issues/issue-1/agent-reply", daemonID: "daemon-1", authorization: "Bearer tok"}
	if got := route.request(t, 1); got != want {
		t.Fatalf("the route was asked %+v, want %+v", got, want)
	}

	// A turn that ends outside an agent terminal names no issue, so it is
	// not reported.
	endTurn(t, hooks, "s2", t.TempDir())
	time.Sleep(100 * time.Millisecond)
	if got := route.count(); got != 1 {
		t.Fatalf("the route was asked %d times, want 1", got)
	}

	// An issue the server no longer has is nothing left to tell.
	route.answer(http.StatusNotFound, 0)
	endTurn(t, hooks, "s3", tree)
	route.request(t, 2)

	// A server that fails is told once, not again.
	route.answer(http.StatusInternalServerError, 0)
	endTurn(t, hooks, "s4", tree)
	route.request(t, 3)
	time.Sleep(100 * time.Millisecond)
	if got := route.count(); got != 3 {
		t.Fatalf("the route was asked %d times after a failure, want 3: the report was retried", got)
	}

	// One that does not answer in time is given up, and the turn after it is
	// reported all the same.
	route.answer(http.StatusOK, 500*time.Millisecond)
	endTurn(t, hooks, "s5", tree)
	route.request(t, 4)
	route.answer(http.StatusOK, 0)
	endTurn(t, hooks, "s6", tree)
	route.request(t, 5)
	stop()

	var failures []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "a turn end was not reported") {
			failures = append(failures, line)
		}
	}
	// The failure and the timeout, for their issue; the 404 is not one.
	if len(failures) != 2 {
		t.Fatalf("the reported failures = %q, want two", failures)
	}
	for _, line := range failures {
		if !strings.Contains(line, "issue_id=issue-1") {
			t.Fatalf("a failure that does not name its issue: %q", line)
		}
	}
	if !strings.Contains(failures[0], "returned 500") || !strings.Contains(failures[1], "context deadline exceeded") {
		t.Fatalf("the failures do not tell the server's error from the timeout: %q", failures)
	}
}
