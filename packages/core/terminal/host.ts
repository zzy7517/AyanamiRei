/**
 * The contract between a terminal session and the process that owns the
 * terminals: the desktop app's main process, reached through
 * `window.desktopAPI.terminal`. A client without that bridge has no terminals.
 */

/**
 * The project a terminal opens in without one being added: its folder is
 * `~/ayanami-projects`, which the app creates when missing.
 */
export const TERMINAL_DEFAULT_PROJECT = "default";

export interface TerminalSize {
  cols: number;
  rows: number;
}

/** Why a terminal could not be opened or rejoined. */
export type TerminalErrorCode =
  /** This client cannot own terminals. */
  | "unavailable"
  | "bad_request"
  /** No terminal has that id, or no folder has that path. */
  | "not_found"
  /** The task's tree was not built, so there is no folder to start in. */
  | "tree_not_ready"
  | "project_not_found"
  | "internal";

/** How a terminal's process ended. */
export interface TerminalExit {
  exitCode: number;
  signal?: number;
}

/**
 * Where a folder is: on this computer, or on a host the system `ssh` reaches.
 * `host` is what `ssh` takes for a destination: a name from `~/.ssh/config`,
 * or `user@machine`.
 */
export type TerminalPlace = { kind: "local" } | { kind: "ssh"; host: string };

/** One of a task's trees, as the agent started in it is told about it. */
export interface AgentTerminalTree {
  /** The tree's folder, on the place the agent starts in. */
  path: string;
  repoName: string;
  /** The task branch the tree works on. */
  branch: string;
  /** The branch the tree was created from; a pull request goes there. */
  baseBranch: string;
}

export interface AgentTerminalTask {
  id: string;
  /** The workspace the task is in. A finished turn is reported there, wherever
   *  the window has navigated since. */
  workspaceId: string;
  identifier: string;
  title: string;
  description: string;
}

/** What a terminal runs. */
export type TerminalHostTarget =
  /** A shell in a folder that exists. */
  | { kind: "shell"; place: TerminalPlace; cwd: string }
  /** A shell in the default project's folder, which is created when missing. */
  | { kind: "defaultProject" }
  /**
   * The agent's command line in `cwd`, the first of the task's trees there.
   * The other trees are additional working directories of the agent.
   */
  | {
      kind: "agent";
      place: TerminalPlace;
      cwd: string;
      trees: AgentTerminalTree[];
      task: AgentTerminalTask;
    };

export type TerminalHostOpenResult =
  | { ok: true; terminalId: string; pid: number; cwd: string }
  | { ok: false; code: TerminalErrorCode; message: string };

/**
 * Rejoining a terminal brings what it printed so far: a rejoin is how a
 * desktop tab finds its terminal again, and the screen it left is the screen
 * it comes back to.
 */
export type TerminalHostAttachResult =
  | { ok: true; running: boolean; exit: TerminalExit | null; screen: string }
  | { ok: false; code: TerminalErrorCode; message: string };

export type TerminalHostEvent =
  | { terminalId: string; type: "output"; data: string }
  | { terminalId: string; type: "exit"; exit: TerminalExit }
  /** An agent in the terminal finished a turn. */
  | { terminalId: string; type: "turnEnd"; taskId: string; workspaceId: string };

export interface TerminalHostBridge {
  open(target: TerminalHostTarget, size: TerminalSize): Promise<TerminalHostOpenResult>;
  /** Follow a terminal that is already open, and get its screen. */
  attach(terminalId: string, size: TerminalSize): Promise<TerminalHostAttachResult>;
  /** Stop following a terminal. Its process keeps running. */
  detach(terminalId: string): void;
  write(terminalId: string, data: string): void;
  resize(terminalId: string, size: TerminalSize): void;
  /** End the terminal's process. */
  end(terminalId: string): void;
  /** `listener` hears every terminal this renderer follows. Returns the stop. */
  onEvent(listener: (event: TerminalHostEvent) => void): () => void;
}

/** The terminal owner of this client, or null where there is none. */
export function terminalHost(): TerminalHostBridge | null {
  if (typeof window === "undefined") return null;
  const desktopAPI = (window as unknown as { desktopAPI?: { terminal?: unknown } }).desktopAPI;
  const bridge = desktopAPI?.terminal;
  // A desktop build older than the terminal bridge exposes no `terminal`.
  return typeof bridge === "object" && bridge !== null ? (bridge as TerminalHostBridge) : null;
}
