"use client";

import { useEffect, useState, useSyncExternalStore } from "react";
import { api } from "../api";
import {
  terminalHost,
  type TerminalErrorCode,
  type TerminalExit,
  type TerminalHostBridge,
  type TerminalHostEvent,
  type TerminalHostTarget,
  type TerminalSize,
} from "./host";

export type TerminalStatus = "idle" | "connecting" | "ready" | "exited" | "error";

export interface TerminalErrorInfo {
  code: TerminalErrorCode;
  message: string;
}

export interface TerminalSessionState {
  status: TerminalStatus;
  /** Named once the owner opened the terminal, and kept after the process ended. */
  terminalId: string | null;
  pid: number | null;
  /** The folder the terminal runs in. */
  cwd: string | null;
  exit: TerminalExit | null;
  error: TerminalErrorInfo | null;
  /** The process was asked to end and has not yet. */
  closing: boolean;
}

/** What a page shows: what the terminal runs, and the one to rejoin, if any. */
export interface TerminalSessionIntent {
  target: TerminalHostTarget;
  /**
   * A terminal that is already open, which is how a desktop tab finds its
   * terminal again after a switch. Once that terminal is gone, `target`
   * opens again.
   *
   * Read when the session starts. The id the page writes into its address
   * after that does not make another session.
   */
  terminalId?: string;
  /**
   * What makes this intent the one a session is kept for. Defaults to the
   * target; an agent's target carries a task's text, which an edit changes
   * without changing the terminal.
   */
  key?: string;
}

export interface TerminalSessionOptions {
  /** The size the terminal opens at, until the view reports its own. */
  size?: TerminalSize;
}

const IDLE: TerminalSessionState = {
  status: "idle",
  terminalId: null,
  pid: null,
  cwd: null,
  exit: null,
  error: null,
  closing: false,
};

const DEFAULT_SIZE: TerminalSize = { cols: 80, rows: 24 };

let turnEndReportsStarted = false;

/**
 * Tells the server each time an agent in a terminal finishes a turn. The
 * owner hears it from the agent's hooks whenever it happens, including while
 * no terminal is on screen, so listening starts with the first terminal and
 * is not tied to any page.
 */
function startTurnEndReports(host: TerminalHostBridge): void {
  if (turnEndReportsStarted) return;
  turnEndReportsStarted = true;
  host.onEvent((event) => {
    if (event.type !== "turnEnd") return;
    // A report that does not arrive is dropped: the turn after it reports again.
    void api.reportIssueAgentReply(event.workspaceId, event.taskId).catch(() => undefined);
  });
}

/**
 * One terminal, run by the owner the client has: the desktop app's main
 * process. The session follows it; it does not own the process. A session that
 * is disposed leaves the terminal running, and one made with its id shows it
 * again, from the screen it had reached.
 */
export class TerminalSession {
  private host: TerminalHostBridge | null;
  private state = IDLE;
  private listeners = new Set<() => void>();
  private outputListeners = new Set<(data: string) => void>();
  private screenListeners = new Set<(screen: string) => void>();
  private stopEvents: (() => void) | null = null;
  private size: TerminalSize;
  /** Output that arrived before what belongs above it on screen. */
  private held: string[] | null = null;
  /** The screen of a rejoined terminal, until the view that draws it asks. */
  private screen: string | null = null;
  /** Counts starts, so an answer to a start that was left behind is dropped. */
  private run = 0;

  /**
   * What the page asks for. Read each time a terminal is started, so a start
   * from a page follows the task as it stands then; a terminal that is
   * already open does not change with it.
   */
  intent: TerminalSessionIntent;

  constructor(intent: TerminalSessionIntent, options: TerminalSessionOptions = {}) {
    this.intent = intent;
    this.host = terminalHost();
    this.size = options.size ?? DEFAULT_SIZE;
  }

  readonly subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  readonly getState = (): TerminalSessionState => this.state;

  /** `listener` receives what the terminal printed, in order. Returns the stop. */
  readonly onOutput = (listener: (data: string) => void): (() => void) => {
    this.outputListeners.add(listener);
    return () => this.outputListeners.delete(listener);
  };

  /**
   * `listener` receives the screen a rejoined terminal had reached, to draw
   * before the output that follows it. The view resets what it drew first.
   */
  readonly onScreen = (listener: (screen: string) => void): (() => void) => {
    this.screenListeners.add(listener);
    this.deliverScreen();
    return () => this.screenListeners.delete(listener);
  };

  /** Open the target, or rejoin the terminal the intent names. */
  start(): void {
    this.launchTerminal(true);
  }

  /** Open the target again, whatever terminal the intent named. */
  restart(): void {
    this.launchTerminal(false);
  }

  private launchTerminal(rejoin: boolean): void {
    const host = this.host;
    const run = ++this.run;
    this.held = [];
    // A terminal this session followed before is left to itself.
    if (this.state.terminalId) this.host?.detach(this.state.terminalId);
    this.set({
      status: "connecting",
      terminalId: null,
      pid: null,
      cwd: null,
      exit: null,
      error: null,
      closing: false,
    });
    if (!host) {
      this.refuse("unavailable");
      return;
    }
    startTurnEndReports(host);
    this.stopEvents ??= host.onEvent(this.onEvent);
    void this.launch(host, run, rejoin);
  }

