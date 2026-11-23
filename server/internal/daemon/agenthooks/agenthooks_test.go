package agenthooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Both clients connect directly: the default transport would send a request
// to a non-loopback address through an HTTP proxy environment variable.
var (
	testClient  = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	probeClient = &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
)

func newTestManager(t *testing.T, opts Options) *Manager {
	t.Helper()
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func mintToken(t *testing.T, m *Manager) string {
	t.Helper()
	token, err := m.MintToken()
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	return token
}

// hookReply is what the receiver answered one hook call with.
type hookReply struct {
	Status      int
	Body        string
	ContentType string
}

func postHookTo(client *http.Client, hostPort, authorization, payload string) (hookReply, error) {
	req, err := http.NewRequest(http.MethodPost, "http://"+hostPort+hookPath, strings.NewReader(payload))
	if err != nil {
		return hookReply{}, err
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := client.Do(req)
	if err != nil {
		return hookReply{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return hookReply{}, err
	}
	return hookReply{Status: resp.StatusCode, Body: string(body), ContentType: resp.Header.Get("Content-Type")}, nil
}

// postHook sends payload to the manager as one authenticated hook call.
func postHook(t *testing.T, m *Manager, authorization, payload string) hookReply {
	t.Helper()
	reply, err := postHookTo(testClient, fmt.Sprintf("127.0.0.1:%d", m.Port()), authorization, payload)
	if err != nil {
		t.Fatalf("post hook: %v", err)
	}
	return reply
}

func hookJSON(t *testing.T, event, session string, extra map[string]any) string {
	t.Helper()
	fields := map[string]any{"hook_event_name": event}
	if session != "" {
		fields["session_id"] = session
	}
	for key, value := range extra {
		fields[key] = value
	}
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode hook payload: %v", err)
	}
	return string(data)
}

// wantSession compares the manager's record with want, in which setTime
// stands for any set time.
func wantSession(t *testing.T, m *Manager, step string, want Session) {
	t.Helper()
	if want.LastEventAt.IsZero() {
		// A record exists only because an event was reported.
		want.LastEventAt = setTime
	}
	got, ok := m.Session(want.ID)
	if !ok {
		t.Fatalf("%s: session %q is not recorded", step, want.ID)
	}
	// Turn times belong to different turns, so each only has to fall
	// between the session's start and its last event.
	for _, at := range []time.Time{got.LastTurnStartedAt, got.LastTurnEndedAt} {
		if !at.IsZero() && (at.Before(got.StartedAt) || at.After(got.LastEventAt)) {
			t.Fatalf("%s: a turn time falls outside %s..%s: %+v", step, got.StartedAt, got.LastEventAt, got)
		}
	}
	for _, at := range []*time.Time{&got.StartedAt, &got.LastTurnStartedAt, &got.LastTurnEndedAt, &got.LastEventAt} {
		if !at.IsZero() {
			*at = setTime
		}
	}
	if got != want {
		t.Fatalf("%s: got %+v, want %+v", step, got, want)
	}
}

func wantIDs(t *testing.T, what string, sessions []Session, want ...string) {
	t.Helper()
	got := make([]string, 0, len(sessions))
	for _, session := range sessions {
		got = append(got, session.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

func TestManagerTracksSessionLifecycle(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	auth := "Bearer " + mintToken(t, m)
	turn := func(event, session string, extra map[string]any) {
		t.Helper()
		if reply := postHook(t, m, auth, hookJSON(t, event, session, extra)); reply.Status != http.StatusNoContent {
			t.Fatalf("%s %s: status %d, want %d", event, session, reply.Status, http.StatusNoContent)
		}
	}

	turn(eventSessionStart, "s1", map[string]any{"cwd": "/w", "transcript_path": "/t/s1.jsonl", "source": "startup"})
	seen := Session{ID: "s1", Cwd: "/w", TranscriptPath: "/t/s1.jsonl", State: StateIdle, StartedAt: setTime, LastEventAt: setTime}
	wantSession(t, m, "after SessionStart", seen)

	turn(eventUserPromptSubmit, "s1", map[string]any{"prompt": "summarize the diff"})
	running := seen
	running.State, running.LastTurnStartedAt = StateRunning, setTime
	wantSession(t, m, "after UserPromptSubmit", running)

	turn(eventStop, "s1", map[string]any{"stop_hook_active": true, "last_assistant_message": "still going"})
	wantSession(t, m, "after Stop with stop_hook_active", running)

	turn(eventStop, "s1", map[string]any{"last_assistant_message": "done"})
	idle := running
	idle.State, idle.LastTurnEndedAt = StateIdle, setTime
	wantSession(t, m, "after Stop", idle)

	// Turn times stay set across turns, so the earlier end is still reported.
	turn(eventUserPromptSubmit, "s1", map[string]any{"prompt": "again"})
	secondTurn := idle
	secondTurn.State = StateRunning
	wantSession(t, m, "after the second UserPromptSubmit", secondTurn)

	turn(eventStopFailure, "s1", map[string]any{
		"error": "rate_limit", "error_details": "429 Too Many Requests", "last_assistant_message": "API Error: Rate limit reached",
	})
	failed := idle
	failed.LastError = "API Error: Rate limit reached"
	wantSession(t, m, "after StopFailure", failed)

	latest, ok := m.LatestForDir("/w")
	if !ok || latest.ID != "s1" || latest.State != StateIdle {
		t.Fatalf("LatestForDir before SessionEnd: got %+v, %v; want an idle s1", latest, ok)
	}

	turn(eventSessionEnd, "s1", map[string]any{"reason": "clear"})
	ended := failed
	ended.State = StateEnded
	wantSession(t, m, "after SessionEnd", ended)
	if latest, ok := m.LatestForDir("/w"); ok {
		t.Fatalf("LatestForDir after SessionEnd: got %+v, want none", latest)
	}
	wantIDs(t, "live sessions after SessionEnd", m.LiveSessions())
}

func TestManagerAnswersWithoutOutput(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	auth := "Bearer " + mintToken(t, m)

	for _, event := range hookEvents[:] {
		payload := hookJSON(t, event, "s1", map[string]any{"cwd": "/w"})
		reply := postHook(t, m, auth, payload)
		if reply.Status != http.StatusNoContent {
			t.Errorf("%s: status %d, want %d", event, reply.Status, http.StatusNoContent)
		}
		// A decision or additionalContext in the reply would restrict or
		// alter the session.
		if reply.Body != "" || reply.ContentType != "" {
			t.Errorf("%s: replied with body %q and Content-Type %q, want no output", event, reply.Body, reply.ContentType)
		}
	}
}

func TestManagerRejectsUnmintedTokens(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	otherToken := mintToken(t, newTestManager(t, Options{}))
	payload := hookJSON(t, eventSessionStart, "s1", map[string]any{"cwd": "/w"})

	for _, tc := range []struct{ name, authorization string }{
		{"no Authorization header", ""},
		{"empty bearer", "Bearer "},
		{"unknown token", "Bearer not-a-minted-token"},
		{"another receiver's token", "Bearer " + otherToken},
		{"another receiver's token with a suffix", "Bearer " + otherToken + "x"},
		{"another scheme", "Basic " + otherToken},
	} {
		if reply := postHook(t, m, tc.authorization, payload); reply.Status != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want %d", tc.name, reply.Status, http.StatusUnauthorized)
		}
	}
	if session, ok := m.Session("s1"); ok {
		t.Fatalf("a rejected hook was recorded: %+v", session)
	}
	wantIDs(t, "live sessions after rejected hooks", m.LiveSessions())
}

func TestManagerAcceptsEveryMintedToken(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	for i := range 3 {
		auth := "Bearer " + mintToken(t, m)
		payload := hookJSON(t, eventSessionStart, fmt.Sprintf("s%d", i), nil)
		if reply := postHook(t, m, auth, payload); reply.Status != http.StatusNoContent {
			t.Errorf("token %d: status %d, want %d", i, reply.Status, http.StatusNoContent)
		}
	}
	wantIDs(t, "live sessions", m.LiveSessions(), "s2", "s1", "s0")
}

func TestManagerServesOnlyPostHook(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	auth := "Bearer " + mintToken(t, m)
	payload := hookJSON(t, eventSessionStart, "s1", nil)

	for _, tc := range []struct {
		name, method, path string
		want               int
	}{
		{"GET on the hook path", http.MethodGet, hookPath, http.StatusMethodNotAllowed},
		{"PUT on the hook path", http.MethodPut, hookPath, http.StatusMethodNotAllowed},
		{"POST on another path", http.MethodPost, "/", http.StatusNotFound},
	} {
		var body io.Reader
		if tc.method == http.MethodPost {
			body = strings.NewReader(payload)
		}
		req, err := http.NewRequest(tc.method, fmt.Sprintf("http://127.0.0.1:%d%s", m.Port(), tc.path), body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", auth)
		resp, err := testClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
	if session, ok := m.Session("s1"); ok {
		t.Fatalf("a request outside POST /hook was recorded: %+v", session)
	}
}

func TestManagerIgnoresUnknownEvent(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	auth := "Bearer " + mintToken(t, m)

	payload := hookJSON(t, "PreToolUse", "s1", map[string]any{"cwd": "/w"})
	if reply := postHook(t, m, auth, payload); reply.Status != http.StatusNoContent {
		t.Fatalf("unknown event: status %d, want %d", reply.Status, http.StatusNoContent)
	}
	if session, ok := m.Session("s1"); ok {
		t.Fatalf("an unknown event was recorded: %+v", session)
	}

	payload = hookJSON(t, eventUserPromptSubmit, "s1", map[string]any{"cwd": "/w"})
	if reply := postHook(t, m, auth, payload); reply.Status != http.StatusNoContent {
		t.Fatalf("UserPromptSubmit: status %d, want %d", reply.Status, http.StatusNoContent)
	}
	payload = hookJSON(t, "SomeFutureEvent", "s1", nil)
	if reply := postHook(t, m, auth, payload); reply.Status != http.StatusNoContent {
		t.Fatalf("unknown event on a known session: status %d, want %d", reply.Status, http.StatusNoContent)
	}
	wantSession(t, m, "after an unknown event", Session{
		ID: "s1", Cwd: "/w", State: StateRunning, StartedAt: setTime, LastTurnStartedAt: setTime,
	})
}

func TestManagerIgnoresUnknownFields(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	auth := "Bearer " + mintToken(t, m)
	payload := hookJSON(t, eventSessionStart, "s1", map[string]any{
		"cwd": "/w", "source": "startup", "agent_id": "a1", "permission_mode": "default", "hook_event_ids": []string{"x"},
	})
	if reply := postHook(t, m, auth, payload); reply.Status != http.StatusNoContent {
		t.Fatalf("status %d, want %d", reply.Status, http.StatusNoContent)
	}
	wantSession(t, m, "after a payload with unknown fields", Session{ID: "s1", Cwd: "/w", State: StateIdle, StartedAt: setTime})
}

func TestManagerRejectsUnusablePayload(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	auth := "Bearer " + mintToken(t, m)

	for _, tc := range []struct{ name, payload string }{
		{"empty body", ""},
		{"truncated JSON", `{"hook_event_name":`},
		{"not an object", `[1]`},
		{"lifecycle event without session_id", `{"hook_event_name":"SessionStart","cwd":"/w"}`},
		{"StopFailure without session_id", `{"hook_event_name":"StopFailure","error":"rate_limit"}`},
	} {
		if reply := postHook(t, m, auth, tc.payload); reply.Status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want %d", tc.name, reply.Status, http.StatusBadRequest)
		}
	}

	// An oversized body goes straight to the handler: the server stops reading
	// at the limit, which can reset the connection before a client reads the reply.
	payload := `{"hook_event_name":"SessionStart","session_id":"s1","pad":"` + strings.Repeat("a", hookPayloadLimit) + `"}`
	req := httptest.NewRequest(http.MethodPost, hookPath, strings.NewReader(payload))
	req.Header.Set("Authorization", auth)
	reply := httptest.NewRecorder()
	m.ServeHTTP(reply, req)
	if reply.Code != http.StatusBadRequest {
		t.Errorf("over the size limit: status %d, want %d", reply.Code, http.StatusBadRequest)
	}
	wantIDs(t, "live sessions after unusable payloads", m.LiveSessions())

	payload = hookJSON(t, "SomeFutureEvent", "", nil)
	if reply := postHook(t, m, auth, payload); reply.Status != http.StatusNoContent {
		t.Errorf("unknown event without session_id: status %d, want %d", reply.Status, http.StatusNoContent)
	}
}

func TestManagerHandlesConcurrentHooks(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	hostPort := fmt.Sprintf("127.0.0.1:%d", m.Port())
	const workers, sessionsPerWorker = 8, 25

	// Readers race with the writers.
	done := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			m.LiveSessions()
			m.LatestForDir("/w")
			m.Session("s-0-0")
		}
	}()

	var writers sync.WaitGroup
	for w := range workers {
		writers.Add(1)
		go func() {
			defer writers.Done()
			token, err := m.MintToken()
			if err != nil {
				t.Errorf("MintToken: %v", err)
				return
			}
			auth := "Bearer " + token
			for i := range sessionsPerWorker {
				id := fmt.Sprintf("s-%d-%d", w, i)
				for _, event := range []string{eventSessionStart, eventUserPromptSubmit, eventStopFailure} {
					reply, err := postHookTo(testClient, hostPort, auth, hookJSON(t, event, id, map[string]any{"cwd": "/w", "error": "server_error"}))
					if err != nil {
						t.Errorf("post %s for %s: %v", event, id, err)
						return
					}
					if reply.Status != http.StatusNoContent {
						t.Errorf("post %s for %s: status %d, want %d", event, id, reply.Status, http.StatusNoContent)
						return
					}
				}
				if session, ok := m.Session(id); !ok || session.State != StateIdle || session.Cwd != "/w" || session.LastError != "server_error" {
					t.Errorf("session %s is %+v, %v; want idle in /w with its error", id, session, ok)
					return
				}
			}
		}()
	}
	writers.Wait()
	close(done)
	readers.Wait()

	if got := len(m.LiveSessions()); got != workers*sessionsPerWorker {
		t.Errorf("%d live sessions, want %d", got, workers*sessionsPerWorker)
	}
}

func TestManagerIsReachableOnlyOnLoopback(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	if host, _, err := net.SplitHostPort(m.listener.Addr().String()); err != nil || host != "127.0.0.1" {
		t.Fatalf("listener is bound to %q, want 127.0.0.1", m.listener.Addr())
	}
	auth := "Bearer " + mintToken(t, m)
	payload := hookJSON(t, eventSessionStart, "s1", nil)
	if reply := postHook(t, m, auth, payload); reply.Status != http.StatusNoContent {
		t.Fatalf("through 127.0.0.1: status %d, want %d", reply.Status, http.StatusNoContent)
	}

	// Probes connect directly and in parallel: a tunnel interface can swallow
	// the connection instead of refusing it.
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	port := fmt.Sprint(m.Port())
	external := 0
	var probes sync.WaitGroup
	for _, addr := range addresses {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		external++
		probes.Add(1)
		go func() {
			defer probes.Done()
			reply, err := postHookTo(probeClient, net.JoinHostPort(ipNet.IP.String(), port), auth, payload)
			if err == nil && reply.Status == http.StatusNoContent {
				t.Errorf("a hook was accepted through %s, which is not loopback", ipNet.IP)
			}
		}()
	}
	probes.Wait()
	if external == 0 {
		t.Skip("no non-loopback address to probe")
	}
}

func TestManagerCloseStopsReceiving(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	auth := "Bearer " + mintToken(t, m)
	payload := hookJSON(t, eventSessionStart, "s1", nil)
	if reply := postHook(t, m, auth, payload); reply.Status != http.StatusNoContent {
		t.Fatalf("before Close: status %d, want %d", reply.Status, http.StatusNoContent)
	}

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := m.MintToken(); !errors.Is(err, ErrClosed) {
		t.Errorf("MintToken after Close returned %v, want ErrClosed", err)
	}
	if reply, err := postHookTo(testClient, fmt.Sprintf("127.0.0.1:%d", m.Port()), auth, payload); err == nil {
		t.Errorf("a hook call succeeded after Close with status %d", reply.Status)
	}
}
