/**
 * Wire protocol of the terminal socket, `GET <api origin>/terminal/ws?ticket=…`.
 *
 * Text frames carry one JSON control message each; binary frames carry raw
 * terminal bytes, keyboard input toward the server and terminal output toward
 * the browser. This module is the only place that knows the field names, so
 * the client and the views speak in typed objects.
 *
 * Everything a server sends is read through `parseWithFallback`: a malformed
 * control message is dropped and logged, never thrown, so a newer or buggy
 * server degrades the session instead of the page.
 */
import { z } from "zod";
import { parseWithFallback } from "../api/schema";

/** `POST`, authenticated like every other `/api/**` call. */
export const TERMINAL_TICKET_ENDPOINT = "/api/terminal/ticket";

/**
 * Path of the terminal socket on the API origin, a sibling of the realtime
 * `/ws` and `/collab` sockets.
 */
export const TERMINAL_SOCKET_PATH = "/terminal/ws";

/** How long the server says a ticket is good for when it sends no readable value. */
export const TERMINAL_TICKET_DEFAULT_TTL_SECONDS = 120;

/**
 * Codes an `error` message can carry. The server reports any code it does not
 * know as `internal`, so this is the whole set a client has to understand.
 * `not_found` also answers an `open` that names a task or a tree the server
 * cannot find; `tree_not_ready` one whose tree is not built.
 */
export const TERMINAL_ERROR_CODES = [
  "flag_disabled",
  "bad_request",
  "terminal_root_unconfigured",
  "path_not_allowed",
  "daemon_unavailable",
  "not_found",
  "unsupported_platform",
  "tree_not_ready",
  "runtime_offline",
  "internal",
] as const;

export type TerminalErrorCode = (typeof TERMINAL_ERROR_CODES)[number];

const errorCodeSchema = z.enum(TERMINAL_ERROR_CODES).catch("internal");

/** A string when the server sent one; absent, null or anything else reads as absent. */
const optionalStringSchema = z.preprocess(
  (value) => (typeof value === "string" ? value : undefined),
  z.string().optional(),
);

// Server → client

/** Answers a successful `open` or `attach`. Output streams from here on. */
export const TerminalReadySchema = z.object({
  type: z.literal("ready"),
  terminalId: z.string().min(1),
  pid: z.number().int().nullable().catch(null),
  cols: z.number().int().positive().nullable().catch(null),
  rows: z.number().int().positive().nullable().catch(null),
});

/** The process ended. `exitCode` is null when a signal ended it, and `signal` then names it. */
export const TerminalExitSchema = z.object({
  type: z.literal("exit"),
  terminalId: z.string().min(1),
  exitCode: z.number().int().nullable().catch(null),
  signal: optionalStringSchema,
});

/** A failure. After one answering `open` or `attach` the server closes the socket. */
export const TerminalErrorSchema = z.object({
  type: z.literal("error"),
  code: errorCodeSchema,
  message: z.string().catch(""),
});

export interface TerminalReadyMessage {
  type: "ready";
  terminalId: string;
  pid: number | null;
  cols: number | null;
  rows: number | null;
}

export interface TerminalExitMessage {
  type: "exit";
  terminalId: string;
  exitCode: number | null;
  signal?: string;
}

export interface TerminalErrorMessage {
  type: "error";
  code: TerminalErrorCode;
  message: string;
}

export type TerminalServerMessage = TerminalReadyMessage | TerminalExitMessage | TerminalErrorMessage;

export const TerminalServerMessageSchema = z.discriminatedUnion("type", [
  TerminalReadySchema,
  TerminalExitSchema,
  TerminalErrorSchema,
]);

const SERVER_MESSAGE_TYPES = new Set<TerminalServerMessage["type"]>(["ready", "exit", "error"]);

/** Whether `type` names a control message the contract defines, so an unknown one can be ignored before parsing. */
export function isTerminalServerMessageType(type: unknown): type is TerminalServerMessage["type"] {
  return typeof type === "string" && SERVER_MESSAGE_TYPES.has(type as TerminalServerMessage["type"]);
}

const CONTROL_MESSAGE_ENDPOINT = "WS /terminal/ws control message";

/**
 * Read one control message. Null for anything that is not a well-formed
 * `ready`, `exit` or `error`; the failure is logged, not thrown.
 */
export function parseServerMessage(data: unknown): TerminalServerMessage | null {
  return parseWithFallback<TerminalServerMessage | null>(
    data,
    TerminalServerMessageSchema,
    null,
    { endpoint: CONTROL_MESSAGE_ENDPOINT },
  );
}

// Ticket

export const TerminalTicketSchema = z.object({
  ticket: z.string().min(1),
  expiresInSeconds: z.number().int().nonnegative().catch(TERMINAL_TICKET_DEFAULT_TTL_SECONDS),
});

/** Single-use and short-lived: it admits one socket handshake and is never reused. */
export interface TerminalTicket {
  ticket: string;
  expiresInSeconds: number;
}

// Client → server

export interface TerminalSize {
  cols: number;
  rows: number;
}

/** The tree of a task an agent terminal starts in, both by UUID: the task's
 *  and the tree's. The server runs it on the machine that built the tree. */
export interface TerminalAgentTarget {
  issue_id: string;
  tree_id: string;
}

export type TerminalClientMessage =
  /** Start a shell in `cwd`. `daemonId` picks the machine among several. */
  | ({ type: "open"; cwd: string; daemonId?: string } & TerminalSize)
  /** Run the agent's command line in a tree of a task. */
  | ({ type: "open"; agent: TerminalAgentTarget } & TerminalSize)
  /** Stream the output of a terminal that is already running; nothing is replayed. */
  | { type: "attach"; terminalId: string }
  | ({ type: "resize" } & TerminalSize)
  /** End the process. */
  | { type: "close"; terminalId: string };

/** Serialize a control message. An absent optional field is left out rather than sent as null. */
export function encodeClientMessage(message: TerminalClientMessage): string {
  return JSON.stringify(message);
}

// Frames

/**
 * The terminal output in a binary frame (an `ArrayBuffer` or a view over
 * one), or null for anything else.
 */
export function decodeOutputFrame(data: unknown): Uint8Array | null {
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  if (ArrayBuffer.isView(data)) {
    return new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
  }
  return null;
}

/**
 * Where the terminal socket lives for an API base URL. The API client's base
 * is the same origin and path prefix every `/api/**` call already uses, so the
 * socket follows it: `https://host/prefix` becomes
 * `wss://host/prefix/terminal/ws?ticket=…`. An empty base means the page's own
 * origin.
 */
export function terminalSocketUrl(apiBaseUrl: string, ticket: string): string {
  const fallback = typeof location !== "undefined" && location.href ? location.href : "http://localhost";
  const url = new URL(apiBaseUrl || "/", fallback);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  url.pathname = `${url.pathname.replace(/\/+$/, "")}${TERMINAL_SOCKET_PATH}`;
  url.search = "";
  url.hash = "";
  url.searchParams.set("ticket", ticket);
  return url.toString();
}
