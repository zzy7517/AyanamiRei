import { mkdtempSync, readdirSync, rmSync, statSync, writeFileSync } from "fs";
import { homedir, tmpdir } from "os";
import { isAbsolute, join, resolve, sep } from "path";
import type { TerminalErrorCode, TerminalHostTarget } from "../../shared/terminal";
import { hookCommand, shellQuote, type AgentHooks } from "./agent-hooks";
import type { PtySpec } from "./pty";
import { POSIX_SHELL, defaultShell, isPosixShell, shellLaunch, terminalEnv } from "./shell";
import { sshFoldersExist } from "./ssh";
import { renderTaskBrief } from "./task-brief";

export type AgentTarget = Extract<TerminalHostTarget, { kind: "agent" }>;

/** Claude Code's hook events, as its settings file groups them. */
const HOOK_EVENTS = ["SessionStart", "UserPromptSubmit", "Stop", "StopFailure", "SessionEnd"];

/** Carries the script that starts the agent to the shell that runs it. */
const AGENT_START_VARIABLE = "AYANAMI_AGENT_START";

/** Claude Code names a project's folder by this many characters, then a hash. */
const CLAUDE_BUCKET_CUT = 200;

/** What a terminal starts for an agent, and what to do once it has ended. */
export interface LaunchPlan {
  spec: PtySpec;
  /** Called when the process has ended. */
  cleanup(): void;
  /** Set for an agent: how its hooks report, and the task they report on. */
  agent?: { token: string; task: { taskId: string; workspaceId: string } };
}

export class LaunchRefusal extends Error {
  constructor(
    readonly code: TerminalErrorCode,
    message: string,
  ) {
    super(message);
  }
}

export interface AgentLaunchInput {
  target: AgentTarget;
  env: NodeJS.ProcessEnv;
  hooks: AgentHooks;
  platform?: NodeJS.Platform;
}

/**
 * Starts an agent's command line, Claude Code, in the first of a task's
 * trees, told about the task through an appended system prompt:
 *
 * - it runs with permissions bypassed;
 * - it continues Claude Code's latest conversation for that folder when there is one;
 * - the task's other trees are additional working directories;
 * - its hooks report each finished turn to the app.
 *
 * On a POSIX machine the agent runs in the user's own shell environment,
 * started as a login shell, so it finds what the user's terminal finds. When
 * it exits, the terminal stays as a shell in the tree, with what was printed
 * still on screen. On an SSH host the same happens there, with the system
 * `ssh`, and nothing is installed on the host.
 */
export async function planAgentLaunch(input: AgentLaunchInput): Promise<LaunchPlan> {
  const { target, env, hooks } = input;
  const platform = input.platform ?? process.platform;
  const { cwd, trees, task, place } = target;
  if (task.id === "") throw new LaunchRefusal("bad_request", "the agent terminal names no task");
  const remote = place.kind === "ssh";
  if (!remote && !isDirectory(cwd)) {
    throw new LaunchRefusal("tree_not_ready", `the tree is not a folder on this computer: ${cwd}`);
  }

  const projects = join(claudeConfigDir(env, cwd, platform), "projects");
  const bucket = claudeProjectBucket(cwd);
  let hasSession: boolean;
  if (remote) {
    // The host is asked without a terminal. When it cannot be asked, the
    // agent simply starts a new conversation.
    const named = bucket.length <= CLAUDE_BUCKET_CUT ? [cwd, join(projects, bucket)] : [cwd];
    const folders = await sshFoldersExist(place.host, named);
    if (folders && !folders[0]) {
      throw new LaunchRefusal("tree_not_ready", `the tree is not a folder on ${place.host}: ${cwd}`);
    }
    hasSession = folders ? folders[1] === true : false;
  } else {
    hasSession = hasClaudeSession(projects, bucket);
  }

  const token = hooks.register({ taskId: task.id, workspaceId: task.workspaceId });
  const command = hookCommand(hooks.port, token);
  const group = [{ hooks: [{ type: "command", command }] }];
  const settings = JSON.stringify({
    hooks: Object.fromEntries(HOOK_EVENTS.map((event) => [event, group])),
    skipDangerousModePermissionPrompt: true,
  });

  const otherDirs = trees.slice(1).map((tree) => tree.path);
  const agent = [
    "claude",
    "--permission-mode",
    "bypassPermissions",
    ...(hasSession ? ["--continue"] : []),
    ...(remote ? otherDirs : otherDirs.filter(isDirectory)).flatMap((dir) => ["--add-dir", dir]),
    "--append-system-prompt",
    renderTaskBrief(task, trees),
  ];
  const launch = { cwd, agent, settings, env: terminalEnv(env), platform };

  const plan = remote
    ? sshAgentSpec({ ...launch, host: place.host, port: hooks.port })
    : platform === "win32"
      ? windowsAgentSpec(launch)
      : posixAgentSpec(launch);
  return {
    spec: plan.spec,
    cleanup: () => {
      plan.cleanup?.();
      hooks.release(token);
    },
    agent: { token, task: { taskId: task.id, workspaceId: task.workspaceId } },
  };
}

/**
 * `agent` as a shell command, ending with the settings file the script
 * writes. That path is the script's own variable, so it stays unquoted.
 */
function agentCommand(agent: string[]): string {
  return `${agent.map(shellQuote).join(" ")} --settings "$d/settings.json"`;
}

