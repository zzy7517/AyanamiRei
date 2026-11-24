import { execFile } from "child_process";
import { createHash } from "crypto";
import { mkdirSync } from "fs";
import {
  copyFile,
  lstat,
  mkdir,
  mkdtemp,
  readdir,
  readFile,
  realpath,
  rm,
  rmdir,
  stat,
  utimes,
  writeFile,
} from "fs/promises";
import { homedir, tmpdir } from "os";
import * as nodePath from "path";
import { posix } from "path";
import { promisify } from "util";
import { childProcessEnv } from "../daemon-spawn-env";
import { shellQuote } from "../terminal/agent-hooks";

const execFileAsync = promisify(execFile);

/** The most a command is allowed to print. */
const OUTPUT_LIMIT = 64 << 20;

/** How long connecting to an SSH host may take. */
const CONNECT_TIMEOUT_S = 10;

/** How long a small file operation on an SSH host may take, once connected. */
const STEP_TIMEOUT_MS = 60_000;

/** How long the connection that the calls for one host share stays open for the next one. */
const CONTROL_PERSIST_S = 300;

/** How long one git command may take: adding a worktree checks out a tree and runs hooks. */
const GIT_TIMEOUT_MS = 10 * 60_000;

/** The exit status ssh uses for its own failures, among which a host it could not reach. */
const SSH_FAILURE = 255;

/** The folder the shared connections wait in; null when there cannot be one. */
let socketFolder: string | null | undefined;

/** What a command printed and how it ended. */
export interface CommandRun {
  /** 0 when it did what was asked. */
  code: number;
  stdout: string;
  stderr: string;
}

/** The place itself could not do what was asked: it could not be reached, or has no git. */
export class PlaceError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "PlaceError";
  }
}

/**
 * The path rules of a place. An SSH host has its own, which are not those of
 * the computer the app runs on.
 */
export interface PathRules {
  basename(path: string, suffix?: string): string;
  dirname(path: string): string;
  isAbsolute(path: string): boolean;
  join(...parts: string[]): string;
  normalize(path: string): string;
  resolve(...parts: string[]): string;
  readonly sep: string;
}

/** What stands at a path. A link is "other" even where it leads to a folder. */
export type Entry = "dir" | "other";

/**
 * Where a project's folder is: this computer, or an SSH host. Everything a
 * task's tree needs from it goes through here, so that a tree is built the
 * same way wherever its project is.
 *
 * `git` answers with the exit status of a command that ran, and throws a
 * `PlaceError` only when the place itself could not run it. The other methods
 * throw for anything that went wrong.
 */
export interface Place {
  /** How messages name it: "this computer", "the SSH host lab". */
  readonly name: string;
  readonly paths: PathRules;
  /** The home folder of the user the app runs as there. */
  home(): Promise<string>;
  /** Runs git in `dir`, with `env` added to the environment. */
  git(dir: string, args: string[], env?: Record<string, string>): Promise<CommandRun>;
  /** What stands at each path, null where nothing does. A link is not followed. */
  stat(paths: string[]): Promise<(Entry | null)[]>;
  /** The names in a folder. */
  list(dir: string): Promise<string[]>;
  /** Makes a folder and those it needs. Already there is fine. */
  mkdir(dir: string): Promise<void>;
  /** Removes an empty folder or a file. Fails on a folder that is not empty. */
  remove(path: string): Promise<void>;
  /** Removes a folder when it is empty. Says whether it did. */
  removeEmpty(dir: string): Promise<boolean>;
  /** A text file, empty when it is not there. */
  read(path: string): Promise<string>;
  write(path: string, text: string): Promise<void>;
  /** Copies a file, keeping the time it was last changed. */
  copy(from: string, to: string): Promise<void>;
  /** Each path with its links resolved, null where it does not stand. */
  resolve(paths: string[]): Promise<(string | null)[]>;
  /** A new empty folder for files that live only as long as one operation. */
  tempDir(): Promise<string>;
  removeRecursively(path: string): Promise<void>;
}

/** How a child ended, however it ended. */
interface Ended {
  /** Null when it did not end on its own. */
  code: number | null;
  /** False when it could not be started at all. */
  started: boolean;
  timedOut: boolean;
  stdout: string;
  stderr: string;
  /** Why it could not be started, when it could not. */
  reason: string;
}

async function end(file: string, args: string[], options: { timeout: number; env?: NodeJS.ProcessEnv }): Promise<Ended> {
  try {
    const { stdout, stderr } = await execFileAsync(file, args, {
      ...options,
      encoding: "utf8",
      maxBuffer: OUTPUT_LIMIT,
    });
    return { code: 0, started: true, timedOut: false, stdout, stderr, reason: "" };
  } catch (error) {
    const failure = error as NodeJS.ErrnoException & { stdout?: string; stderr?: string; killed?: boolean };
    const code = typeof failure.code === "number" ? failure.code : null;
    return {
      code,
      started: code !== null || failure.code === "ERR_CHILD_PROCESS_STDIO_MAXBUFFER",
      timedOut: failure.killed === true,
      stdout: failure.stdout ?? "",
      stderr: failure.stderr ?? "",
      reason: failure.message,
    };
  }
}

