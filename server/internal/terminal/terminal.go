// Package terminal is the server side of the web terminal (terminal_v1).
//
// A browser gets a single-use ticket from POST /api/terminal/ticket and opens
// /terminal/ws with it. The server owns no PTY: a daemon does, and keeps one
// dedicated WebSocket to the server (/api/daemon/terminal/ws) over which the
// server asks it to open, resize and close shells and sends keyboard input,
// and the daemon streams output back. The server relays between the two.
//
// Terminal traffic does not ride the daemon control connection: that hub is
// best-effort (bounded queue, slow clients evicted), which is right for
// events and wrong for keystrokes. The daemon opens the terminal connection
// only after a heartbeat ack advertises terminal-v1, and the server refuses
// the connection unless the daemon's request declares the same capability,
// so a daemon that does not implement the terminal never sees a terminal
// frame.
//
// All state is in memory. Tickets, daemon connections and terminals live in
// one server process and end with it; a multi-node deployment needs a relay
// between nodes before a browser and its daemon may land on different ones.
package terminal

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	defaultTicketTTL   = 120 * time.Second
	defaultOpenTimeout = 20 * time.Second

	// Per browser socket: at most 64 frames of at most 32 KiB of output, so a
	// stalled reader costs under 2 MiB before it is detached.
	browserOutboxFrames  = 64
	browserOutboxReserve = 4
	browserOutboxBytes   = 2 << 20

	// Per daemon connection, shared by every terminal on it. Input frames can
	// be as large as a whole paste.
	daemonOutboxFrames  = 1024
	daemonOutboxReserve = 64
	daemonOutboxBytes   = 8 << 20

	// maxControlFrame bounds one JSON control message; the honest ones are
	// under 200 bytes.
	maxControlFrame = 16 << 10
	maxTicketLen    = 256

	// inputStallTimeout is how long a browser socket may wait for room on its
	// daemon's outbound queue before it is detached.
	inputStallTimeout = 2 * time.Second
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  protocol.MaxTerminalOutputChunk,
	WriteBufferSize: protocol.MaxTerminalOutputChunk,
	CheckOrigin:     realtime.CheckOrigin,
}

// DaemonIdentity names who a daemon's terminal connection acts for. UserID is
// the daemon's owner and the only user who may attach to its terminals.
type DaemonIdentity struct {
	UserID   string
	DaemonID string
}

