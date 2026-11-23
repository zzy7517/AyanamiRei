package agenthooks

import (
	"testing"
	"time"
)

// setTime stands in for any set time when a real clock produced it.
var setTime = time.Unix(1, 0)

type fakeClock struct {
	now time.Time
}

type tableFixture struct {
	t     *testing.T
	tbl   *table
	start time.Time
	clock *fakeClock
}

func newTableFixture(t *testing.T, maxSize int, ttl time.Duration) *tableFixture {
	t.Helper()
	t.Parallel()
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return &tableFixture{
		t:     t,
		tbl:   newTable(func() time.Time { return clock.now }, maxSize, ttl),
		start: clock.now,
		clock: clock,
	}
}

func (f *tableFixture) advance(d time.Duration) time.Time {
	f.clock.now = f.clock.now.Add(d)
	return f.clock.now
}

func (f *tableFixture) record(p hookPayload) {
	f.tbl.record(p)
}

func (f *tableFixture) wantSession(step string, want Session) {
	f.t.Helper()
	got, ok := f.tbl.get(want.ID)
	if !ok {
		f.t.Fatalf("%s: session %q is gone, want %+v", step, want.ID, want)
	}
	if got != want {
		f.t.Fatalf("%s: got %+v, want %+v", step, got, want)
	}
}

func (f *tableFixture) wantMissing(step, id string) {
	f.t.Helper()
	if got, ok := f.tbl.get(id); ok {
		f.t.Fatalf("%s: session %q is still %+v, want it dropped", step, id, got)
	}
}

func (f *tableFixture) wantLatest(step, dir, wantID string) {
	f.t.Helper()
	got, ok := f.tbl.latestForDir(dir)
	switch {
	case wantID == "" && ok:
		f.t.Fatalf("%s: LatestForDir(%q) returned %q, want none", step, dir, got.ID)
	case wantID != "" && (!ok || got.ID != wantID):
		f.t.Fatalf("%s: LatestForDir(%q) returned %q, %v; want %q", step, dir, got.ID, ok, wantID)
	}
}

func TestTableTracksTurns(t *testing.T) {
	f := newTableFixture(t, 0, 0)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "s1", Cwd: "/w/", TranscriptPath: "/t/s1.jsonl", Source: "startup"})
	want := Session{
		ID: "s1", Cwd: "/w", TranscriptPath: "/t/s1.jsonl", State: StateIdle, StartedAt: f.start, LastEventAt: f.start,
	}
	f.wantSession("after SessionStart", want)

	at := f.advance(time.Minute)
	f.record(hookPayload{Event: eventUserPromptSubmit, SessionID: "s1"})
	want.State, want.LastTurnStartedAt, want.LastEventAt = StateRunning, at, at
	f.wantSession("after UserPromptSubmit", want)

	at = f.advance(time.Minute)
	f.record(hookPayload{Event: eventStop, SessionID: "s1"})
	want.State, want.LastTurnEndedAt, want.LastEventAt = StateIdle, at, at
	f.wantSession("after Stop", want)

	at = f.advance(time.Minute)
	f.record(hookPayload{Event: eventSessionEnd, SessionID: "s1"})
	want.State, want.LastEventAt = StateEnded, at
	f.wantSession("after SessionEnd", want)
}

func TestTableFollowsLatestReportedPath(t *testing.T) {
	f := newTableFixture(t, 0, 0)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "s1", Cwd: "/w", TranscriptPath: "/t/s1.jsonl", Source: "startup"})
	f.record(hookPayload{Event: eventUserPromptSubmit, SessionID: "s1", Cwd: "/elsewhere"})
	f.record(hookPayload{Event: eventStop, SessionID: "s1"})
	f.wantSession("after a later cwd and events without one", Session{
		ID: "s1", Cwd: "/elsewhere", TranscriptPath: "/t/s1.jsonl", State: StateIdle,
		StartedAt: f.start, LastTurnStartedAt: f.start, LastTurnEndedAt: f.start, LastEventAt: f.start,
	})
}

