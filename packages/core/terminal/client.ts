/**
 * One terminal's session on the client side.
 *
 * A session owns a socket and nothing else: it fetches a ticket, connects,
 * asks the server to open a shell or to attach to a running one, and hands
 * the terminal's output and lifecycle to its listeners. When the socket drops
 * while the terminal is still running it reconnects with backoff and
 * reattaches by the terminal id.
 *
 * `dispose()` detaches. The socket closes and the process keeps running, so
 * the terminal can be attached again later; only `close()` ends it.
 */
import { useEffect, useMemo, useRef, useSyncExternalStore } from "react";
import { api, ApiError } from "../api";
import { noopLogger, type Logger } from "../logger";
import {
  TERMINAL_TICKET_ENDPOINT,
  decodeOutputFrame,
  encodeClientMessage,
  isTerminalServerMessageType,
  parseServerMessage,
  terminalSocketUrl,
  type TerminalErrorCode,
  type TerminalExitMessage,
  type TerminalReadyMessage,
  type TerminalSize,
  type TerminalAgentTarget,
  type TerminalTicket,
} from "./protocol";

export type TerminalStatus = "connecting" | "ready" | "exited" | "error";

export interface TerminalExitInfo {
  /** Null when a signal ended the process, or when the session lost the socket before the server could say. */
  exitCode: number | null;
  signal: string | null;
}

/**
 * A failure. `code` is one of the server's error codes, or `unavailable` when
 * the ticket endpoint refused or never answered usably. `message` is the
 * server's own words or a technical detail; the UI words the code itself.
 */
export interface TerminalErrorInfo {
  code: TerminalErrorCode | "unavailable";
  message: string;
}

export interface TerminalSessionState {
  status: TerminalStatus;
  /** The terminal the server bound this session to; null until the first `ready`. */
  terminalId: string | null;
  pid: number | null;
  /** Set once the process has ended. */
  exit: TerminalExitInfo | null;
  /** Set when the session gave up. */
  error: TerminalErrorInfo | null;
  /** Tries made in a row to reach `ready`; 0 until the first drop. */
  reconnectAttempt: number;
  /** `close()` was asked for and no `exit` has answered yet. */
  closing: boolean;
}

export interface TerminalSessionOptions {
  /** Fetches a ticket, good for one connection attempt. Defaults to the API client. */
  createTicket?: () => Promise<TerminalTicket | null>;
  /** The socket address for a ticket. Defaults to the terminal socket on the API client's origin. */
  socketUrl?: (ticket: string) => string;
  logger?: Logger;
}

type Intent =
  | { kind: "open"; target: { cwd: string } | { agent: TerminalAgentTarget }; daemonId?: string }
  | { kind: "attach"; terminalId: string };

/** The size asked for until the viewport reports its own. */
const DEFAULT_SIZE: TerminalSize = { cols: 80, rows: 24 };

// Same reconnect curve as the realtime socket: exponential with jitter, so
// clients dropped together do not return together.
const RECONNECT_BASE_DELAY_MS = 1_000;
const RECONNECT_MAX_DELAY_MS = 30_000;
/**
 * Tries to reach a terminal before the session stops asking: before the first
 * `ready` it reports an error instead, and an end that was asked for is
 * counted as done. A terminal that is known to run is otherwise retried for
 * as long as the session lives, since nothing else can recover it.
 */
const MAX_CONNECT_ATTEMPTS = 8;
const UNPARSEABLE_LOG_MAX_CHARS = 200;

const encoder = new TextEncoder();

const INITIAL_STATE: TerminalSessionState = {
  status: "connecting",
  terminalId: null,
  pid: null,
  exit: null,
  error: null,
  reconnectAttempt: 0,
  closing: false,
};

function summarizeUnparseable(data: unknown): string {
  const text = typeof data === "string" ? data : String(data);
  if (text.length <= UNPARSEABLE_LOG_MAX_CHARS) return text;
  return `${text.slice(0, UNPARSEABLE_LOG_MAX_CHARS)}... (truncated, ${text.length} chars total)`;
}

