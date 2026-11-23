// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { parseWithFallback, setSchemaLogger } from "../api/schema";
import { noopLogger } from "../logger";
import {
  TERMINAL_SOCKET_PATH,
  TerminalServerMessageSchema,
  TerminalTicketSchema,
  decodeOutputFrame,
  encodeClientMessage,
  isTerminalServerMessageType,
  parseServerMessage,
  terminalSocketUrl,
} from "./protocol";

afterEach(() => {
  setSchemaLogger(noopLogger);
});

describe("encodeClientMessage", () => {
  it("keeps the contract's field names and leaves an absent daemon out", () => {
    expect(encodeClientMessage({ type: "open", cwd: "/srv/app", cols: 120, rows: 40 })).toBe(
      '{"type":"open","cwd":"/srv/app","cols":120,"rows":40}',
    );
    expect(
      encodeClientMessage({ type: "open", cwd: "/srv/app", cols: 120, rows: 40, daemonId: "d-1" }),
    ).toBe('{"type":"open","cwd":"/srv/app","cols":120,"rows":40,"daemonId":"d-1"}');
  });

  it("opens an agent's terminal in a tree of a task, naming no directory", () => {
    expect(
      encodeClientMessage({ type: "open", agent: { issue_id: "issue-1", tree_id: "tree-1" }, cols: 80, rows: 24 }),
    ).toBe('{"type":"open","agent":{"issue_id":"issue-1","tree_id":"tree-1"},"cols":80,"rows":24}');
  });

  it("encodes attach, resize and close", () => {
    expect(encodeClientMessage({ type: "attach", terminalId: "t-1" })).toBe('{"type":"attach","terminalId":"t-1"}');
    expect(encodeClientMessage({ type: "resize", cols: 80, rows: 24 })).toBe('{"type":"resize","cols":80,"rows":24}');
    expect(encodeClientMessage({ type: "close", terminalId: "t-1" })).toBe('{"type":"close","terminalId":"t-1"}');
  });

  it("escapes instead of breaking the frame", () => {
    const frame = encodeClientMessage({ type: "open", cwd: '/a "quoted" \\ path', cols: 1, rows: 1 });
    expect(JSON.parse(frame)).toEqual({ type: "open", cwd: '/a "quoted" \\ path', cols: 1, rows: 1 });
  });
});

describe("parseServerMessage", () => {
  it("reads a ready message", () => {
    expect(
      parseServerMessage({ type: "ready", terminalId: "t-1", pid: 4242, cols: 120, rows: 40 }),
    ).toEqual({ type: "ready", terminalId: "t-1", pid: 4242, cols: 120, rows: 40 });
  });

  it("reads an exit by status and an exit by signal", () => {
    expect(parseServerMessage({ type: "exit", terminalId: "t-1", exitCode: 0 })).toEqual({
      type: "exit",
      terminalId: "t-1",
      exitCode: 0,
    });
    expect(parseServerMessage({ type: "exit", terminalId: "t-1", exitCode: null, signal: "SIGKILL" })).toEqual({
      type: "exit",
      terminalId: "t-1",
      exitCode: null,
      signal: "SIGKILL",
    });
  });

  it("reads an error with any of the contract's codes", () => {
    expect(parseServerMessage({ type: "error", code: "path_not_allowed", message: "outside the roots" })).toEqual({
      type: "error",
      code: "path_not_allowed",
      message: "outside the roots",
    });
    for (const code of [
      "flag_disabled",
      "bad_request",
      "terminal_root_unconfigured",
      "daemon_unavailable",
      "not_found",
      "tree_not_ready",
      "runtime_offline",
      "unsupported_platform",
      "internal",
    ]) {
      expect(parseServerMessage({ type: "error", code, message: "m" })).toMatchObject({ code });
    }
  });

  it("fills in what the server left out or garbled", () => {
    expect(parseServerMessage({ type: "ready", terminalId: "t-1" })).toMatchObject({
      pid: null,
      cols: null,
      rows: null,
    });
    expect(parseServerMessage({ type: "ready", terminalId: "t-1", pid: "4242", cols: -1, rows: 1.5 })).toMatchObject({
      pid: null,
      cols: null,
      rows: null,
    });
    const garbled = parseServerMessage({ type: "exit", terminalId: "t-1", exitCode: "1", signal: 9 });
    expect(garbled).toMatchObject({ exitCode: null });
    expect(garbled?.type === "exit" ? garbled.signal : "not an exit").toBeUndefined();
    const unsignaled = parseServerMessage({ type: "exit", terminalId: "t-1", exitCode: 3 });
    expect(unsignaled?.type === "exit" ? unsignaled.signal : "not an exit").toBeUndefined();
  });

  it("reads an unknown error code as internal, the code the server reports for codes it does not know", () => {
    expect(parseServerMessage({ type: "error", code: "teapot", message: "m" })).toEqual({
      type: "error",
      code: "internal",
      message: "m",
    });
    expect(parseServerMessage({ type: "error", code: "internal" })).toEqual({
      type: "error",
      code: "internal",
      message: "",
    });
  });

  it("keeps extra fields the message may carry", () => {
    expect(parseServerMessage({ type: "ready", terminalId: "t-1", pid: 1, cols: 2, rows: 3, extra: true })).toEqual({
      type: "ready",
      terminalId: "t-1",
      pid: 1,
      cols: 2,
      rows: 3,
    });
  });

  it("drops a message that names no terminal", () => {
    const logger = { ...noopLogger, warn: vi.fn() };
    setSchemaLogger(logger);
    for (const raw of [
      { type: "ready", pid: 1 },
      { type: "ready", terminalId: "" },
      { type: "exit", exitCode: 0 },
      { type: "exit", terminalId: 7, exitCode: 0 },
    ]) {
      expect(parseServerMessage(raw)).toBeNull();
    }
    expect(logger.warn).toHaveBeenCalledTimes(4);
    expect(logger.warn.mock.calls[0]![0]).toBe("API response failed schema validation: WS /terminal/ws control message");
  });

  it("drops anything that is not a control message instead of throwing", () => {
    for (const raw of [null, undefined, "ready", 42, [], {}, { type: 1 }, { type: "ping" }, { type: "ready;" }]) {
      expect(parseServerMessage(raw)).toBeNull();
    }
  });

  it("answers whether a type is one the contract defines", () => {
    expect(isTerminalServerMessageType("ready")).toBe(true);
    expect(isTerminalServerMessageType("exit")).toBe(true);
    expect(isTerminalServerMessageType("error")).toBe(true);
    expect(isTerminalServerMessageType("open")).toBe(false);
    expect(isTerminalServerMessageType("pong")).toBe(false);
    expect(isTerminalServerMessageType(undefined)).toBe(false);
    expect(isTerminalServerMessageType(["ready"])).toBe(false);
  });

  it("is safe to use as a type guard on untyped frames", () => {
    const frame: unknown = JSON.parse('{"type":"exit","terminalId":"t-1","exitCode":3}');
    const message = parseServerMessage(frame);
    expect(message?.type === "exit" ? message.exitCode : "not an exit").toBe(3);
  });
});

