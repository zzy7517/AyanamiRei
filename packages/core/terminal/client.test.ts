// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError, setApiInstance } from "../api";
import { noopLogger } from "../logger";
import { TerminalSession, type TerminalSessionState } from "./client";
import type { TerminalTicket } from "./protocol";

const READY = JSON.stringify({ type: "ready", terminalId: "t-1", pid: 4242, cols: 120, rows: 40 });

/** One socket per connection attempt. Tests drive it by hand: no network, no real handshake. */
class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  binaryType = "blob";
  sent: Array<string | Uint8Array> = [];
  closeCalls = 0;
  constructor(readonly url: string) {
    FakeWebSocket.instances.push(this);
  }
  send(data: string | Uint8Array) {
    this.sent.push(typeof data === "string" ? data : new Uint8Array(data));
  }
  close() {
    this.closeCalls += 1;
  }
  open() {
    this.onopen?.();
  }
  receiveText(text: string) {
    this.onmessage?.({ data: text });
  }
  receiveBytes(...bytes: number[]) {
    this.onmessage?.({ data: new Uint8Array(bytes).buffer });
  }
  /** The socket went away the way a network drop looks to a page. */
  drop() {
    this.onclose?.();
  }
  static get last(): FakeWebSocket {
    return FakeWebSocket.instances.at(-1)!;
  }
}

/** Rejects an address the way the browser's constructor does. */
class StrictWebSocket extends FakeWebSocket {
  constructor(url: string) {
    new URL(url);
    super(url);
  }
}

const createLogger = () => ({ ...noopLogger, warn: vi.fn(), info: vi.fn() });

const ticket: TerminalTicket = { ticket: "tk", expiresInSeconds: 120 };

/** Let the ticket request and the socket setup settle. */
const flush = () => vi.advanceTimersByTimeAsync(0);

function setup(overrides: { createTicket?: () => Promise<TerminalTicket | null> } = {}) {
  const createTicket = vi.fn(overrides.createTicket ?? (async () => ticket));
  const logger = createLogger();
  const session = new TerminalSession({
    createTicket,
    socketUrl: (t) => `wss://srv.test/terminal/ws?ticket=${t}`,
    logger,
  });
  const states: TerminalSessionState[] = [];
  session.subscribe(() => states.push(session.getState()));
  return { session, states, createTicket, logger };
}

/** Open a shell and bring it to `ready`; returns the socket it runs on. */
async function readySession(size: { cols: number; rows: number } = { cols: 120, rows: 40 }) {
  const world = setup();
  world.session.open("/srv/app", size);
  await flush();
  const socket = FakeWebSocket.last;
  socket.open();
  socket.receiveText(READY);
  return { ...world, socket };
}