/** The first line of what a command complained about. */
export function firstLine(text: string): string {
  return text.trim().split("\n")[0]?.trim() ?? "";
}

/** The lines a command printed, without the last line break. */
function lines(text: string): string[] {
  return text === "" ? [] : text.replace(/\n$/, "").split("\n");
}

async function kindOf(path: string): Promise<Entry | null> {
  try {
    return (await lstat(path)).isDirectory() ? "dir" : "other";
  } catch {
    return null;
  }
}

/** The project folders on the computer the app runs on. */
export function localPlace(): Place {
  const env = childProcessEnv(process.env);
  return {
    name: "this computer",
    paths: nodePath,
    home: async () => homedir(),
    git: async (dir, args, extra = {}) => {
      const ended = await end("git", ["-C", dir, ...args], { env: { ...env, ...extra }, timeout: GIT_TIMEOUT_MS });
      if (!ended.started) throw new PlaceError(`git could not be started: ${ended.reason}`);
      if (ended.timedOut) throw new PlaceError("git did not finish in time");
      return { code: ended.code ?? 1, stdout: ended.stdout, stderr: ended.stderr };
    },
    stat: (paths) => Promise.all(paths.map(kindOf)),
    list: (dir) => readdir(dir),
    mkdir: async (dir) => {
      await mkdir(dir, { recursive: true });
    },
    remove: async (path) => {
      try {
        await rmdir(path);
      } catch (error) {
        // Not a folder: a file, or a link, which is not followed.
        if ((error as NodeJS.ErrnoException).code !== "ENOTDIR") throw error;
        await rm(path);
      }
    },
    removeEmpty: async (dir) => {
      try {
        await rmdir(dir);
        return true;
      } catch {
        return false;
      }
    },
    read: async (path) => {
      try {
        return await readFile(path, "utf8");
      } catch {
        return "";
      }
    },
    write: (path, text) => writeFile(path, text, { mode: 0o644 }),
    copy: async (from, to) => {
      await copyFile(from, to);
      const times = await stat(from);
      await utimes(to, times.atime, times.mtime);
    },
    resolve: (paths) => Promise.all(paths.map((path) => realpath(path).catch(() => null))),
    tempDir: () => mkdtemp(nodePath.join(tmpdir(), "ayanami-trees-")),
    removeRecursively: (path) => rm(path, { recursive: true, force: true }),
  };
}

/**
 * What lets the calls for one host share a connection: setting one up is
 * most of what a call costs, and making a tree takes a dozen. Nothing when
 * the connections cannot wait in a folder of this user's own.
 */
function sharedConnection(host: string): string[] {
  // The OpenSSH of Windows cannot share connections.
  if (process.platform === "win32") return [];
  if (socketFolder === undefined) {
    socketFolder = null;
    try {
      const owner = typeof process.getuid === "function" ? process.getuid() : "user";
      const folder = nodePath.join(tmpdir(), `ayanami-ssh-${owner}`);
      mkdirSync(folder, { recursive: true, mode: 0o700 });
      socketFolder = folder;
    } catch {
      // A connection of its own for every call is only slower.
    }
  }
  if (socketFolder === null) return [];
  const socket = nodePath.join(socketFolder, createHash("sha1").update(host).digest("hex").slice(0, 16));
  return [
    "-o",
    "ControlMaster=auto",
    "-o",
    `ControlPath=${socket}`,
    "-o",
    `ControlPersist=${CONTROL_PERSIST_S}`,
  ];
}

/**
 * Runs `body` in a POSIX shell on `host` with the system `ssh`. Nothing can
 * stop and ask: a host that wants a password or a key passphrase is not
 * answered, and the call fails. `~/.ssh/config` applies as always.
 *
 * The shell's exit status travels at the end of what the command printed,
 * because ssh's own status says nothing about the command: 255 is also what
 * a command exits with when it is killed by a signal.
 */
async function sshRun(host: string, body: string, timeoutMs: number): Promise<CommandRun> {
  const script = `${body}\nprintf '\\n@@%s\\n' "$?"`;
  const args = [
    "-o",
    "BatchMode=yes",
    "-o",
    `ConnectTimeout=${CONNECT_TIMEOUT_S}`,
    "-o",
    "ServerAliveInterval=15",
    "-o",
    "ServerAliveCountMax=4",
    ...sharedConnection(host),
    host,
    `sh -c ${shellQuote(script)}`,
  ];
  const ended = await end("ssh", args, { timeout: timeoutMs + CONNECT_TIMEOUT_S * 1000 });
  if (!ended.started) throw new PlaceError(`ssh could not be started: ${ended.reason}`);
  if (ended.timedOut) throw new PlaceError("a command on the SSH host did not finish in time");
  const mark = ended.stdout.lastIndexOf("\n@@");
  if (mark < 0 || !/^\n@@-?\d+\n$/.test(ended.stdout.slice(mark))) {
    const reason = firstLine(ended.stderr) || (ended.code === SSH_FAILURE ? "ssh failed" : "the connection was lost");
    throw new PlaceError(`the SSH host could not be reached: ${reason}`);
  }
  return { code: Number(ended.stdout.slice(mark + 3, -1)), stdout: ended.stdout.slice(0, mark), stderr: ended.stderr };
}

