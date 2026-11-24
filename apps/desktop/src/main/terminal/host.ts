import { randomUUID } from "crypto";
import { mkdirSync, statSync } from "fs";
import { homedir } from "os";
import { isAbsolute, join } from "path";
import { SerializeAddon } from "@xterm/addon-serialize";
import { Terminal as Screen } from "@xterm/headless";
import { TERMINAL_EVENT_CHANNEL } from "../../shared/terminal";
import type {
  TerminalErrorCode,
  TerminalExit,
  TerminalHostAttachResult,
  TerminalHostEvent,
  TerminalHostOpenResult,
  TerminalHostTarget,
  TerminalPlace,
  TerminalSize,
} from "../../shared/terminal";
import { shellQuote, type AgentHooks, type TurnEnd } from "./agent-hooks";
import { LaunchRefusal, planAgentLaunch } from "./agent-launch";
import type { PtyFactory, PtyHandlers, PtyProcess, PtySpec } from "./pty";
import { defaultShell, shellLaunch, terminalEnv } from "./shell";

/** The folder terminals open in when no project was added: `~/ayanami-projects`. */
export const DEFAULT_PROJECT_FOLDER = "ayanami-projects";

/** The largest a terminal is asked to be. */
const MAX_DIMENSION = 1000;

/** Lines of what went by that a rejoined terminal brings back. */
const SCREEN_SCROLLBACK = 10_000;

/** How long a process gets to end on its own before it is killed. */
const END_GRACE_MS = 1_500;

/** The renderer that follows a terminal, and receives what it prints. */
export interface TerminalSender {
  readonly id: number;
  send(channel: string, payload: TerminalHostEvent): void;
}

interface Session {
  id: string;
  cwd: string;
  followers: Map<number, TerminalSender>;
  pty: PtyProcess | null;
  screen: Screen;
  serializer: SerializeAddon;
  /** Ends once everything written to `screen` before was parsed. */
  flushed: Promise<void>;
  exit: TerminalExit | null;
  cleanup: (() => void) | null;
  endTimer: ReturnType<typeof setTimeout> | null;
}

export interface TerminalHostOptions {
  spawn: PtyFactory;
  env?: NodeJS.ProcessEnv;
  /** Hears the agents' hooks. Without it no agent starts. */
  hooks?: AgentHooks | null;
  homeDir?: () => string;
  platform?: NodeJS.Platform;
  /** Receives an event when no renderer follows its terminal. */
  broadcast?: (event: TerminalHostEvent) => void;
  log?: (message: string, error?: unknown) => void;
}

interface Plan {
  spec: PtySpec;
  cwd: string;
  cleanup?(): void;
  agent?: { token: string; task: TurnEnd };
}

/**
 * The terminals of this app. Each is a process in a pseudo-terminal that
 * keeps running whichever page shows it, so a tab can leave and come back to
 * the same shell. What a terminal printed is also kept as the screen it
 * reached, which is what coming back shows.
 *
 * A terminal ends when its process does, when a renderer asks for that, or
 * with the app.
 */
export class TerminalHost {
  private readonly sessions = new Map<string, Session>();
  /** The agent terminals, by the token their hooks report with. */
  private readonly agents = new Map<string, Session>();
  private readonly options: TerminalHostOptions;

  constructor(options: TerminalHostOptions) {
    this.options = options;
    if (options.hooks) options.hooks.onTurnEnd = this.turnEnded;
  }

  /** Opens a terminal for `sender`, which follows it from the start. */
  async open(
    target: TerminalHostTarget,
    size: TerminalSize,
    sender: TerminalSender,
  ): Promise<TerminalHostOpenResult> {
    const dimensions = dimensionsOf(size);
    if (!dimensions) return refused("bad_request", "cols and rows must be whole numbers from 1 to 1000");
    let plan: Plan;
    try {
      plan = await this.plan(target, dimensions);
    } catch (error) {
      return this.refusal(error, "the terminal could not be opened");
    }

    const session = this.createSession(plan.cwd);
    session.cleanup = plan.cleanup ?? null;
    this.sessions.set(session.id, session);
    if (plan.agent) this.agents.set(plan.agent.token, session);
    session.followers.set(sender.id, sender);
    try {
      session.pty = await this.options.spawn.spawn(plan.spec, this.handlers(session));
    } catch (error) {
      this.drop(session);
      return this.refusal(error, "the terminal could not be started");
    }
    return { ok: true, terminalId: session.id, pid: session.pty.pid, cwd: session.cwd };
  }