beforeEach(() => {
  vi.useFakeTimers();
  // No jitter: every reconnect waits exactly its base delay.
  vi.spyOn(Math, "random").mockReturnValue(0.5);
  FakeWebSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeWebSocket as unknown as typeof WebSocket);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("TerminalSession lifecycle", () => {
  it("starts connecting, and opens the shell with the viewport's size once the socket is up", async () => {
    const { session, states, createTicket } = setup();

    session.open("/srv/app", { cols: 120, rows: 40 }, "d-1");
    expect(session.getState().status).toBe("connecting");
    expect(FakeWebSocket.instances).toHaveLength(0);

    await flush();
    expect(createTicket).toHaveBeenCalledTimes(1);
    const socket = FakeWebSocket.last;
    expect(socket.url).toBe("wss://srv.test/terminal/ws?ticket=tk");
    expect(socket.binaryType).toBe("arraybuffer");
    expect(socket.sent).toEqual([]);

    socket.open();
    expect(socket.sent).toEqual([
      '{"type":"open","cwd":"/srv/app","cols":120,"rows":40,"daemonId":"d-1"}',
    ]);
    expect(states.map((state) => state.status)).toEqual(["connecting"]);
  });

  it("leaves daemonId out when none was chosen", async () => {
    const { session } = setup();
    session.open("/srv/app");
    await flush();
    FakeWebSocket.last.open();
    expect(FakeWebSocket.last.sent).toEqual(['{"type":"open","cwd":"/srv/app","cols":80,"rows":24}']);
  });

  it("reports ready with the terminal the server bound it to", async () => {
    const { session, states } = await readySession();
    expect(session.getState()).toMatchObject({
      status: "ready",
      terminalId: "t-1",
      pid: 4242,
      exit: null,
      error: null,
      reconnectAttempt: 0,
    });
    expect(states.map((state) => state.status)).toEqual(["connecting", "ready"]);
  });

  it("opens an agent's terminal in a tree of a task, and reattaches to it after a drop", async () => {
    const { session } = setup();
    session.openAgent({ issue_id: "issue-1", tree_id: "tree-1" }, { cols: 100, rows: 30 });
    await flush();
    const socket = FakeWebSocket.last;
    socket.open();
    expect(socket.sent).toEqual([
      '{"type":"open","agent":{"issue_id":"issue-1","tree_id":"tree-1"},"cols":100,"rows":30}',
    ]);
    socket.receiveText(READY);

    socket.drop();
    await vi.advanceTimersByTimeAsync(1000);
    const reconnected = FakeWebSocket.last;
    reconnected.open();
    expect(reconnected.sent).toEqual(['{"type":"attach","terminalId":"t-1"}']);
    reconnected.receiveText(READY);
    expect(session.getState()).toMatchObject({ status: "ready", terminalId: "t-1" });
  });

  it("attaches to a running terminal instead of opening one, then brings it to the viewport's size", async () => {
    const { session } = setup();
    session.resize(100, 30);
    session.attach("t-1");
    await flush();
    const socket = FakeWebSocket.last;
    socket.open();
    expect(socket.sent).toEqual(['{"type":"attach","terminalId":"t-1"}']);
    socket.receiveText(READY);
    expect(session.getState()).toMatchObject({ status: "ready", terminalId: "t-1" });
    expect(socket.sent).toEqual([
      '{"type":"attach","terminalId":"t-1"}',
      '{"type":"resize","cols":100,"rows":30}',
    ]);
  });

  it("cannot run two terminals at once, and runs another once the first has ended", async () => {
    const { session, socket } = await readySession();
    expect(() => session.open("/elsewhere")).toThrow(/already running/);

    socket.receiveText(JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: 0 }));
    session.open("/elsewhere");
    await flush();
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(session.getState()).toMatchObject({ status: "connecting", terminalId: null, exit: null });
  });

  it("ignores events from a socket it no longer owns", async () => {
    const { session, socket, states } = await readySession();
    const before = states.length;
    session.dispose();
    socket.onmessage?.({ data: READY });
    socket.onclose?.();
    expect(states).toHaveLength(before);
    expect(session.getState().status).toBe("ready");
    await vi.advanceTimersByTimeAsync(60_000);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });
});

describe("TerminalSession input and output", () => {
  it("sends input as UTF-8 bytes, and only once the terminal is ready", async () => {
    const { session } = setup();
    session.open("/srv/app");
    session.sendInput("too soon");
    await flush();
    const socket = FakeWebSocket.last;
    socket.open();
    expect(socket.sent).toHaveLength(1);

    socket.receiveText(READY);
    session.sendInput("ls\r\n");
    session.sendInput(new Uint8Array([0x1b, 0x5b, 0x41]));
    session.sendInput("é中");
    expect(socket.sent.slice(1)).toEqual([
      new Uint8Array([0x6c, 0x73, 0x0d, 0x0a]),
      new Uint8Array([0x1b, 0x5b, 0x41]),
      new Uint8Array([0xc3, 0xa9, 0xe4, 0xb8, 0xad]),
    ]);
  });

  it("drops input while the terminal is only being reconnected", async () => {
    const { session, socket } = await readySession();
    socket.drop();
    expect(session.getState().status).toBe("connecting");
    session.sendInput("ls");
    expect(FakeWebSocket.last.sent).toHaveLength(1);
  });

  it("hands binary frames to output listeners as bytes, and nothing else to them", async () => {
    const { session, socket } = await readySession();
    const output = vi.fn();
    const unsubscribe = session.onOutput(output);

    socket.receiveBytes(0x24, 0x20);
    socket.receiveText(READY);
    socket.onmessage?.({ data: new Uint8Array([1, 2, 3, 4]).subarray(1, 3) });
    socket.onmessage?.({ data: 7 });

    expect(output.mock.calls).toEqual([[new Uint8Array([0x24, 0x20])], [new Uint8Array([2, 3])]]);

    unsubscribe();
    socket.receiveBytes(0x0a);
    expect(output).toHaveBeenCalledTimes(2);
  });

  it("ignores output that arrives after the session let go of the socket", async () => {
    const { session, socket } = await readySession();
    const output = vi.fn();
    session.onOutput(output);
    session.dispose();
    socket.onmessage?.({ data: new Uint8Array([1]).buffer });
    expect(output).not.toHaveBeenCalled();
  });
});

