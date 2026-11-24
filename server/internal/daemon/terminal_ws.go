package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/internal/daemon/agentsession"
	dterminal "github.com/multica-ai/multica/server/internal/daemon/terminal"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	terminalReconnectMin = time.Second
	terminalReconnectMax = 30 * time.Second
)

var (
	errTerminalUnsupported = errors.New("the server has no terminal endpoint")
	errTerminalStopped     = errors.New("the server stopped offering the terminal")
)

// terminalURL is the daemon side of the terminal relay, on the server the task
// wakeups also connect to.
func terminalURL(baseURL, daemonID string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", errors.New("invalid daemon server URL")
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", errors.New("daemon server URL must use http, https, ws, or wss")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/daemon/terminal/ws"
	u.RawPath = ""
	u.Fragment = ""
	q := u.Query()
	q.Set("daemon_id", daemonID)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// noteTerminalServer records whether the latest heartbeat ack advertises the
// terminal capability. A change wakes the terminal loop, which dials on the
// first offer; a withdrawal also ends the connection it is holding.
func (d *Daemon) noteTerminalServer(supported bool) {
	if d.terminalServer.Swap(supported) == supported {
		return
	}
	if !supported {
		d.terminalMu.Lock()
		cancel := d.terminalCancel
		d.terminalMu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	select {
	case d.terminalWake <- struct{}{}:
	default:
	}
}

// terminalLoop keeps one connection to the terminal relay for as long as the
// server offers it. Nothing is dialed before that, so a daemon talking to an
// older server, or to one with the flag off, opens no connection at all.
func (d *Daemon) terminalLoop(ctx context.Context) {
	delay := time.Duration(0)
	for ctx.Err() == nil {
		if !d.terminalServer.Load() {
			select {
			case <-ctx.Done():
				return
			case <-d.terminalWake:
			}
			continue
		}
		connCtx, cancel := context.WithCancel(ctx)
		// Registering under the mutex with the flag still set closes the
		// gap to a withdrawal that finds nothing to cancel.
		d.terminalMu.Lock()
		d.terminalCancel = cancel
		withdrawn := !d.terminalServer.Load()
		d.terminalMu.Unlock()
		started := time.Now()
		err := errTerminalStopped
		if !withdrawn {
			err = d.runTerminalConnection(connCtx)
		}
		d.terminalMu.Lock()
		d.terminalCancel = nil
		d.terminalMu.Unlock()
		cancel()
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= terminalReconnectMax {
			delay = 0
		}
		if errors.Is(err, errTerminalUnsupported) {
			// The server has no terminal endpoint; wait for an ack that
			// says otherwise instead of retrying.
			d.terminalServer.Store(false)
			delay = 0
			d.logger.Info("terminal: idle until the server offers the terminal", "reason", err)
		} else if !errors.Is(err, errTerminalStopped) {
			delay = nextTerminalDelay(delay)
			if err != nil {
				d.logger.Warn("terminal: connection ended; reconnecting", "after", delay, "error", err)
			}
		} else {
			delay = 0
		}
		if delay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			case <-d.terminalWake:
			}
		}
	}
}

func nextTerminalDelay(current time.Duration) time.Duration {
	next := current * 2
	if next < terminalReconnectMin {
		next = terminalReconnectMin
	}
	if next > terminalReconnectMax {
		next = terminalReconnectMax
	}
	return jitterDuration(next)
}

func (d *Daemon) runTerminalConnection(ctx context.Context) error {
	wsURL, err := terminalURL(d.cfg.ServerBaseURL, d.cfg.DaemonID)
	if err != nil {
		return err
	}
	// Same dial as the task wakeups: the environment's proxy settings apply.
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
	}
	conn, resp, err := dialer.DialContext(ctx, wsURL, d.terminalHeaders())
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return errTerminalUnsupported
		}
		return err
	}
	defer conn.Close()
	dterminal.ServeWS(ctx, conn, dterminal.Config{
		TaskRoot: d.cfg.TaskRoot, ProjectRoot: d.cfg.ProjectRoot, Logger: d.logger, Launcher: d.agentLauncher(),
	})
	if !d.terminalServer.Load() {
		return errTerminalStopped
	}
	return nil
}

func (d *Daemon) terminalHeaders() http.Header {
	headers := http.Header{}
	if d.client.platform != "" {
		headers.Set("X-Client-Platform", d.client.platform)
	}
	if d.client.version != "" {
		headers.Set("X-Client-Version", d.client.version)
	}
	if d.client.os != "" {
		headers.Set("X-Client-OS", d.client.os)
	}
	headers.Set("X-Client-Capabilities", daemonClientCapabilities())
	return headers
}

// claudeProvider is the agent whose CLI an agent terminal runs.
const claudeProvider = "claude"

// agentLauncher is what an agent terminal is started with: nil when this
// daemon has no hook receiver or no Claude Code. The executable is resolved
// the way a task launch resolves it, so a version manager replacing the
// binary between sessions is followed.
func (d *Daemon) agentLauncher() dterminal.Launcher {
	entry, ok := d.agents()[claudeProvider]
	if !ok || d.agentHooks == nil {
		return nil
	}
	resolved, _ := d.resolveAgentEntry(context.Background(), claudeProvider, entry)
	// No skill policy applies: nothing prepares a task for these sessions.
	return agentsession.NewLauncher(agentsession.Config{Claude: resolved.Path, Hooks: d.agentHooks, Logger: d.logger})
}

// terminalServerOffered reports a heartbeat ack that lists the terminal
// capability.
func terminalServerOffered(resp *HeartbeatResponse) bool {
	return resp != nil && slices.Contains(resp.ServerCapabilities, protocol.DaemonCapabilityTerminalV1)
}