  /**
   * Has `sender` follow a terminal that is already open, and returns the
   * screen it has reached, to draw before what it prints next.
   */
  async attach(
    terminalId: string,
    size: TerminalSize,
    sender: TerminalSender,
  ): Promise<TerminalHostAttachResult> {
    const session = this.sessions.get(terminalId);
    if (!session) return refused("not_found", `no terminal is open with the id ${terminalId}`);
    // The screen is taken once everything printed so far is parsed. What is
    // printed from here on reaches the renderer directly, so nothing is shown
    // twice and nothing is missed.
    const printed = session.flushed;
    session.followers.set(sender.id, sender);
    const dimensions = dimensionsOf(size);
    if (dimensions) this.resizeSession(session, dimensions);
    await printed;
    return {
      ok: true,
      running: session.exit === null,
      exit: session.exit,
      screen: session.serializer.serialize({ scrollback: SCREEN_SCROLLBACK }),
    };
  }

  /** Has `sender` stop following a terminal. The process keeps running. */
  detach(terminalId: string, sender: TerminalSender): void {
    const session = this.sessions.get(terminalId);
    if (!session) return;
    session.followers.delete(sender.id);
    this.dropWhenAbandoned(session);
  }

  /** The renderer is gone: it no longer follows any terminal. */
  detachAll(senderId: number): void {
    for (const session of this.sessions.values()) {
      session.followers.delete(senderId);
      this.dropWhenAbandoned(session);
    }
  }

  write(terminalId: string, sender: TerminalSender, data: string): void {
    const session = this.followed(terminalId, sender);
    if (session && typeof data === "string") session.pty?.write(data);
  }

  resize(terminalId: string, sender: TerminalSender, size: TerminalSize): void {
    const session = this.followed(terminalId, sender);
    const dimensions = dimensionsOf(size);
    if (session && dimensions) this.resizeSession(session, dimensions);
  }

  /** Asks for a terminal's process to end. Its followers hear when it has. */
  end(terminalId: string): void {
    const session = this.sessions.get(terminalId);
    if (!session?.pty) return;
    if ((this.options.platform ?? process.platform) === "win32") session.pty.kill();
    else session.pty.kill("SIGHUP");
    // What ignores that is killed once it had a moment to end on its own.
    session.endTimer = setTimeout(() => {
      session.endTimer = null;
      session.pty?.kill("SIGKILL");
    }, END_GRACE_MS);
  }

  /** Ends every terminal at once. The app is quitting. */
  endAll(): void {
    for (const session of this.sessions.values()) {
      if (session.endTimer !== null) clearTimeout(session.endTimer);
      session.endTimer = null;
      session.pty?.kill("SIGKILL");
    }
  }

  private readonly turnEnded = (token: string, task: TurnEnd): void => {
    const session = this.agents.get(token);
    if (!session) return;
    const event: TerminalHostEvent = {
      terminalId: session.id,
      type: "turnEnd",
      taskId: task.taskId,
      workspaceId: task.workspaceId,
    };
    // With nobody watching the terminal, every window hears it instead: the
    // task is then to be marked as having a new reply.
    if (session.followers.size > 0) this.tell(session, event);
    else this.options.broadcast?.(event);
  };

  private async plan(target: TerminalHostTarget, size: TerminalSize): Promise<Plan> {
    const platform = this.options.platform ?? process.platform;
    const env = this.options.env ?? process.env;
    if (target.kind === "defaultProject") {
      const cwd = join((this.options.homeDir ?? homedir)(), DEFAULT_PROJECT_FOLDER);
      try {
        mkdirSync(cwd, { recursive: true });
      } catch (error) {
        throw new LaunchRefusal(
          "internal",
          `the default project's folder could not be created: ${describe(error)}`,
        );
      }
      return this.shell({ kind: "local" }, cwd, env, platform, size);
    }
    if (target.kind === "shell") return this.shell(target.place, target.cwd, env, platform, size);
    if (!this.options.hooks) {
      throw new LaunchRefusal("internal", "the agent's hook receiver is not running");
    }
    const plan = await planAgentLaunch({ target, env, hooks: this.options.hooks, platform });
    return { cwd: target.cwd, cleanup: plan.cleanup, agent: plan.agent, spec: { ...plan.spec, cols: size.cols, rows: size.rows } };
  }

