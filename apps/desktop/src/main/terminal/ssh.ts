import { execFile } from "child_process";
import { shellQuote } from "./agent-hooks";

/** How a command on an SSH host ended. `code` is -1 when it did not end on its own. */
export interface SshAnswer {
  code: number;
  stdout: string;
}

/**
 * Runs `command` on `host` with the system `ssh`, without a terminal. Nothing
 * can stop and ask: a host that wants a password or a key passphrase is not
 * answered, and the command ends as failed. `~/.ssh/config` applies as always.
 *
 * Resolves to null when `ssh` itself could not run.
 */
export function sshExec(host: string, command: string, timeoutMs = 20_000): Promise<SshAnswer | null> {
  const args = ["-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host, command];
  return new Promise((resolve) => {
    execFile(
      "ssh",
      args,
      { timeout: timeoutMs, encoding: "utf8", maxBuffer: 8 << 20 },
      (error, stdout) => {
        if (error && typeof error.code !== "number") return resolve(null);
        resolve({ code: error ? (error.code as number) : 0, stdout });
      },
    );
  });
}

/**
 * Whether each of `paths` is a folder on `host`. Resolves to null when the
 * host could not be asked, so the caller goes on without the answer.
 */
export async function sshFoldersExist(host: string, paths: string[]): Promise<boolean[] | null> {
  if (paths.length === 0) return [];
  const test = paths
    .map((path) => `[ -d ${shellQuote(path)} ] && printf y || printf n`)
    .join("; ");
  const answer = await sshExec(host, test);
  if (!answer || answer.code !== 0 || answer.stdout.length !== paths.length) return null;
  return [...answer.stdout].map((mark) => mark === "y");
}