describe("TerminalSession resize", () => {
  it("sends each new size once", async () => {
    const { session, socket } = await readySession({ cols: 120, rows: 40 });

    session.resize(120, 40);
    session.resize(100, 30);
    session.resize(100, 30);
    session.resize(0, 30);
    session.resize(100.5, 30);
    session.resize(Number.NaN, 30);

    expect(socket.sent.slice(1)).toEqual(['{"type":"resize","cols":100,"rows":30}']);
  });

  it("keeps only the last size asked for before the terminal was ready", async () => {
    const { session } = setup();
    session.open("/srv/app", { cols: 120, rows: 40 });
    await flush();
    const socket = FakeWebSocket.last;
    socket.open();
    session.resize(110, 33);
    session.resize(101, 31);
    expect(socket.sent).toHaveLength(1);

    socket.receiveText(READY);
    expect(socket.sent).toEqual([
      '{"type":"open","cwd":"/srv/app","cols":120,"rows":40}',
      '{"type":"resize","cols":101,"rows":31}',
    ]);
  });
});

describe("TerminalSession exit and close", () => {
  it("reports an exit by status and stops there", async () => {
    const { session, socket, states } = await readySession();
    socket.receiveText(JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: 0 }));

    expect(session.getState()).toMatchObject({
      status: "exited",
      terminalId: "t-1",
      exit: { exitCode: 0, signal: null },
      closing: false,
    });
    expect(socket.closeCalls).toBe(1);
    expect(socket.onclose).toBeNull();
    expect(states.at(-1)?.status).toBe("exited");

    await vi.advanceTimersByTimeAsync(120_000);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("reports an exit by signal", async () => {
    const { session, socket } = await readySession();
    socket.receiveText(JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: null, signal: "SIGKILL" }));
    expect(session.getState()).toMatchObject({ status: "exited", exit: { exitCode: null, signal: "SIGKILL" } });
  });

  it("ends the process on request and settles on the exit that answers", async () => {
    const { session, socket } = await readySession();

    session.close();
    expect(socket.sent.slice(1)).toEqual(['{"type":"close","terminalId":"t-1"}']);
    expect(session.getState()).toMatchObject({ status: "ready", closing: true });

    socket.receiveText(JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: null, signal: "SIGTERM" }));
    expect(session.getState()).toMatchObject({
      status: "exited",
      closing: false,
      exit: { exitCode: null, signal: "SIGTERM" },
    });
  });

  it("counts the session ended when the socket goes after the end was asked for", async () => {
    const { session, socket } = await readySession();
    session.close();
    socket.drop();

    expect(session.getState()).toMatchObject({
      status: "exited",
      closing: false,
      exit: { exitCode: null, signal: null },
    });
    await vi.advanceTimersByTimeAsync(120_000);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("delivers a close asked for during a reconnection once the terminal is back", async () => {
    const { session, socket } = await readySession();
    socket.drop();
    session.close();
    expect(session.getState()).toMatchObject({ status: "connecting", closing: true });

    await vi.advanceTimersByTimeAsync(1_000);
    const reconnected = FakeWebSocket.last;
    reconnected.open();
    expect(reconnected.sent).toEqual(['{"type":"attach","terminalId":"t-1"}']);
    reconnected.receiveText(READY);
    expect(reconnected.sent).toEqual([
      '{"type":"attach","terminalId":"t-1"}',
      '{"type":"resize","cols":120,"rows":40}',
      '{"type":"close","terminalId":"t-1"}',
    ]);
  });

  it("gives up on an end the server never hears about, after as many tries as a first connection gets", async () => {
    const { session, socket } = await readySession();
    socket.drop();
    session.close();
    for (let tries = 2; tries <= 7; tries += 1) {
      await vi.advanceTimersByTimeAsync(31_000);
      expect(session.getState()).toMatchObject({ status: "connecting", closing: true });
      FakeWebSocket.last.drop();
    }

    await vi.advanceTimersByTimeAsync(31_000);
    FakeWebSocket.last.drop();

    expect(session.getState()).toMatchObject({
      status: "exited",
      closing: false,
      exit: { exitCode: null, signal: null },
    });
    await vi.advanceTimersByTimeAsync(120_000);
    expect(FakeWebSocket.instances).toHaveLength(8);
  });

  it("counts an end asked for before the first terminal as done once the tries run out", async () => {
    const { session, logger } = setup();
    session.open("/srv/app");
    await flush();
    session.close();
    for (let tries = 1; tries <= 7; tries += 1) {
      FakeWebSocket.last.drop();
      expect(session.getState()).toMatchObject({ status: "connecting", closing: true });
      await vi.advanceTimersByTimeAsync(31_000);
    }

    FakeWebSocket.last.drop();

    expect(session.getState()).toMatchObject({
      status: "exited",
      closing: false,
      exit: { exitCode: null, signal: null },
    });
    expect(logger.warn).not.toHaveBeenCalledWith("terminal: unavailable", expect.anything());
  });

  it("has nothing to close once the terminal has ended", async () => {
    const { session, socket } = await readySession();
    socket.receiveText(JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: 0 }));
    session.close();
    expect(socket.sent).toHaveLength(1);
    expect(session.getState().closing).toBe(false);
  });
});