  sendInput(data: string | Uint8Array): void {
    const { terminalId, status } = this.state;
    if (!terminalId || status !== "ready") return;
    this.host?.write(terminalId, typeof data === "string" ? data : new TextDecoder().decode(data));
  }

  resize(size: TerminalSize): void {
    this.size = size;
    const { terminalId, status } = this.state;
    if (terminalId && status === "ready") this.host?.resize(terminalId, size);
  }

  /** Ask for the process to end. It is `exited` once the owner says it did. */
  close(): void {
    const { terminalId, status } = this.state;
    if (!terminalId || status !== "ready") return;
    this.set({ closing: true });
    this.host?.end(terminalId);
  }

  /** Stop following the terminal. Its process keeps running. */
  dispose(): void {
    this.run += 1;
    this.stopEvents?.();
    this.stopEvents = null;
    if (this.state.terminalId) this.host?.detach(this.state.terminalId);
  }

  private async launch(host: TerminalHostBridge, run: number, rejoin: boolean): Promise<void> {
    const { target, terminalId } = this.intent;
    if (rejoin && terminalId) {
      const rejoin = await host.attach(terminalId, this.size);
      if (run !== this.run) return;
      if (rejoin.ok) {
        this.screen = rejoin.screen;
        this.joined({ terminalId, exit: rejoin.exit });
        return;
      }
      if (rejoin.code !== "not_found") {
        this.refuse(rejoin.code, rejoin.message);
        return;
      }
      // No terminal has that id any more, as after the app restarted. The
      // target opens again, rather than the page saying there is nothing.
    }
    const opened = await host.open(target, this.size);
    if (run !== this.run) return;
    if (!opened.ok) {
      this.refuse(opened.code, opened.message);
      return;
    }
    this.joined({ terminalId: opened.terminalId, pid: opened.pid, cwd: opened.cwd });
  }

  private joined(terminal: {
    terminalId: string;
    pid?: number;
    cwd?: string;
    exit?: TerminalExit | null;
  }): void {
    this.set({
      status: terminal.exit ? "exited" : "ready",
      terminalId: terminal.terminalId,
      pid: terminal.pid ?? null,
      cwd: terminal.cwd ?? null,
      exit: terminal.exit ?? null,
    });
    // The screen comes first, then what was printed since it was taken.
    this.deliverScreen();
    const held = this.held;
    this.held = null;
    for (const data of held ?? []) this.emitOutput(data);
  }

  private readonly onEvent = (event: TerminalHostEvent): void => {
    if (event.terminalId !== this.state.terminalId) return;
    if (event.type === "output") {
      if (this.held) this.held.push(event.data);
      else this.emitOutput(event.data);
    } else if (event.type === "exit") {
      this.set({ status: "exited", exit: event.exit, closing: false });
    }
  };

  private emitOutput(data: string): void {
    for (const listener of this.outputListeners) listener(data);
  }

  private deliverScreen(): void {
    if (this.screen === null || this.screenListeners.size === 0) return;
    const screen = this.screen;
    this.screen = null;
    for (const listener of this.screenListeners) listener(screen);
  }

  private refuse(code: TerminalErrorCode, message = ""): void {
    this.held = null;
    this.set({ status: "error", error: { code, message } });
  }

  private set(patch: Partial<TerminalSessionState>): void {
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener();
  }
}

/**
 * The session for one terminal. It starts on its own and is left running when
 * the page goes. Another target is another session, unless the intent carries
 * a `key`: then the page keeps the terminal it has while it is opening,
 * running or ended, and starts the next one from the intent as it stands.
 */
export function useTerminalSession(
  intent: TerminalSessionIntent,
  options: TerminalSessionOptions = {},
): TerminalSession {
  const target = JSON.stringify(intent.target);
  const key = intent.key ?? target;
  const [entry, setEntry] = useState(() => ({
    key,
    target,
    session: new TerminalSession(intent, options),
  }));
  // A terminal that is opening, running or ended stays this page's, whatever
  // the intent says since: it is what the viewer looks at, or just lost, and
  // a task edited meanwhile does not start its agent again. The intent as it
  // stands is what the next start opens. A terminal that was refused gives
  // way to a changed intent: a tree that was built in the meantime opens.
  const status = entry.session.getState().status;
  const kept =
    entry.key === key &&
    (entry.target === target || status === "connecting" || status === "ready" || status === "exited");
  const session = kept ? entry.session : undefined;
  if (session) {
    session.intent = intent;
    entry.target = target;
  }

  useEffect(() => {
    if (!session) return;
    session.start();
    return () => session.dispose();
  }, [session]);

  if (session) return session;
  const next = { key, target, session: new TerminalSession(intent, options) };
  setEntry(next);
  return next.session;
}

export function useTerminalSessionState(session: TerminalSession): TerminalSessionState {
  return useSyncExternalStore(session.subscribe, session.getState, session.getState);
}
