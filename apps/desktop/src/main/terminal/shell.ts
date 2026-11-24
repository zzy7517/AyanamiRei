import { accessSync, constants as fsConstants, statSync } from "fs";
import { basename } from "path";
import { childProcessEnv } from "../daemon-spawn-env";

/** Shells that start as a login shell with `-l`, which runs the user's own startup files. */
const LOGIN_SHELLS = new Set(["bash", "zsh", "sh", "dash", "ksh", "fish", "csh", "tcsh"]);

/** The system's POSIX shell, for a user shell that takes none of the flags used here. */
export const POSIX_SHELL = "/bin/sh";

/** The shell a terminal runs: `$SHELL` when it is an executable file, else the system's. */
export function defaultShell(env: NodeJS.ProcessEnv, platform: NodeJS.Platform = process.platform): string {
  if (env.SHELL && isExecutableFile(env.SHELL)) return env.SHELL;
  if (platform === "win32") return "powershell.exe";
  return POSIX_SHELL;
}

/** Whether the shell takes `-l` and `-c` as bash does, so a command can run in it with the user's environment. */
export function isPosixShell(shell: string, platform: NodeJS.Platform = process.platform): boolean {
  return platform !== "win32" && LOGIN_SHELLS.has(basename(shell));
}

/** How a terminal starts an interactive shell. */
export function shellLaunch(
  shell: string,
  platform: NodeJS.Platform = process.platform,
): { file: string; args: string[] } {
  if (platform === "win32") return { file: shell, args: ["-NoLogo"] };
  // A shell outside the known ones keeps its own start-up: a flag it does not
  // know would end the terminal at once.
  return { file: shell, args: isPosixShell(shell, platform) ? ["-l"] : [] };
}

/** What every process of a terminal starts with. */
export function terminalEnv(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  return { ...childProcessEnv(env), TERM: "xterm-256color", COLORTERM: "truecolor" };
}

function isExecutableFile(path: string): boolean {
  try {
    if (!statSync(path).isFile()) return false;
    accessSync(path, fsConstants.X_OK);
    return true;
  } catch {
    return false;
  }
}
