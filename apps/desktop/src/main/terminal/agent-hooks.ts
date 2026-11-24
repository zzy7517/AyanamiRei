import { randomBytes, timingSafeEqual } from "crypto";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "http";

/** The Claude Code hook events that report an agent's lifecycle. */
const HOOK_EVENTS = [
  "SessionStart",
  "UserPromptSubmit",
  "Stop",
  "StopFailure",
  "SessionEnd",
] as const;
type HookEvent = (typeof HOOK_EVENTS)[number];

const HOOK_PATH = "/hook";
const PAYLOAD_LIMIT_BYTES = 2 << 20;
const HOOK_TIMEOUT_SECONDS = 2;

/** The task an agent that finished a turn works on. */
export interface TurnEnd {
  taskId: string;
  workspaceId: string;
}

/** Wraps `text` in single quotes for a POSIX shell. */
export function shellQuote(text: string): string {
  return `'${text.replaceAll("'", `'\\''`)}'`;
}

/**
 * The command that posts a hook's input to the receiver. It always exits 0
 * and prints nothing: a failing hook would block the prompt or keep Claude
 * working, and its output would land in Claude's context. `-q` ignores
 * `~/.curlrc`, and `--noproxy` keeps a proxy out of the loopback request.
 */
export function hookCommand(port: number, token: string): string {
  const authorization = shellQuote(`Authorization: Bearer ${token}`);
  return (
    `curl -q -sS --noproxy '*' -m ${HOOK_TIMEOUT_SECONDS} -o /dev/null ` +
    `-H 'Content-Type: application/json' -H 'Expect:' -H ${authorization} ` +
    `--data-binary @- http://127.0.0.1:${port}${HOOK_PATH} || true`
  );
}

/**
 * Receives what an agent's hooks post while it runs. Each agent is launched
 * with its own token, so a report names the task of exactly that agent.
 *
 * It listens on the loopback interface only. Nothing is written to disk.
 */
export class AgentHooks {
  private readonly server: Server;
  private readonly portValue: number;
  /** The task of each running agent, by the token it reports with. */
  private readonly tasks = new Map<string, TurnEnd>();
  /** Called with the token an agent reports with, and its task, when a turn ends. */
  onTurnEnd: ((token: string, turnEnd: TurnEnd) => void) | null = null;

  private constructor(server: Server, port: number) {
    this.server = server;
    this.portValue = port;
  }

  /** Starts the receiver, or rejects when the loopback interface has no port to give. */
  static async listen(): Promise<AgentHooks> {
    const server = createServer();
    const hooks = new AgentHooks(
      server,
      await new Promise<number>((resolve, reject) => {
        server.once("error", reject);
        server.listen(0, "127.0.0.1", () => {
          server.off("error", reject);
          const address = server.address();
          if (address && typeof address === "object") resolve(address.port);
          else reject(new Error("the hook receiver has no port"));
        });
      }),
    );
    server.on("request", hooks.onRequest);
    return hooks;
  }

  get port(): number {
    return this.portValue;
  }

  /**
   * Registers an agent about to start and returns the token its hooks report
   * with. The token stays valid until `release` is called.
   */
  register(task: TurnEnd): string {
    const token = randomBytes(24).toString("base64url");
    this.tasks.set(token, task);
    return token;
  }

  release(token: string): void {
    this.tasks.delete(token);
  }

  close(): void {
    this.server.close();
  }

  private readonly onRequest = (request: IncomingMessage, response: ServerResponse): void => {
    if (pathname(request.url) !== HOOK_PATH) return reply(response, 404);
    if (request.method !== "POST") return reply(response, 405, { Allow: "POST" });
    const token = this.authorized(request.headers.authorization);
    if (!token) return reply(response, 401);

    const chunks: Buffer[] = [];
    let size = 0;
    request.on("data", (chunk: Buffer) => {
      size += chunk.length;
      // Over the limit the request is cut off rather than answered.
      if (size > PAYLOAD_LIMIT_BYTES) request.destroy();
      else chunks.push(chunk);
    });
    request.on("error", () => undefined);
    request.on("end", () => {
      let payload: Record<string, unknown>;
      try {
        payload = JSON.parse(Buffer.concat(chunks).toString("utf8")) as Record<string, unknown>;
      } catch {
        return reply(response, 400);
      }
      this.record(token, payload);
      reply(response, 204);
    });
  };

  private record(token: string, payload: Record<string, unknown>): void {
    const event = payload.hook_event_name;
    // A Stop that followed a Stop hook's block is not the end of the turn.
    const ended =
      event === "StopFailure" || (event === "Stop" && payload.stop_hook_active !== true);
    if (!ended || !this.isHookEvent(event)) return;
    const task = this.tasks.get(token);
    if (task) this.onTurnEnd?.(token, task);
  }

  private isHookEvent(event: unknown): event is HookEvent {
    return (HOOK_EVENTS as readonly unknown[]).includes(event);
  }

  /** The token in `header`, when it is one this receiver minted. */
  private authorized(header: string | undefined): string | null {
    const scheme = "Bearer ";
    if (!header?.startsWith(scheme)) return null;
    const presented = Buffer.from(header.slice(scheme.length));
    let matched: string | null = null;
    for (const token of this.tasks.keys()) {
      const minted = Buffer.from(token);
      if (minted.length === presented.length && timingSafeEqual(minted, presented)) matched = token;
    }
    return matched;
  }
}

function pathname(url: string | undefined): string {
  try {
    return new URL(url ?? "", "http://127.0.0.1").pathname;
  } catch {
    return "";
  }
}

function reply(response: ServerResponse, status: number, headers: Record<string, string> = {}): void {
  // No body: a hook's answer is not read, and 204 promises none.
  response.writeHead(status, headers);
  response.end();
}
