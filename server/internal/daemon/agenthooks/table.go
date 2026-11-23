package agenthooks

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// State is how far a session's current turn has progressed, as its hooks report it.
type State string

const (
	// StateIdle means no turn is running.
	StateIdle State = "idle"
	// StateRunning means a prompt was submitted and its turn has not reported
	// Stop or StopFailure. An interrupted turn reports neither, so the state
	// can outlive the turn; LastEventAt shows how old it is.
	StateRunning State = "running"
	// StateEnded means the session reported SessionEnd.
	StateEnded State = "ended"
)

// Session is one CLI session as its hooks have reported it.
type Session struct {
	ID string
	// Cwd and TranscriptPath are the last non-empty values reported.
	Cwd            string
	TranscriptPath string
	State          State
	// StartedAt is when SessionStart last started or restarted the session, or
	// when the session first reported itself through another event.
	StartedAt time.Time
	// LastTurnStartedAt and LastTurnEndedAt are zero until the first
	// UserPromptSubmit and the first Stop or StopFailure, and stay set across turns.
	LastTurnStartedAt time.Time
	LastTurnEndedAt   time.Time
	// LastEventAt is when the session last reported an event.
	LastEventAt time.Time
	// LastError describes the last turn that ended with StopFailure, until a
	// later turn starts or ends: the error text the conversation showed, or
	// else the error type with its details.
	LastError string
}

const (
	defaultMaxSessions = 1024
	defaultSessionTTL  = 7 * 24 * time.Hour
)

// table is the bounded in-memory session table.
type table struct {
	mu      sync.Mutex
	now     func() time.Time
	maxSize int
	ttl     time.Duration
	entries map[string]*Session
}

func newTable(now func() time.Time, maxSize int, ttl time.Duration) *table {
	if maxSize <= 0 {
		maxSize = defaultMaxSessions
	}
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	return &table{
		now:     now,
		maxSize: maxSize,
		ttl:     ttl,
		entries: make(map[string]*Session),
	}
}

// record applies one lifecycle event, creating the session if it is new, and
// reports whether the event ended a turn.
func (t *table) record(p hookPayload) (Session, bool) {
	// A Stop that follows a Stop hook's block ends no turn of its own.
	if p.Event == eventStop && p.StopHookActive {
		return Session{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.expireLocked(now)

	session := t.entries[p.SessionID]
	if session == nil {
		session = t.insertLocked(p.SessionID, now)
	}
	switch p.Event {
	case eventSessionStart:
		// Auto-compaction reports SessionStart in the middle of a turn, and at
		// launch UserPromptSubmit can overtake SessionStart, so a running turn
		// is kept.
		if session.State != StateRunning {
			session.State = StateIdle
		}
		switch p.Source {
		case "startup", "resume", "clear":
			session.StartedAt = now
		}
	case eventUserPromptSubmit:
		session.State = StateRunning
		session.LastTurnStartedAt = now
		session.LastError = ""
	case eventStop:
		session.State = StateIdle
		session.LastTurnEndedAt = now
		session.LastError = ""
	case eventStopFailure:
		session.State = StateIdle
		session.LastTurnEndedAt = now
		session.LastError = p.errorText()
	case eventSessionEnd:
		session.State = StateEnded
	}
	if p.Cwd != "" {
		session.Cwd = filepath.Clean(p.Cwd)
	}
	if p.TranscriptPath != "" {
		session.TranscriptPath = p.TranscriptPath
	}
	session.LastEventAt = now
	return *session, p.Event == eventStop || p.Event == eventStopFailure
}

func (t *table) get(id string) (Session, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expireLocked(t.now())
	session, ok := t.entries[id]
	if !ok {
		return Session{}, false
	}
	return *session, true
}

// latestForDir returns the most recently started session in dir that has not ended.
func (t *table) latestForDir(dir string) (Session, bool) {
	if dir == "" {
		return Session{}, false
	}
	cwd := filepath.Clean(dir)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expireLocked(t.now())
	var latest *Session
	for _, session := range t.entries {
		if session.State != StateEnded && session.Cwd == cwd &&
			(latest == nil || compareRecent(*session, *latest) < 0) {
			latest = session
		}
	}
	if latest == nil {
		return Session{}, false
	}
	return *latest, true
}

// live returns the sessions that have not ended, most recently started first.
func (t *table) live() []Session {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expireLocked(t.now())
	sessions := make([]Session, 0, len(t.entries))
	for _, session := range t.entries {
		if session.State != StateEnded {
			sessions = append(sessions, *session)
		}
	}
	slices.SortFunc(sessions, compareRecent)
	return sessions
}

// expireLocked drops sessions that have stayed silent longer than the TTL.
func (t *table) expireLocked(now time.Time) {
	for id, session := range t.entries {
		if now.Sub(session.LastEventAt) > t.ttl {
			delete(t.entries, id)
		}
	}
}

// insertLocked adds a session, first dropping one when the table is full.
func (t *table) insertLocked(id string, now time.Time) *Session {
	if len(t.entries) >= t.maxSize {
		var victim *Session
		for _, session := range t.entries {
			if victim == nil || evictFirst(session, victim) {
				victim = session
			}
		}
		delete(t.entries, victim.ID)
	}
	session := &Session{ID: id, StartedAt: now, LastEventAt: now}
	t.entries[id] = session
	return session
}

// evictFirst reports whether a should be dropped before b.
func evictFirst(a, b *Session) bool {
	aEnded, bEnded := a.State == StateEnded, b.State == StateEnded
	if aEnded != bEnded {
		return aEnded
	}
	return a.LastEventAt.Before(b.LastEventAt)
}

// compareRecent orders sessions most recently started first.
func compareRecent(a, b Session) int {
	if c := b.StartedAt.Compare(a.StartedAt); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}