func TestTableStopWithStopHookActiveEndsNoTurn(t *testing.T) {
	f := newTableFixture(t, 0, 0)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "s1", Source: "startup"})
	want := Session{ID: "s1", State: StateIdle, StartedAt: f.start, LastEventAt: f.start}
	f.record(hookPayload{Event: eventStop, SessionID: "s1", StopHookActive: true})
	f.wantSession("while idle", want)

	at := f.advance(time.Minute)
	f.record(hookPayload{Event: eventUserPromptSubmit, SessionID: "s1"})
	want.State, want.LastTurnStartedAt, want.LastEventAt = StateRunning, at, at
	f.record(hookPayload{Event: eventStop, SessionID: "s1", StopHookActive: true})
	f.wantSession("while running", want)

	at = f.advance(time.Minute)
	f.record(hookPayload{Event: eventStop, SessionID: "s1"})
	want.State, want.LastTurnEndedAt, want.LastEventAt = StateIdle, at, at
	// The ignored Stop is not a report either: LastEventAt stays where it is.
	f.advance(time.Minute)
	f.record(hookPayload{Event: eventStop, SessionID: "s1", StopHookActive: true})
	f.wantSession("after the turn ended", want)

	f.record(hookPayload{Event: eventStop, SessionID: "unseen", StopHookActive: true})
	f.wantMissing("from an unseen session", "unseen")
}

func TestTableStopFailureEndsTurnAndRecordsError(t *testing.T) {
	f := newTableFixture(t, 0, 0)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "s1", Source: "startup"})
	turnStart := f.advance(time.Minute)
	f.record(hookPayload{Event: eventUserPromptSubmit, SessionID: "s1"})
	failed := f.advance(time.Minute)
	f.record(hookPayload{Event: eventStopFailure, SessionID: "s1", Error: "rate_limit", ErrorDetails: "429 Too Many Requests", LastAssistantMessage: "API Error: Rate limit reached"})
	f.wantSession("after StopFailure", Session{
		ID: "s1", State: StateIdle, StartedAt: f.start, LastTurnStartedAt: turnStart,
		LastTurnEndedAt: failed, LastEventAt: failed, LastError: "API Error: Rate limit reached",
	})

	// The error describes a turn only until a later one starts.
	retry := f.advance(time.Minute)
	f.record(hookPayload{Event: eventUserPromptSubmit, SessionID: "s1"})
	f.wantSession("after the next turn started", Session{
		ID: "s1", State: StateRunning, StartedAt: f.start, LastTurnStartedAt: retry, LastTurnEndedAt: failed,
		LastEventAt: retry,
	})

	// ...or ends.
	stopped := f.advance(time.Minute)
	f.record(hookPayload{Event: eventStopFailure, SessionID: "s1", Error: "server_error"})
	f.wantSession("after a StopFailure without details", Session{
		ID: "s1", State: StateIdle, StartedAt: f.start, LastTurnStartedAt: retry,
		LastTurnEndedAt: stopped, LastEventAt: stopped, LastError: "server_error",
	})
	f.record(hookPayload{Event: eventStop, SessionID: "s1"})
	f.wantSession("after the next turn ended", Session{
		ID: "s1", State: StateIdle, StartedAt: f.start, LastTurnStartedAt: retry, LastTurnEndedAt: stopped,
		LastEventAt: stopped,
	})
}