describe("TerminalServerMessageSchema", () => {
  it("treats each type as a separate message, so one shape cannot pose as another", () => {
    expect(TerminalServerMessageSchema.safeParse({ type: "error", code: "internal" }).success).toBe(true);
    expect(TerminalServerMessageSchema.safeParse({ type: "ready", code: "internal" }).success).toBe(false);
  });
});

describe("TerminalTicketSchema", () => {
  const parse = (raw: unknown) => parseWithFallback(raw, TerminalTicketSchema, null, { endpoint: "ticket" });

  it("reads the ticket response", () => {
    expect(parse({ ticket: "abc", expiresInSeconds: 120 })).toEqual({ ticket: "abc", expiresInSeconds: 120 });
  });

  it("falls back to the contract's lifetime when none is readable", () => {
    expect(parse({ ticket: "abc" })).toEqual({ ticket: "abc", expiresInSeconds: 120 });
    expect(parse({ ticket: "abc", expiresInSeconds: "soon" })).toEqual({ ticket: "abc", expiresInSeconds: 120 });
  });

  it("refuses a response without a usable ticket", () => {
    expect(parse({})).toBeNull();
    expect(parse({ ticket: "" })).toBeNull();
    expect(parse({ ticket: 42, expiresInSeconds: 120 })).toBeNull();
    expect(parse(null)).toBeNull();
  });
});

describe("decodeOutputFrame", () => {
  it("reads an ArrayBuffer", () => {
    const bytes = new Uint8Array([1, 2, 3]);
    expect(decodeOutputFrame(bytes.buffer)).toEqual(bytes);
  });

  it("reads only the view's range of its buffer", () => {
    const backed = new Uint8Array([9, 1, 2, 3, 9]);
    const view = backed.subarray(1, 4);
    expect(decodeOutputFrame(view)).toEqual(new Uint8Array([1, 2, 3]));
    expect(decodeOutputFrame(new DataView(backed.buffer, 1, 3))).toEqual(new Uint8Array([1, 2, 3]));
  });

  it("is null for a control frame or anything else", () => {
    expect(decodeOutputFrame('{"type":"ready"}')).toBeNull();
    expect(decodeOutputFrame(null)).toBeNull();
    expect(decodeOutputFrame(7)).toBeNull();
  });
});

describe("terminalSocketUrl", () => {
  it("is a sibling of the API on the same origin", () => {
    expect(terminalSocketUrl("https://api.example.test", "t 1")).toBe(
      "wss://api.example.test/terminal/ws?ticket=t+1",
    );
    expect(terminalSocketUrl("http://localhost:8080", "t1")).toBe("ws://localhost:8080/terminal/ws?ticket=t1");
  });

  it("follows a path prefix and ignores a trailing slash", () => {
    expect(terminalSocketUrl("https://example.test/multica/", "t1")).toBe(
      "wss://example.test/multica/terminal/ws?ticket=t1",
    );
    expect(terminalSocketUrl("https://example.test/multica", "t1")).toBe(
      "wss://example.test/multica/terminal/ws?ticket=t1",
    );
  });

  it("drops whatever else the base URL carried", () => {
    const url = new URL(terminalSocketUrl("https://example.test/api?x=1#frag", "a&b"));
    expect(`${url.origin}${url.pathname}`).toBe("wss://example.test/api/terminal/ws");
    expect([...url.searchParams.keys()]).toEqual(["ticket"]);
    expect(url.searchParams.get("ticket")).toBe("a&b");
    expect(url.hash).toBe("");
    expect(terminalSocketUrl("https://example.test", "a&b")).toBe("wss://example.test/terminal/ws?ticket=a%26b");
  });

  it("uses the page's origin for an empty base", () => {
    // No page in this environment, so the base is the loopback host.
    expect(terminalSocketUrl("", "t1")).toBe("ws://localhost/terminal/ws?ticket=t1");
    expect(terminalSocketUrl("/", "t1")).toBe("ws://localhost/terminal/ws?ticket=t1");
    expect(TERMINAL_SOCKET_PATH).toBe("/terminal/ws");
  });
});