/** Whole positive dimensions, or null when the pair cannot be sent. */
function normalizeSize(size: Partial<TerminalSize>): TerminalSize | null {
  const { cols, rows } = size;
  if (!Number.isInteger(cols) || !Number.isInteger(rows) || !cols || !rows || cols < 0 || rows < 0) {
    return null;
  }
  return { cols, rows };
}

function sameSize(a: TerminalSize | null, b: TerminalSize): boolean {
  return a !== null && a.cols === b.cols && a.rows === b.rows;
}

export class TerminalSession {
  private createTicket: () => Promise<TerminalTicket | null>;
  private socketUrl: (ticket: string) => string;
  private logger: Logger;
  private state: TerminalSessionState = INITIAL_STATE;
  private stateListeners = new Set<() => void>();
  private outputListeners = new Set<(data: Uint8Array) => void>();
  /** True from `open`/`attach` until the terminal ends, the session gives up, or it is disposed. */
  private running = false;
  private intent: Intent | null = null;
  private socket: WebSocket | null = null;
  private size: TerminalSize = DEFAULT_SIZE;
  /**
   * The size the server's terminal was last given over the current socket.
   * An attach starts with null: the terminal may have been resized by anyone
   * since this client last saw it.
   */
  private syncedSize: TerminalSize | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempt = 0;
  // Distinguishes this run from the one before it, so a ticket requested for
  // an earlier run is not connected when it arrives late.
  private run = 0;
  // One log per connection: a non-conforming frame can repeat indefinitely.
  private badFrameLogged = false;
  private closing = false;

  constructor(options: TerminalSessionOptions = {}) {
    // Read lazily: the API client is installed at app boot, which can be later than this.
    this.createTicket = options.createTicket ?? (() => api.createTerminalTicket());
    this.socketUrl = options.socketUrl ?? ((ticket) => terminalSocketUrl(api.getBaseUrl(), ticket));
    this.logger = options.logger ?? noopLogger;
  }

  subscribe = (listener: () => void): (() => void) => {
    this.stateListeners.add(listener);
    return () => {
      this.stateListeners.delete(listener);
    };
  };

  getState = (): TerminalSessionState => this.state;

  /** Terminal output, as it arrives. */
  onOutput = (listener: (data: Uint8Array) => void): (() => void) => {
    this.outputListeners.add(listener);
    return () => {
      this.outputListeners.delete(listener);
    };
  };

  /** Start a shell in `cwd`. The terminal is `size` until `resize()` says otherwise. */
  open(cwd: string, size?: Partial<TerminalSize>, daemonId?: string): void {
    this.start(
      { kind: "open", target: { cwd }, ...(daemonId ? { daemonId } : {}) },
      size,
    );
  }

  /**
   * Run the agent's command line in a tree of a task, on the machine that
   * built the tree. The server refuses a tree that is not built.
   */
  openAgent(agent: TerminalAgentTarget, size?: Partial<TerminalSize>): void {
    this.start({ kind: "open", target: { agent } }, size);
  }

  /** Stream the output of a running terminal. What is already on screen is not replayed. */
  attach(terminalId: string): void {
    this.start({ kind: "attach", terminalId });
  }

  private start(intent: Intent, size?: Partial<TerminalSize> | undefined): void {
    if (this.running) {
      throw new Error("TerminalSession is already running a terminal; dispose it before starting another");
    }
    if (size) this.size = normalizeSize(size) ?? this.size;
    this.running = true;
    this.intent = intent;
    this.reconnectAttempt = 0;
    this.closing = false;
    this.setState(INITIAL_STATE);
    this.run += 1;
    void this.connect(this.run);
  }

  /** Keyboard input, once the terminal is ready. Input sent before that is dropped, as the server would. */
  sendInput(data: string | Uint8Array<ArrayBuffer>): void {
    if (this.state.status !== "ready" || !this.socket) return;
    this.socket.send(typeof data === "string" ? encoder.encode(data) : data);
  }