/**
 * The script that starts the agent, for a POSIX shell. It writes the settings
 * file into a private temporary folder, removes it once the agent has ended,
 * and leaves an interactive shell behind.
 */
function agentStartScript(input: { cwd: string; command: string; settings: string; then: string }): string {
  return [
    `d=$(umask 077 && mktemp -d "\${TMPDIR:-/tmp}/multica-agent-session.XXXXXXXX") || exit 1`,
    `printf %s ${shellQuote(input.settings)} > "$d/settings.json" || exit 1`,
    `cd -- ${shellQuote(input.cwd)} || exit 1`,
    input.command,
    `rm -rf "$d"`,
    `exec ${input.then}`,
  ].join("\n");
}

interface AgentSpec {
  spec: PtySpec;
  cleanup?(): void;
}

function posixAgentSpec(input: AgentSpecInput): AgentSpec {
  const shell = defaultShell(input.env, input.platform);
  // The agent needs the environment of an interactive login shell, so it runs
  // in a shell that can start one. A user shell that cannot is only what the
  // user is left in afterwards.
  const starter = isPosixShell(shell, input.platform) ? shell : POSIX_SHELL;
  const after = shellLaunch(shell, input.platform);
  const script = agentStartScript({
    cwd: input.cwd,
    command: agentCommand(input.agent),
    settings: input.settings,
    then: [after.file, ...after.args].map(shellQuote).join(" "),
  });
  return {
    spec: {
      file: starter,
      args: ["-l", "-i", "-c", `exec sh -c "$${AGENT_START_VARIABLE}"`],
      cwd: input.cwd,
      env: { ...input.env, [AGENT_START_VARIABLE]: script },
      cols: 80,
      rows: 24,
    },
  };
}

function sshAgentSpec(input: AgentSpecInput & { host: string; port: number }): AgentSpec {
  const script = agentStartScript({
    cwd: input.cwd,
    command: agentCommand(input.agent),
    settings: input.settings,
    // The host's own login shell, as `ssh` resolves it.
    then: '"$SHELL" -l',
  });
  const start =
    `env ${AGENT_START_VARIABLE}=${shellQuote(script)} ` +
    '"$SHELL" -l -i -c \'exec sh -c "$AYANMI_AGENT_START"\'';
  return {
    spec: {
      file: "ssh",
      // The hooks post to 127.0.0.1 on the host. A forwarded port brings them
      // to this app's receiver, on the same number, so the command is the same.
      args: ["-tt", "-R", `${input.port}:127.0.0.1:${input.port}`, input.host, start],
      cwd: homedir(),
      env: input.env,
      cols: 80,
      rows: 24,
    },
  };
}

interface AgentSpecInput {
  cwd: string;
  agent: string[];
  settings: string;
  env: NodeJS.ProcessEnv;
  platform: NodeJS.Platform;
}

function windowsAgentSpec(input: AgentSpecInput): AgentSpec {
  const dir = mkdtempSync(join(tmpdir(), "multica-agent-session-"));
  writeFileSync(join(dir, "settings.json"), input.settings, { mode: 0o600 });
  return {
    spec: {
      file: input.env.COMSPEC || "cmd.exe",
      args: ["/d", "/s", "/c", [...input.agent, "--settings", join(dir, "settings.json")].map(windowsQuote).join(" ")],
      cwd: input.cwd,
      env: input.env,
      cols: 80,
      rows: 24,
    },
    cleanup: () => rmSync(dir, { recursive: true, force: true }),
  };
}

/** `arg` as `cmd.exe` passes it on: in double quotes, with its own quotes doubled. */
function windowsQuote(arg: string): string {
  return `"${arg.replaceAll('"', '""')}"`;
}

/** Claude Code's config folder: `CLAUDE_CONFIG_DIR`, else `.claude` in the home directory. */
function claudeConfigDir(env: NodeJS.ProcessEnv, cwd: string, platform: NodeJS.Platform): string {
  const configured = env.CLAUDE_CONFIG_DIR?.trim();
  if (configured) return isAbsolute(configured) ? configured : resolve(cwd, configured);
  const home = platform === "win32" ? env.USERPROFILE || homedir() : env.HOME || homedir();
  return join(home, ".claude");
}

/** Whether Claude Code keeps conversations for `bucket`, a folder's name under `projects`. */
function hasClaudeSession(projects: string, bucket: string): boolean {
  if (bucket.length <= CLAUDE_BUCKET_CUT) return isDirectory(join(projects, bucket));
  // A long name is cut and hashed, so any folder starting with the cut counts.
  const prefix = `${bucket.slice(0, CLAUDE_BUCKET_CUT)}-`;
  const entries = readdirSafe(projects);
  return entries.some((entry) => entry.startsWith(prefix) && isDirectory(join(projects, entry)));
}

/** Names a folder the way Claude Code does: what is not a letter or a digit becomes `-`. */
export function claudeProjectBucket(dir: string): string {
  let path = dir.split(sep).join("/");
  if (path !== "/" && !/^[A-Za-z]:\/$/.test(path)) path = path.replace(/\/+$/, "");
  return path.replace(/[^A-Za-z0-9]/g, "-");
}

function readdirSafe(path: string): string[] {
  try {
    return readdirSync(path);
  } catch {
    return [];
  }
}

function isDirectory(path: string): boolean {
  try {
    return statSync(path).isDirectory();
  } catch {
    return false;
  }
}