  private shell(
    place: TerminalPlace,
    cwd: string,
    env: NodeJS.ProcessEnv,
    platform: NodeJS.Platform,
    size: TerminalSize,
  ): Plan {
    if (cwd === "") throw new LaunchRefusal("bad_request", "the terminal names no folder");
    const common = { cwd, env: terminalEnv(env), cols: size.cols, rows: size.rows };
    if (place.kind === "ssh") {
      // The host's login shell, in the folder. Nothing is installed there.
      const command = `cd -- ${shellQuote(cwd)} && exec "$SHELL" -l`;
      return {
        cwd,
        spec: {
          file: "ssh",
          args: ["-tt", place.host, command],
          ...common,
          cwd: (this.options.homeDir ?? homedir)(),
        },
      };
    }
    if (!isAbsolute(cwd)) throw new LaunchRefusal("bad_request", `the folder is not an absolute path: ${cwd}`);
    if (!isDirectory(cwd)) throw new LaunchRefusal("not_found", `not an existing directory: ${cwd}`);
    const launch = shellLaunch(defaultShell(env, platform), platform);
    return { cwd, spec: { file: launch.file, args: launch.args, ...common } };
  }

  private createSession(cwd: string): Session {
    const screen = new Screen({ cols: 80, rows: 24, scrollback: SCREEN_SCROLLBACK, allowProposedApi: true });
    const serializer = new SerializeAddon();
    serializer.activate(screen as unknown as Parameters<SerializeAddon["activate"]>[0]);
    return {
      id: randomUUID(),
      cwd,
      followers: new Map(),
      pty: null,
      screen,
      serializer,
      flushed: Promise.resolve(),
      exit: null,
      cleanup: null,
      endTimer: null,
    };
  }

  private handlers(session: Session): PtyHandlers {
    return {
      onData: (data) => {
        // One queue, in order: each chunk is parsed after the one before it.
        session.flushed = session.flushed.then(
          () => new Promise<void>((parsed) => session.screen.write(data, parsed)),
        );
        this.tell(session, { terminalId: session.id, type: "output", data });
      },
      onExit: (exit) => this.exited(session, exit),
    };
  }

  private exited(session: Session, exit: TerminalExit): void {
    session.exit = exit;
    session.pty = null;
    if (session.endTimer !== null) clearTimeout(session.endTimer);
    session.endTimer = null;
    session.cleanup?.();
    session.cleanup = null;
    for (const [token, agent] of this.agents) {
      if (agent === session) this.agents.delete(token);
    }
    this.tell(session, { terminalId: session.id, type: "exit", exit });
    // The screen stays for a page that comes back to read how the process ended.
    this.dropWhenAbandoned(session);
  }

  /** A terminal that ended is forgotten once nobody is left to read its end. */
  private dropWhenAbandoned(session: Session): void {
    if (session.exit && session.followers.size === 0) this.drop(session);
  }

  private drop(session: Session): void {
    this.sessions.delete(session.id);
    session.cleanup?.();
    session.cleanup = null;
    if (session.endTimer !== null) clearTimeout(session.endTimer);
    session.endTimer = null;
    session.screen.dispose();
  }

  private resizeSession(session: Session, size: TerminalSize): void {
    session.pty?.resize(size.cols, size.rows);
    session.screen.resize(size.cols, size.rows);
  }

  /** The session `sender` follows, when it is that one. */
  private followed(terminalId: string, sender: TerminalSender): Session | null {
    const session = this.sessions.get(terminalId);
    return session?.followers.has(sender.id) ? session : null;
  }

  private tell(session: Session, event: TerminalHostEvent): void {
    for (const [id, sender] of session.followers) {
      try {
        sender.send(TERMINAL_EVENT_CHANNEL, event);
      } catch {
        // Gone without saying so.
        session.followers.delete(id);
      }
    }
  }

  private refusal(
    error: unknown,
    introduction: string,
  ): { ok: false; code: TerminalErrorCode; message: string } {
    if (error instanceof LaunchRefusal) return refused(error.code, error.message);
    this.options.log?.(`${introduction}: ${describe(error)}`);
    return refused("internal", `${introduction}: ${describe(error)}`);
  }
}

function refused(code: TerminalErrorCode, message: string) {
  return { ok: false as const, code, message };
}

/** The size as a terminal takes it, or null when it is not one. */
export function dimensionsOf(size: unknown): TerminalSize | null {
  if (!isRecord(size)) return null;
  const { cols, rows } = size;
  const isDimension = (value: unknown): value is number =>
    typeof value === "number" && Number.isInteger(value) && value >= 1 && value <= MAX_DIMENSION;
  return isDimension(cols) && isDimension(rows) ? { cols, rows } : null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isDirectory(path: string): boolean {
  try {
    return statSync(path).isDirectory();
  } catch {
    return false;
  }
}

function describe(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