  /**
   * Make the terminal `cols` by `rows`. Until the terminal is ready the size
   * is only kept, and it is applied when the server answers.
   */
  resize(cols: number, rows: number): void {
    const size = normalizeSize({ cols, rows });
    if (!size) return;
    this.size = size;
    if (this.state.status === "ready" && !sameSize(this.syncedSize, size)) this.sendResize();
  }

  /**
   * Ask the server to end the process; the `exit` that follows settles the
   * session. While the terminal is only being reconnected the request waits
   * for it, for as long as a first connection is tried for. Nothing to do
   * once the terminal has ended.
   */
  close(): void {
    if (this.state.status !== "ready" && this.state.status !== "connecting") return;
    this.closing = true;
    this.setState({ closing: true });
    if (this.state.status === "ready") this.sendClose();
  }

  /** Detach: close the socket without ending the process, which stays attachable by `state.terminalId`. */
  dispose(): void {
    this.clearReconnectTimer();
    this.running = false;
    this.intent = null;
    this.closing = false;
    this.run += 1;
    this.teardownSocket();
    this.stateListeners.clear();
    this.outputListeners.clear();
  }

  /** One connection attempt: a fresh ticket, then the socket. */
  private async connect(run: number): Promise<void> {
    let ticket: TerminalTicket | null;
    try {
      ticket = await this.createTicket();
    } catch (error) {
      this.onTicketFailure(error, run);
      return;
    }
    // Disposed, or restarted, while the ticket was in flight.
    if (!this.running || run !== this.run) return;
    if (!ticket) {
      this.fail({ code: "unavailable", message: `${TERMINAL_TICKET_ENDPOINT}: no ticket in the response` });
      return;
    }
    this.openSocket(ticket.ticket);
  }