func TestStopFailureErrorText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		payload hookPayload
		want    string
	}{
		{
			name:    "the text the conversation showed",
			payload: hookPayload{Error: "rate_limit", ErrorDetails: "429 Too Many Requests", LastAssistantMessage: "API Error: Rate limit reached"},
			want:    "API Error: Rate limit reached",
		},
		{name: "the type with its details", payload: hookPayload{Error: "server_error", ErrorDetails: "upstream reset"}, want: "server_error: upstream reset"},
		{name: "the type alone", payload: hookPayload{Error: "billing_error"}, want: "billing_error"},
		{name: "the details alone", payload: hookPayload{ErrorDetails: "quota"}, want: "quota"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.payload.errorText(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTableSessionStartKeepsRunningTurn(t *testing.T) {
	f := newTableFixture(t, 0, 0)
	prompt := f.advance(time.Minute)
	f.record(hookPayload{Event: eventUserPromptSubmit, SessionID: "s1"})
	started := f.advance(time.Second)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "s1", Source: "startup"})
	f.wantSession("when UserPromptSubmit overtakes SessionStart", Session{
		ID: "s1", State: StateRunning, StartedAt: started, LastTurnStartedAt: prompt, LastEventAt: started,
	})

	compacted := f.advance(time.Minute)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "s1", Source: "compact"})
	f.wantSession("when compaction reports SessionStart mid-turn", Session{
		ID: "s1", State: StateRunning, StartedAt: started, LastTurnStartedAt: prompt, LastEventAt: compacted,
	})
}

func TestTableSessionStartRestartsEndedSessionBySource(t *testing.T) {
	for _, tc := range []struct {
		source string
		resets bool
	}{
		{source: "startup", resets: true},
		{source: "resume", resets: true},
		{source: "clear", resets: true},
		{source: "compact", resets: false},
		{source: "", resets: false},
		{source: "future-source", resets: false},
	} {
		t.Run(tc.source, func(t *testing.T) {
			f := newTableFixture(t, 0, 0)
			f.record(hookPayload{Event: eventSessionStart, SessionID: "s1", Source: "startup"})
			f.record(hookPayload{Event: eventSessionEnd, SessionID: "s1"})
			restarted := f.advance(time.Hour)
			f.record(hookPayload{Event: eventSessionStart, SessionID: "s1", Source: tc.source})

			startedAt := f.start
			if tc.resets {
				startedAt = restarted
			}
			f.wantSession("after SessionEnd then SessionStart", Session{
				ID: "s1", State: StateIdle, StartedAt: startedAt, LastEventAt: restarted,
			})
		})
	}
}

func TestTableRecordsSessionsWithoutSessionStart(t *testing.T) {
	f := newTableFixture(t, 0, 0)
	seen := f.advance(time.Minute)
	f.record(hookPayload{Event: eventUserPromptSubmit, SessionID: "s1", Cwd: "/w"})
	f.wantSession("after UserPromptSubmit from an unseen session", Session{
		ID: "s1", Cwd: "/w", State: StateRunning, StartedAt: seen, LastTurnStartedAt: seen, LastEventAt: seen,
	})
	f.record(hookPayload{Event: eventStopFailure, SessionID: "s2", Error: "unknown"})
	f.wantSession("after StopFailure from an unseen session", Session{
		ID: "s2", State: StateIdle, StartedAt: seen, LastTurnEndedAt: seen, LastEventAt: seen, LastError: "unknown",
	})
	f.record(hookPayload{Event: eventSessionEnd, SessionID: "s3"})
	f.wantSession("after SessionEnd from an unseen session", Session{ID: "s3", State: StateEnded, StartedAt: seen, LastEventAt: seen})
}

func TestTableLatestForDirPrefersNewestLiveSession(t *testing.T) {
	f := newTableFixture(t, 0, 0)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "old", Cwd: "/w", Source: "startup"})
	f.advance(time.Hour)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "newer", Cwd: "/w", Source: "startup"})
	f.advance(time.Hour)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "elsewhere", Cwd: "/other", Source: "startup"})

	f.wantLatest("with two live sessions", "/w", "newer")
	f.wantLatest("with a trailing slash", "/w/", "newer")
	f.wantLatest("for another dir", "/other", "elsewhere")
	f.wantLatest("for an unknown dir", "/missing", "")
	f.wantLatest("for an empty dir", "", "")

	f.record(hookPayload{Event: eventSessionEnd, SessionID: "newer"})
	f.wantLatest("after the newer session ended", "/w", "old")

	f.advance(time.Hour)
	f.record(hookPayload{Event: eventUserPromptSubmit, SessionID: "latest", Cwd: "/w"})
	f.wantLatest("after a later session reported", "/w", "latest")

	f.advance(time.Hour)
	f.record(hookPayload{Event: eventUserPromptSubmit, SessionID: "old"})
	f.wantLatest("when the older session is active more recently", "/w", "latest")
}

