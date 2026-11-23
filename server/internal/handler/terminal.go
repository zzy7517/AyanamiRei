package handler

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/terminal"
)

// CreateTerminalTicket issues the single-use ticket a browser opens
// /terminal/ws with. Behind the terminal_v1 flag: it answers 404 while off.
func (h *Handler) CreateTerminalTicket(w http.ResponseWriter, r *http.Request) {
	if t, ok := h.terminalRelay(w); ok {
		t.HandleTicket(w, r)
	}
}

// TerminalWebSocket serves the browser side of the web terminal,
// GET /terminal/ws?ticket=... on the root router next to /ws.
func (h *Handler) TerminalWebSocket(w http.ResponseWriter, r *http.Request) {
	if t, ok := h.terminalRelay(w); ok {
		t.HandleBrowserWS(w, r)
	}
}

// DaemonTerminalWebSocket serves a daemon's terminal connection,
// GET /api/daemon/terminal/ws, under the daemon auth middleware.
func (h *Handler) DaemonTerminalWebSocket(w http.ResponseWriter, r *http.Request) {
	if t, ok := h.terminalRelay(w); ok {
		t.HandleDaemonWS(w, r)
	}
}

func (h *Handler) terminalRelay(w http.ResponseWriter) (*terminal.Terminal, bool) {
	if h.Terminal == nil {
		writeErrorCode(w, http.StatusInternalServerError, "terminal_misconfigured", "terminal unavailable")
		return nil, false
	}
	return h.Terminal, true
}