  private onTicketFailure(error: unknown, run: number): void {
    if (!this.running || run !== this.run) return;
    // A refusal is final: the same request would be refused again. Anything
    // else (a network error, a 5xx, a rate limit) can clear up on its own.
    if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 429) {
      this.fail({ code: "unavailable", message: `${TERMINAL_TICKET_ENDPOINT}: ${error.status} ${error.statusText}` });
      return;
    }
    this.scheduleReconnect(error instanceof ApiError ? error.retryAfterMs : undefined);
  }

  private openSocket(ticket: string): void {
    this.badFrameLogged = false;
    this.syncedSize = null;
    let socket: WebSocket;
    try {
      socket = new WebSocket(this.socketUrl(ticket));
    } catch (error) {
      // An address that does not parse is not something another try can fix.
      this.fail({ code: "unavailable", message: error instanceof Error ? error.message : String(error) });
      return;
    }
    socket.binaryType = "arraybuffer";
    socket.onopen = () => this.onSocketOpen(socket);
    socket.onmessage = (event) => this.onSocketMessage(socket, event.data);
    socket.onerror = () => {
      // onclose follows and decides what the drop means.
    };
    socket.onclose = () => this.onSocketClose(socket);
    this.socket = socket;
  }

  private onSocketOpen(socket: WebSocket): void {
    if (this.socket !== socket || !this.intent) return;
    if (this.intent.kind === "open") {
      const { target, daemonId } = this.intent;
      const size = { cols: this.size.cols, rows: this.size.rows };
      // An open names a directory or a task's tree, never both.
      if ("cwd" in target) {
        socket.send(
          encodeClientMessage({ type: "open", cwd: target.cwd, ...size, ...(daemonId ? { daemonId } : {}) }),
        );
      } else {
        socket.send(encodeClientMessage({ type: "open", agent: target.agent, ...size }));
      }
      this.syncedSize = this.size;
    } else {
      socket.send(encodeClientMessage({ type: "attach", terminalId: this.intent.terminalId }));
    }
  }

  private onSocketMessage(socket: WebSocket, data: unknown): void {
    if (this.socket !== socket) return;
    if (typeof data === "string") {
      this.onControlFrame(data);
      return;
    }
    const bytes = decodeOutputFrame(data);
    if (bytes) for (const listener of this.outputListeners) listener(bytes);
  }

  private onControlFrame(text: string): void {
    let raw: unknown;
    try {
      raw = JSON.parse(text);
    } catch {
      this.logBadFrame("terminal: received an unparseable control frame", summarizeUnparseable(text));
      return;
    }
    if (!isTerminalServerMessageType((raw as { type?: unknown } | null)?.type)) {
      this.logBadFrame("terminal: dropping a control frame of unknown type", summarizeUnparseable(text));
      return;
    }
    // A frame that parses but does not fit is logged by the parser and dropped.
    const message = parseServerMessage(raw);
    if (!message) return;
    switch (message.type) {
      case "ready":
        this.onReady(message);
        break;
      case "exit":
        this.onExit(message);
        break;
      case "error":
        this.fail({ code: message.code, message: message.message });
        break;
    }
  }

  private logBadFrame(message: string, detail: string): void {
    if (this.badFrameLogged) return;
    this.badFrameLogged = true;
    this.logger.warn(message, detail);
  }

  private onReady(message: TerminalReadyMessage): void {
    this.reconnectAttempt = 0;
    this.setState({
      status: "ready",
      terminalId: message.terminalId,
      pid: message.pid,
      exit: null,
      error: null,
      reconnectAttempt: 0,
    });
    this.logger.info("terminal: ready", message.terminalId);
    if (!sameSize(this.syncedSize, this.size)) this.sendResize();
    if (this.closing) this.sendClose();
  }

  private onExit(message: TerminalExitMessage): void {
    this.clearReconnectTimer();
    this.running = false;
    this.closing = false;
    this.setState({
      status: "exited",
      terminalId: this.state.terminalId ?? message.terminalId,
      exit: { exitCode: message.exitCode, signal: message.signal ?? null },
      error: null,
      closing: false,
    });
    // The server closes the socket after the exit. Ours goes first, so that
    // close is not taken for a drop to recover from.
    this.teardownSocket();
  }

  /** Give up on the terminal and report why. */
  private fail(error: TerminalErrorInfo): void {
    this.clearReconnectTimer();
    this.running = false;
    this.closing = false;
    this.setState({ status: "error", error, closing: false });
    this.teardownSocket();
    this.logger.warn(`terminal: ${error.code}`, error.message);
  }

  private onSocketClose(socket: WebSocket): void {
    if (this.socket !== socket) return;
    this.socket = null;
    this.syncedSize = null;
    // Exited, failed or disposed: the close was ours or expected.
    if (!this.running) return;
    // The end went out and the socket went before the server could report
    // it. The process was asked to end, which is all this side knows.
    if (this.closing && this.state.status === "ready") {
      this.endUnreported();
      return;
    }
    this.scheduleReconnect();
  }

  /** The end was asked for; whether the process did end, and how, stays unknown here. */
  private endUnreported(): void {
    this.clearReconnectTimer();
    this.running = false;
    this.closing = false;
    this.setState({ status: "exited", exit: { exitCode: null, signal: null }, closing: false });
    this.teardownSocket();
  }

  private scheduleReconnect(minDelayMs?: number): void {
    if (!this.running) return;
    this.reconnectAttempt += 1;
    // The end was asked for and the terminal stays out of reach: waiting
    // longer would not tell any more than this.
    if (this.closing && this.reconnectAttempt >= MAX_CONNECT_ATTEMPTS) {
      this.endUnreported();
      return;
    }
    if (this.state.terminalId === null && this.reconnectAttempt >= MAX_CONNECT_ATTEMPTS) {
      this.fail({
        code: "unavailable",
        message: `no terminal after ${this.reconnectAttempt} attempts`,
      });
      return;
    }
    // A terminal that was reached is picked up again by its id; only a shell
    // that never answered is asked for again.
    if (this.state.terminalId !== null) {
      this.intent = { kind: "attach", terminalId: this.state.terminalId };
    }
    const base = Math.min(
      RECONNECT_BASE_DELAY_MS * 2 ** (this.reconnectAttempt - 1),
      RECONNECT_MAX_DELAY_MS,
    );
    // ±20 % so clients dropped together do not return together.
    const jitter = base * 0.2 * (Math.random() * 2 - 1);
    const delay = Math.round(Math.max(minDelayMs ?? 0, base + jitter));
    this.setState({ status: "connecting", reconnectAttempt: this.reconnectAttempt });
    this.logger.warn(
      `terminal: disconnected, reconnecting in ${delay}ms (attempt ${this.reconnectAttempt})`,
    );
    this.clearReconnectTimer();
    const run = this.run;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      void this.connect(run);
    }, delay);
  }

  private sendResize(): void {
    if (!this.socket || this.state.status !== "ready") return;
    this.socket.send(encodeClientMessage({ type: "resize", cols: this.size.cols, rows: this.size.rows }));
    this.syncedSize = this.size;
  }

  private sendClose(): void {
    const terminalId = this.state.terminalId;
    if (!this.socket || !terminalId || this.state.status !== "ready") return;
    this.socket.send(encodeClientMessage({ type: "close", terminalId }));
  }

  private teardownSocket(): void {
    const socket = this.socket;
    if (!socket) return;
    this.socket = null;
    socket.onopen = null;
    socket.onmessage = null;
    socket.onerror = null;
    socket.onclose = null;
    socket.close();
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer === null) return;
    clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
  }

  private setState(patch: Partial<TerminalSessionState>): void {
    this.state = { ...this.state, ...patch };
    for (const listener of this.stateListeners) listener();
  }
}