// TicketResponse is the body of POST /api/terminal/ticket.
type TicketResponse struct {
	Ticket           string `json:"ticket"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
}

type Options struct {
	// FeatureFlags gates the whole surface on terminal_v1. Nil means off.
	FeatureFlags *featureflag.Service
	Logger       *slog.Logger
	// TicketTTL and OpenTimeout replace the defaults (two minutes, twenty
	// seconds). They exist for tests.
	TicketTTL   time.Duration
	OpenTimeout time.Duration

	// Agent resolves an open that names a task tree instead of a directory
	// (see AgentResolver). Nil refuses every such open.
	Agent AgentResolver
}

// Terminal relays browser sockets to daemons. The zero value is not usable;
// call New.
type Terminal struct {
	flags       *featureflag.Service
	log         *slog.Logger
	ticketTTL   time.Duration
	openTimeout time.Duration
	agent       AgentResolver
	newID       func() string

	tickets *ticketStore

	// mu guards the three maps below, the binding of every browserConn
	// (closed, terminal) and of every terminal (attached, daemon). A frame is
	// enqueued only by a goroutine holding mu that has just checked its
	// socket is not closed, which is what lets writePump treat "done closed"
	// as "nothing more will be queued".
	mu        sync.Mutex
	daemons   map[daemonKey]*daemonConn
	terminals map[string]*terminal
	pending   map[string]*pendingRequest
}

type daemonKey struct{ userID, daemonID string }

func New(opts Options) *Terminal {
	t := &Terminal{
		flags:       opts.FeatureFlags,
		log:         opts.Logger,
		ticketTTL:   opts.TicketTTL,
		openTimeout: opts.OpenTimeout,
		agent:       opts.Agent,
		newID:       newID,
		tickets:     newTicketStore(time.Now),
		daemons:     map[daemonKey]*daemonConn{},
		terminals:   map[string]*terminal{},
		pending:     map[string]*pendingRequest{},
	}
	if t.log == nil {
		t.log = slog.Default()
	}
	if t.ticketTTL <= 0 {
		t.ticketTTL = defaultTicketTTL
	}
	if t.openTimeout <= 0 {
		t.openTimeout = defaultOpenTimeout
	}
	return t
}

// newID mints an unguessable id for a request to a daemon. Terminal ids come
// from the daemon and are minted the same way there.
func newID() string {
	return rand.Text()
}

func (t *Terminal) enabled(r *http.Request) bool {
	return featureflags.TerminalV1Enabled(r.Context(), t.flags)
}

// HandleTicket issues a single-use ticket for the authenticated user, whom
// middleware.Auth names in X-User-ID. It answers 404 while terminal_v1 is off.
func (t *Terminal) HandleTicket(w http.ResponseWriter, r *http.Request) {
	if !t.enabled(r) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "user not authenticated")
		return
	}
	ticket, err := t.tickets.issue(userID, t.ticketTTL)
	if errors.Is(err, errTooManyTickets) {
		writeError(w, http.StatusTooManyRequests, "too many outstanding terminal tickets")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue ticket")
		return
	}
	writeJSON(w, http.StatusOK, TicketResponse{Ticket: ticket, ExpiresInSeconds: int(t.ticketTTL / time.Second)})
}

// HandleBrowserWS upgrades GET /terminal/ws?ticket=... and serves the socket
// until it closes. The ticket is bound to one user and redeemed by the
// upgrade, so a ticket cannot be replayed or used by anyone else. While
// terminal_v1 is off the handshake is refused with 404, like the ticket
// endpoint.
func (t *Terminal) HandleBrowserWS(w http.ResponseWriter, r *http.Request) {
	if !t.enabled(r) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !realtime.CheckOrigin(r) {
		writeError(w, http.StatusForbidden, "origin not allowed")
		return
	}
	raw := r.URL.Query().Get("ticket")
	if raw == "" || len(raw) > maxTicketLen {
		writeError(w, http.StatusForbidden, "invalid ticket")
		return
	}
	userID, ok := t.tickets.redeem(raw)
	if !ok {
		writeError(w, http.StatusForbidden, "invalid ticket")
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already answered the request.
		return
	}
	t.ServeBrowser(conn, userID)
}

// HandleDaemonWS upgrades a daemon's terminal connection,
// GET /api/daemon/terminal/ws?daemon_id=<id>, under the daemon auth
// middleware.
//
// daemon_id is the daemon's own assertion, as in its register request. A user
// credential (JWT or PAT) makes that user the owner. A daemon token is bound
// to one daemon, so the assertion must match it and the token's owner is the
// owner of the terminals.
func (t *Terminal) HandleDaemonWS(w http.ResponseWriter, r *http.Request) {
	if !t.enabled(r) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !requestDeclares(r, protocol.DaemonCapabilityTerminalV1) {
		writeError(w, http.StatusBadRequest, "daemon does not declare "+protocol.DaemonCapabilityTerminalV1)
		return
	}
	identity := DaemonIdentity{DaemonID: r.URL.Query().Get("daemon_id")}
	if identity.DaemonID == "" {
		writeError(w, http.StatusBadRequest, "daemon_id is required")
		return
	}
	if tokenDaemonID := middleware.DaemonIDFromContext(r.Context()); tokenDaemonID != "" {
		if tokenDaemonID != identity.DaemonID {
			writeError(w, http.StatusForbidden, "daemon identity mismatch")
			return
		}
		identity.UserID = middleware.DaemonTokenOwnerIDFromContext(r.Context())
	} else {
		identity.UserID = r.Header.Get("X-User-ID")
	}
	if identity.UserID == "" {
		writeError(w, http.StatusUnauthorized, "user not authenticated")
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	t.ServeDaemon(conn, identity)
}

// requestDeclares reports whether the request's X-Client-Capabilities header
// lists capability.
func requestDeclares(r *http.Request, capability string) bool {
	for _, part := range strings.Split(r.Header.Get("X-Client-Capabilities"), ",") {
		if strings.TrimSpace(part) == capability {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// validCwd is the server's lexical check of an open request's directory. The
// daemon decides whether the directory is inside its terminal root.
func validCwd(cwd string) bool {
	return filepath.IsAbs(cwd) && filepath.Clean(cwd) == cwd && !strings.ContainsRune(cwd, 0)
}
