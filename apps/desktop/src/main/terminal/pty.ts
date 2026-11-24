import type { IPty } from "node-pty";
import type { TerminalExit } from "../../shared/terminal";

/** A terminal's process, as its owner drives it. */
export interface PtyProcess {
  readonly pid: number;
  write(data: string): void;
  resize(cols: number, rows: number): void;
  /**
   * Sends `signal` to the process and to the group it leads. With no signal
   * the process is ended at once.
   */
  kill(signal?: string): void;
}

export interface PtySpec {
  file: string;
  args: string[];
  cwd: string;
  env: NodeJS.ProcessEnv;
  cols: number;
  rows: number;
}

export interface PtyHandlers {
  onData(data: string): void;
  onExit(exit: TerminalExit): void;
}

/** Starts a process in a pseudo-terminal. The app uses node-pty; tests pass a fake. */
export interface PtyFactory {
  spawn(spec: PtySpec, handlers: PtyHandlers): Promise<PtyProcess>;
}

/**
 * node-pty, loaded when the first terminal opens: a machine that cannot load
 * it gets a window and a refusal, not a start-up that fails.
 */
export const nodePtyFactory: PtyFactory = {
  async spawn(spec, handlers) {
    const loaded = await import("node-pty");
    // A CommonJS load wraps the module in `default`.
    const nodePty: typeof import("node-pty") =
      (loaded as { default?: typeof import("node-pty") }).default ?? loaded;
    const pty: IPty = nodePty.spawn(spec.file, spec.args, {
      name: "xterm-256color",
      cols: spec.cols,
      rows: spec.rows,
      cwd: spec.cwd,
      env: spec.env,
    });
    pty.onData((data) => handlers.onData(data));
    pty.onExit(({ exitCode, signal }) => handlers.onExit({ exitCode, signal }));
    const { pid } = pty;
    return {
      pid,
      write: (data) => pty.write(data),
      resize: (cols, rows) => pty.resize(cols, rows),
      kill: (signal) => {
        if (process.platform !== "win32") signalGroup(pid, signal);
        pty.kill(signal);
      },
    };
  },
};

/**
 * The process leads its group, so a signal to `-pid` reaches what it started
 * without job control. Nothing is sent once the group is gone.
 */
function signalGroup(pid: number, signal?: string): void {
  try {
    process.kill(-pid, signal);
  } catch {
    // The group has no member left.
  }
}