/** The session's state, re-rendering whenever it changes. */
export function useTerminalSessionState(session: TerminalSession): TerminalSessionState {
  return useSyncExternalStore(session.subscribe, session.getState, session.getState);
}

export type TerminalSessionIntent =
  | {
      /** Directory the shell starts in. Another directory is another session. */
      cwd: string;
      agent?: undefined;
      /**
       * Rejoin the terminal with this id when the session for `cwd` starts,
       * instead of opening a shell. It is read at that moment only: the id
       * usually arrives in the same intent a moment later, when `ready` names
       * the terminal just opened, and that must not restart the session.
       */
      terminalId?: string;
      daemonId?: string;
    }
  | {
      /** The task tree the agent's command line runs in. Another task or tree is another session. */
      agent: TerminalAgentTarget;
      cwd?: undefined;
      /** Rejoin the terminal with this id, as a shell's session does. */
      terminalId?: string;
      // The server runs the agent where the tree was built; no machine is chosen here.
      daemonId?: undefined;
    };

/**
 * A session tied to the component's lifetime: it opens or attaches on mount
 * and detaches, leaving the process running, on unmount or when the intent
 * changes. A session that has ended is not started again until `cwd`,
 * `agent` or `daemonId` changes; call `open` on it to start another terminal.
 */
export function useTerminalSession(
  intent: TerminalSessionIntent,
  options?: TerminalSessionOptions,
): TerminalSession {
  const { cwd, terminalId, daemonId } = intent;
  // Primitives, so that an intent object rebuilt on every render of its caller
  // is not read as another session.
  const agentIssueId = intent.agent?.issue_id;
  const agentTreeId = intent.agent?.tree_id;
  const { createTicket, socketUrl, logger } = options ?? {};
  const session = useMemo(
    () => new TerminalSession({ createTicket, socketUrl, logger }),
    // `cwd`, `daemonId` and the tree are not read above: they key the session,
    // so another directory, machine or tree is another session and this one
    // is disposed of.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [createTicket, socketUrl, logger, cwd, daemonId, agentIssueId, agentTreeId],
  );
  // Through a ref, because the id is an input to the start of a session and
  // not a reason to start one over (see TerminalSessionIntent).
  const target = useRef(terminalId);
  useEffect(() => {
    target.current = terminalId;
  }, [terminalId]);
  useEffect(() => {
    if (target.current) session.attach(target.current);
    else if (agentIssueId !== undefined && agentTreeId !== undefined) {
      session.openAgent({ issue_id: agentIssueId, tree_id: agentTreeId });
    } else session.open(cwd ?? "", undefined, daemonId);
    return () => session.dispose();
  }, [session, cwd, daemonId, agentIssueId, agentTreeId]);
  return session;
}