/** The project folders on an SSH host. Every operation there is one `ssh` call. */
export function sshPlace(host: string): Place {
  const name = `the SSH host ${host}`;
  // A small operation: it failed when the shell says so.
  const step = async (body: string, what: string): Promise<string> => {
    let answer: CommandRun;
    try {
      answer = await sshRun(host, body, STEP_TIMEOUT_MS);
    } catch (error) {
      throw error instanceof PlaceError ? new PlaceError(error.message.replace("the SSH host", name)) : error;
    }
    if (answer.code !== 0) throw new PlaceError(`could not ${what} on ${name}: ${firstLine(answer.stderr)}`);
    return answer.stdout;
  };
  const quoted = (paths: string[]) => paths.map(shellQuote).join(" ");
  return {
    name,
    paths: posix,
    home: async () => lines(await step(`printf '%s\\n' "$HOME"`, "read the home folder"))[0] ?? "",
    git: (dir, args, env = {}) => {
      const additions = Object.entries(env)
        .map(([key, value]) => `${key}=${shellQuote(value)} `)
        .join("");
      const words = args.map((arg) => ` ${shellQuote(arg)}`).join("");
      return sshRun(host, `${additions}git -C ${shellQuote(dir)}${words}`, GIT_TIMEOUT_MS).catch((error: unknown) => {
        if (!(error instanceof PlaceError)) throw error;
        throw new PlaceError(error.message.replace("the SSH host", name));
      });
    },
    stat: async (paths) => {
      if (paths.length === 0) return [];
      const marks = lines(
        await step(
          `for p in ${quoted(paths)}; do\n` +
            'if [ -L "$p" ]; then printf \'o\\n\'\nelif [ -d "$p" ]; then printf \'d\\n\'\n' +
            'elif [ -e "$p" ]; then printf \'o\\n\'\nelse printf \'m\\n\'\nfi\ndone',
          "look at the task's folder",
        ),
      );
      if (marks.length !== paths.length) throw new PlaceError(`could not look at the task's folder on ${name}`);
      return marks.map((mark) => (mark === "d" ? "dir" : mark === "o" ? "other" : null));
    },
    list: async (dir) =>
      lines(
        await step(
          `cd ${shellQuote(dir)} && {\nfor f in .* *; do\n` +
            'if [ "$f" != . ] && [ "$f" != .. ] && { [ -e "$f" ] || [ -L "$f" ]; }; then printf \'%s\\n\' "$f"; fi\n' +
            "done\ntrue\n}",
          `list ${dir}`,
        ),
      ),
    mkdir: async (dir) => {
      await step(`mkdir -p ${shellQuote(dir)}`, `make the folder ${dir}`);
    },
    remove: async (path) => {
      await step(`[ -d ${shellQuote(path)} ] && [ ! -L ${shellQuote(path)} ] && rmdir ${shellQuote(path)} || rm -f ${shellQuote(path)}`, `remove ${path}`);
    },
    removeEmpty: async (dir) =>
      (await step(`if rmdir ${shellQuote(dir)} 2>/dev/null; then printf y; else printf n; fi`, `remove the folder ${dir}`)) === "y",
    read: async (path) => step(`cat ${shellQuote(path)} 2>/dev/null || true`, `read ${path}`),
    write: async (path, text) => {
      await step(`( umask 022; printf '%s' ${shellQuote(text)} > ${shellQuote(path)} )`, `write ${path}`);
    },
    copy: async (from, to) => {
      await step(`cp -p ${shellQuote(from)} ${shellQuote(to)}`, `copy ${from}`);
    },
    resolve: async (paths) => {
      if (paths.length === 0) return [];
      const found = lines(
        await step(
          `for p in ${quoted(paths)}; do\nr=$(readlink -f "$p" 2>/dev/null) && printf '%s\\n' "$r" || printf '\\n'\ndone`,
          "look at the task's folder",
        ),
      );
      if (found.length !== paths.length) throw new PlaceError(`could not look at the task's folder on ${name}`);
      return found.map((path) => path || null);
    },
    tempDir: async () =>
      lines(await step(`d=$(mktemp -d "\${TMPDIR:-/tmp}/ayanami-trees.XXXXXX") && printf '%s\\n' "$d"`, "make a temporary folder"))[0] ??
      "",
    removeRecursively: async (path) => {
      await step(`rm -rf ${shellQuote(path)}`, `remove ${path}`);
    },
  };
}