func TestTableLiveSessionsNewestFirst(t *testing.T) {
	f := newTableFixture(t, 0, 0)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "first", Source: "startup"})
	f.advance(time.Hour)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "second", Source: "startup"})
	f.advance(time.Hour)
	f.record(hookPayload{Event: eventSessionEnd, SessionID: "second"})
	f.advance(time.Hour)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "third", Source: "startup"})
	wantIDs(t, "live sessions", f.tbl.live(), "third", "first")

	for _, id := range []string{"first", "third"} {
		f.record(hookPayload{Event: eventSessionEnd, SessionID: id})
	}
	wantIDs(t, "live sessions after all ended", f.tbl.live())
}

func TestTableExpiresSilentSessions(t *testing.T) {
	f := newTableFixture(t, 0, time.Hour)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "quiet", Source: "startup"})
	f.advance(30 * time.Minute)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "busy", Source: "startup"})
	started := f.start.Add(30 * time.Minute)

	f.advance(time.Hour)
	f.wantMissing("after an hour and a half without hooks", "quiet")
	f.wantSession("exactly at the TTL", Session{ID: "busy", State: StateIdle, StartedAt: started, LastEventAt: started})
	f.record(hookPayload{Event: eventUserPromptSubmit, SessionID: "busy"})

	// A session that keeps reporting outlives the TTL counted from its start.
	var lastTurn time.Time
	for range 12 {
		lastTurn = f.advance(30 * time.Minute)
		f.record(hookPayload{Event: eventUserPromptSubmit, SessionID: "busy"})
	}
	f.wantSession("seven hours after it started", Session{
		ID: "busy", State: StateRunning, StartedAt: started, LastTurnStartedAt: lastTurn, LastEventAt: lastTurn,
	})

	// An expired session that reports again is recorded anew.
	again := f.advance(time.Hour)
	f.record(hookPayload{Event: eventSessionEnd, SessionID: "quiet"})
	f.wantSession("after an expired session reported", Session{
		ID: "quiet", State: StateEnded, StartedAt: again, LastEventAt: again,
	})
}

func TestTableEvictsLeastRecentlySeenWhenFull(t *testing.T) {
	f := newTableFixture(t, 2, 0)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "first", Source: "startup"})
	second := f.advance(time.Minute)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "second", Source: "startup"})

	third := f.advance(time.Minute)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "third", Source: "startup"})
	f.wantMissing("when a third live session arrived", "first")
	f.wantSession("while at the cap", Session{ID: "second", State: StateIdle, StartedAt: second, LastEventAt: second})
	f.wantSession("while at the cap", Session{ID: "third", State: StateIdle, StartedAt: third, LastEventAt: third})

	f.advance(time.Minute)
	f.record(hookPayload{Event: eventSessionEnd, SessionID: "third"})
	fourth := f.advance(time.Minute)
	f.record(hookPayload{Event: eventSessionStart, SessionID: "fourth", Source: "startup"})
	f.wantMissing("when an ended session was seen before a live one", "third")
	f.wantSession("after the ended session was evicted", Session{ID: "second", State: StateIdle, StartedAt: second, LastEventAt: second})
	f.wantSession("after the ended session was evicted", Session{ID: "fourth", State: StateIdle, StartedAt: fourth, LastEventAt: fourth})
	wantIDs(t, "live sessions", f.tbl.live(), "fourth", "second")
}