describe("TerminalSession dispose", () => {
  it("detaches without ending the process", async () => {
    const { session, socket, states } = await readySession();
    const before = states.length;
    session.dispose();

    expect(socket.sent).toHaveLength(1);
    expect(socket.closeCalls).toBe(1);
    expect(socket.onclose).toBeNull();
    expect(socket.onmessage).toBeNull();
    // Nothing further reaches the listeners, not even from a late event.
    socket.onclose?.();
    expect(states).toHaveLength(before);
  });

  it("does not connect when the ticket arrives after it", async () => {
    let release!: (ticket: TerminalTicket) => void;
    const { session } = setup({
      createTicket: () => new Promise((resolve) => (release = resolve)),
    });
    session.open("/srv/app");
    session.dispose();
    release(ticket);
    await flush();
    expect(FakeWebSocket.instances).toHaveLength(0);
  });

  it("cancels a reconnect that was pending", async () => {
    const { session, socket } = await readySession();
    socket.drop();
    expect(session.getState().reconnectAttempt).toBe(1);
    session.dispose();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("does not connect a ticket requested for the run before a restart", async () => {
    const releases: Array<(ticket: TerminalTicket) => void> = [];
    const { session } = setup({
      createTicket: () => new Promise((resolve) => releases.push(resolve)),
    });
    session.open("/first");
    session.dispose();
    session.open("/second");
    // The first request answers last.
    releases[1]!(ticket);
    await flush();
    releases[0]!(ticket);
    await flush();

    expect(FakeWebSocket.instances).toHaveLength(1);
    FakeWebSocket.last.open();
    expect(FakeWebSocket.last.sent).toEqual(['{"type":"open","cwd":"/second","cols":80,"rows":24}']);
  });
});

describe("TerminalSession reconnect", () => {
  it("reattaches by terminal id with a fresh ticket after an unexpected drop", async () => {
    const { session, socket, createTicket, states, logger } = await readySession();

    socket.drop();
    expect(session.getState()).toMatchObject({
      status: "connecting",
      terminalId: "t-1",
      reconnectAttempt: 1,
      exit: null,
    });
    expect(logger.warn).toHaveBeenCalledWith("terminal: disconnected, reconnecting in 1000ms (attempt 1)");
    expect(states.map((state) => state.status)).toEqual(["connecting", "ready", "connecting"]);

    await vi.advanceTimersByTimeAsync(999);
    expect(FakeWebSocket.instances).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(createTicket).toHaveBeenCalledTimes(2);
    expect(FakeWebSocket.instances).toHaveLength(2);

    const reconnected = FakeWebSocket.last;
    expect(reconnected.url).toBe("wss://srv.test/terminal/ws?ticket=tk");
    reconnected.open();
    expect(reconnected.sent).toEqual(['{"type":"attach","terminalId":"t-1"}']);
    reconnected.receiveText(READY);
    expect(session.getState()).toMatchObject({ status: "ready", reconnectAttempt: 0 });
  });

  it("waits longer after each failure and resumes from the shortest delay once reattached", async () => {
    const { session, socket } = await readySession();
    for (const [attempt, delay] of [[1, 1_000], [2, 2_000], [3, 4_000]] as const) {
      socket.drop();
      expect(session.getState().reconnectAttempt).toBe(attempt);
      await vi.advanceTimersByTimeAsync(delay - 1);
      expect(FakeWebSocket.instances).toHaveLength(attempt);
      await vi.advanceTimersByTimeAsync(1);
      expect(FakeWebSocket.instances).toHaveLength(attempt + 1);
      FakeWebSocket.last.drop();
    }
    await vi.advanceTimersByTimeAsync(8_000);
    expect(FakeWebSocket.instances).toHaveLength(5);
    FakeWebSocket.last.open();
    FakeWebSocket.last.receiveText(READY);
    expect(session.getState().reconnectAttempt).toBe(0);
  });

  it("stops with the server's reason when the terminal is gone", async () => {
    const { session, socket } = await readySession();
    socket.drop();
    await vi.advanceTimersByTimeAsync(1_000);
    FakeWebSocket.last.open();
    FakeWebSocket.last.receiveText(
      JSON.stringify({ type: "error", code: "not_found", message: "terminal t-1 is gone" }),
    );

    expect(session.getState()).toMatchObject({
      status: "error",
      error: { code: "not_found", message: "terminal t-1 is gone" },
    });
    expect(FakeWebSocket.last.onclose).toBeNull();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("keeps trying for as long as a terminal it has reached is unreachable", async () => {
    const { session, socket } = await readySession();
    socket.drop();
    // A drop before `ready` is retried every time; none of them is the last.
    for (let i = 0; i < 40; i += 1) {
      await vi.advanceTimersByTimeAsync(31_000);
      FakeWebSocket.last.drop();
    }
    expect(session.getState().status).toBe("connecting");
    expect(FakeWebSocket.instances.length).toBeGreaterThan(10);
  });
});

describe("TerminalSession reaching the first terminal", () => {
  it("retries a shell that never answers with a new open each time, then gives up", async () => {
    const { session, createTicket, logger } = setup();
    session.open("/srv/app");
    await flush();
    for (let attempt = 1; attempt <= 7; attempt += 1) {
      FakeWebSocket.last.open();
      FakeWebSocket.last.drop();
      expect(session.getState().status).toBe("connecting");
      await vi.advanceTimersByTimeAsync(31_000);
    }
    FakeWebSocket.last.open();
    FakeWebSocket.last.drop();

    expect(session.getState()).toMatchObject({ status: "error", error: { code: "unavailable" } });
    expect(createTicket).toHaveBeenCalledTimes(8);
    expect(FakeWebSocket.instances).toHaveLength(8);
    expect(logger.warn).toHaveBeenCalledWith("terminal: unavailable", "no terminal after 8 attempts");
    await vi.advanceTimersByTimeAsync(120_000);
    expect(FakeWebSocket.instances).toHaveLength(8);
  });

  it("attaches again by the id it kept when a reattach keeps failing", async () => {
    const { session } = setup();
    session.attach("t-1");
    await flush();
    FakeWebSocket.last.open();
    FakeWebSocket.last.receiveText(READY);
    for (let i = 0; i < 9; i += 1) {
      FakeWebSocket.last.drop();
      await vi.advanceTimersByTimeAsync(31_000);
      FakeWebSocket.last.open();
    }
    expect(FakeWebSocket.last.sent).toEqual(['{"type":"attach","terminalId":"t-1"}']);
    expect(session.getState().status).toBe("connecting");
  });

  it("treats a refused ticket as final", async () => {
    const { session, createTicket } = setup({
      createTicket: async () => {
        throw new ApiError("API error: 404 Not Found", 404, "Not Found");
      },
    });
    session.open("/srv/app");
    await flush();

    expect(session.getState()).toMatchObject({
      status: "error",
      error: { code: "unavailable", message: "/api/terminal/ticket: 404 Not Found" },
    });
    expect(FakeWebSocket.instances).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(120_000);
    expect(createTicket).toHaveBeenCalledTimes(1);
  });

  it("treats a ticket response without a ticket as final", async () => {
    const { session } = setup({ createTicket: async () => null });
    session.open("/srv/app");
    await flush();
    expect(session.getState()).toMatchObject({
      status: "error",
      error: { code: "unavailable", message: "/api/terminal/ticket: no ticket in the response" },
    });
  });

  it("retries a ticket request that failed on the way", async () => {
    const createTicket = vi
      .fn<() => Promise<TerminalTicket | null>>()
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockRejectedValueOnce(new ApiError("API error: 503", 503, "Service Unavailable"))
      .mockResolvedValue(ticket);
    const { session } = setup({ createTicket });
    session.open("/srv/app");
    await flush();
    expect(session.getState().status).toBe("connecting");

    await vi.advanceTimersByTimeAsync(3_001);
    expect(createTicket).toHaveBeenCalledTimes(3);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("waits as long as a rate limit asks", async () => {
    const createTicket = vi
      .fn<() => Promise<TerminalTicket | null>>()
      .mockRejectedValueOnce(new ApiError("API error: 429", 429, "Too Many Requests", undefined, 5_000))
      .mockResolvedValue(ticket);
    const { session } = setup({ createTicket });
    session.open("/srv/app");
    await flush();

    await vi.advanceTimersByTimeAsync(4_999);
    expect(createTicket).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(createTicket).toHaveBeenCalledTimes(2);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("does not retry an address that cannot be parsed", async () => {
    vi.stubGlobal("WebSocket", StrictWebSocket as unknown as typeof WebSocket);
    const { createTicket } = setup();
    const broken = new TerminalSession({
      createTicket,
      socketUrl: () => "not a url",
    });
    broken.subscribe(() => undefined);
    broken.open("/srv/app");
    await flush();

    expect(broken.getState()).toMatchObject({
      status: "error",
      error: { code: "unavailable", message: "Invalid URL" },
    });
    expect(createTicket).toHaveBeenCalledTimes(1);
  });
});

describe("TerminalSession malformed frames", () => {
  it("logs an unparseable frame once per connection and keeps reading after it", async () => {
    const { session, socket, logger } = await readySession();
    socket.drop();
    await vi.advanceTimersByTimeAsync(1_000);
    const reconnected = FakeWebSocket.last;
    reconnected.open();
    const huge = "x".repeat(5000);

    reconnected.receiveText(huge);
    reconnected.receiveText('{"type":"ready');
    reconnected.receiveText(READY);

    expect(logger.warn).toHaveBeenCalledTimes(2);
    expect(logger.warn).toHaveBeenLastCalledWith(
      "terminal: received an unparseable control frame",
      `${"x".repeat(200)}... (truncated, 5000 chars total)`,
    );
    expect(session.getState().status).toBe("ready");
  });

  it("ignores a control message the contract does not define", async () => {
    const { session, socket, logger, states } = await readySession();
    const before = states.length;
    socket.receiveText(JSON.stringify({ type: "ping" }));
    socket.receiveText(JSON.stringify({ type: ["ready"] }));
    socket.receiveText(JSON.stringify(null));
    socket.receiveText(JSON.stringify("ready"));

    expect(logger.warn).toHaveBeenCalledTimes(1);
    expect(session.getState().status).toBe("ready");
    expect(states).toHaveLength(before);
  });

  it("drops a ready or exit that names no terminal instead of acting on it", async () => {
    const { session, socket } = await readySession();
    socket.receiveText(JSON.stringify({ type: "ready", pid: 1 }));
    socket.receiveText(JSON.stringify({ type: "exit", exitCode: 0 }));
    expect(session.getState()).toMatchObject({ status: "ready", terminalId: "t-1" });
  });

  it("takes an error it cannot read in full as an internal one", async () => {
    const { session, socket } = await readySession();
    socket.receiveText(JSON.stringify({ type: "error", code: 42, message: {} }));
    expect(session.getState()).toMatchObject({
      status: "error",
      error: { code: "internal", message: "" },
    });
  });

  it("reads what it can from a sloppy exit", async () => {
    const { session, socket } = await readySession();
    socket.receiveText(JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: "3", signal: 9 }));
    expect(session.getState()).toMatchObject({ status: "exited", exit: { exitCode: null, signal: null } });
  });
});

describe("TerminalSession default wiring", () => {
  it("takes the ticket and the socket address from the API client", async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json(ticket));
    vi.stubGlobal("fetch", fetchMock);
    setApiInstance(new ApiClient("https://api.example.test"));

    const session = new TerminalSession();
    session.subscribe(() => undefined);
    session.open("/srv/app");
    await flush();

    expect(fetchMock.mock.calls[0]![0]).toBe("https://api.example.test/api/terminal/ticket");
    expect(FakeWebSocket.last.url).toBe("wss://api.example.test/terminal/ws?ticket=tk");
  });
});
