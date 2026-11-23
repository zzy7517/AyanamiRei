// Package agenthooks receives Claude Code hook events for sessions whose
// conversation the daemon cannot read, such as a CLI running in a terminal,
// and records which of them has a turn in progress.
//
// A Manager listens on 127.0.0.1 and authenticates each launch with a bearer
// token it minted. SettingsFragment returns the --settings JSON that makes a
// CLI report to the manager.
//
// An interrupted turn reports no Stop, so a running state can outlive its
// turn; see StateRunning.
package agenthooks

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	hookPath = "/hook"
	// readHeaderTimeout drops clients that never finish sending headers.
	readHeaderTimeout = 5 * time.Second
	// hookPayloadLimit bounds one hook's JSON, which carries the prompt or the
	// Stop event's last assistant message.
	hookPayloadLimit = 2 << 20
	// turnEndQueue bounds the turn ends waiting to be read.
	turnEndQueue = 256
)

// ErrClosed is returned by MintToken once Close has stopped the listener.
var ErrClosed = errors.New("agenthooks: receiver is closed")

// Options configures New. The zero value takes every default.
type Options struct {
	// MaxSessions caps the table. When a new session arrives at the cap, the
	// least recently seen one is dropped, ended sessions first. Default 1024.
	MaxSessions int
	// SessionTTL drops sessions that have not reported a hook for this long.
	// Default seven days.
	SessionTTL time.Duration
	// Logger receives hook traffic at debug level. Default slog.Default().
	Logger *slog.Logger
}

// TurnEnd is a session's turn that ended, as its Stop or StopFailure hook
// reported. IssueID is the issue whose agent terminal owns Cwd; empty when no
// agent terminal is mapped to that directory.
type TurnEnd struct {
	IssueID   string
	Cwd       string
	SessionID string
}

// Manager receives hook events on a loopback listener and owns the session table.
type Manager struct {
	table    *table
	logger   *slog.Logger
	listener net.Listener
	server   *http.Server
	// turnEnds carries the turn ends nobody has read yet.
	turnEnds chan TurnEnd

	mu     sync.Mutex
	tokens [][]byte
	closed bool
	// issues maps a directory to the issue whose agent terminal runs there,
	// and to how many terminals claimed it.
	issues map[string]mappedIssue
}

type mappedIssue struct {
	issueID string
	claims  int
}

// MapIssue makes the turn ends of sessions in dir report issueID. Dir has to
// match the cwd the hooks report. Claims are counted: the mapping lasts until
// as many UnmapIssue calls were made.
func (m *Manager) MapIssue(dir, issueID string) {
	if dir == "" || issueID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.issues == nil {
		m.issues = map[string]mappedIssue{}
	}
	mapped := m.issues[filepath.Clean(dir)]
	mapped.issueID = issueID
	mapped.claims++
	m.issues[filepath.Clean(dir)] = mapped
}

// UnmapIssue gives up one claim made by MapIssue.
func (m *Manager) UnmapIssue(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cwd := filepath.Clean(dir)
	mapped := m.issues[cwd]
	if mapped.claims <= 1 {
		delete(m.issues, cwd)
		return
	}
	mapped.claims--
	m.issues[cwd] = mapped
}

// TurnEnds reports each turn that ends, until the manager is closed. A report
// nobody reads is dropped, not held.
func (m *Manager) TurnEnds() <-chan TurnEnd { return m.turnEnds }

// turnEnded offers a session's ended turn on TurnEnds.
func (m *Manager) turnEnded(session Session) {
	m.mu.Lock()
	issueID := m.issues[session.Cwd].issueID
	m.mu.Unlock()
	select {
	case m.turnEnds <- TurnEnd{IssueID: issueID, Cwd: session.Cwd, SessionID: session.ID}:
	default:
		m.logger.Debug("agent hooks: turn end not reported", "session", session.ID)
	}
}

// New starts the receiver on an ephemeral 127.0.0.1 port. Close stops it.
func New(opts Options) (*Manager, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for agent hooks: %w", err)
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		table:    newTable(time.Now, opts.MaxSessions, opts.SessionTTL),
		logger:   logger,
		listener: listener,
		turnEnds: make(chan TurnEnd, turnEndQueue),
	}
	m.server = &http.Server{Handler: m, ReadHeaderTimeout: readHeaderTimeout}
	go func() { _ = m.server.Serve(listener) }()
	return m, nil
}

// Port is the loopback port the receiver listens on.
func (m *Manager) Port() int {
	return m.listener.Addr().(*net.TCPAddr).Port
}

// MintToken issues the bearer token of one launch.
func (m *Manager) MintToken() (string, error) {
	token := rand.Text()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", ErrClosed
	}
	m.tokens = append(m.tokens, []byte(token))
	return token, nil
}

// Session returns the session with the given CLI session id.
func (m *Manager) Session(id string) (Session, bool) {
	return m.table.get(id)
}

// LatestForDir returns the most recently started session in dir that has not
// ended. dir has to match the cwd the hooks reported, symlinks included.
func (m *Manager) LatestForDir(dir string) (Session, bool) {
	return m.table.latestForDir(dir)
}

// LiveSessions returns the sessions that have not ended, most recently started first.
func (m *Manager) LiveSessions() []Session {
	return m.table.live()
}

// Close stops the listener. Calls still in flight fail, which the hook command tolerates.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	return m.server.Close()
}

// ServeHTTP implements http.Handler. A hook is answered as soon as the
// in-memory table is updated, with a body-less reply: hook output can carry a
// decision or additionalContext that restricts or alters the session, and the
// stdout of SessionStart and UserPromptSubmit hooks enters Claude's context.
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != hookPath {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !m.authorized(r.Header.Get("Authorization")) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var p hookPayload
	r.Body = http.MaxBytesReader(w, r.Body, hookPayloadLimit)
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		m.logger.Warn("agent hooks: invalid payload", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if slices.Contains(hookEvents[:], p.Event) {
		if p.SessionID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if session, ended := m.table.record(p); ended {
			m.turnEnded(session)
		}
	}
	m.logger.Debug("agent hooks: received", "event", p.Event, "session", p.SessionID)
	w.WriteHeader(http.StatusNoContent)
}

// authorized reports whether header carries a bearer token this manager minted.
// Every token is compared, so the time taken does not reveal which one matched.
func (m *Manager) authorized(header string) bool {
	const scheme = "Bearer "
	if !strings.HasPrefix(header, scheme) {
		return false
	}
	presented := []byte(header[len(scheme):])
	m.mu.Lock()
	tokens := m.tokens
	m.mu.Unlock()
	accepted := 0
	for _, token := range tokens {
		accepted |= subtle.ConstantTimeCompare(presented, token)
	}
	return accepted == 1
}
