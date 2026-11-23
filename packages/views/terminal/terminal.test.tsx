// @vitest-environment jsdom
import { createRef } from "react";
import { act, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@multica/core/api";
import { TerminalSession } from "@multica/core/terminal";
import { renderWithI18n } from "../test/i18n";
import { Terminal, type TerminalHandle } from "./terminal";

// jsdom lays nothing out and has no canvas, so xterm.js is replaced by a fake
// that records what the component does to it and lets a test speak as it.

const xterm = vi.hoisted(() => {
  type Listener<T> = (value: T) => void;

  /**
   * What an xterm.js event is to the component: called with a listener, which
   * gets a way to stop it. The fake also speaks through it.
   */
  function event<T>() {
    const listeners = new Set<Listener<T>>();
    const on = (listener: Listener<T>) => {
      listeners.add(listener);
      return { dispose: () => void listeners.delete(listener) };
    };
    return Object.assign(on, {
      emit: (value: T) => {
        for (const listener of listeners) listener(value);
      },
      size: () => listeners.size,
    });
  }

  class FakeTerminal {
    static instances: FakeTerminal[] = [];
    /** What the next fit sizes the terminal to. */
    static measured = { cols: 120, rows: 40 };
    element: HTMLElement | null = null;
    options: Record<string, unknown>;
    written: Array<string | Uint8Array> = [];
    focused = 0;
    disposed = false;
    cols = 80;
    rows = 24;
    readonly onData = event<string>();
    readonly onBinary = event<string>();
    readonly onResize = event<{ cols: number; rows: number }>();

    constructor(options: Record<string, unknown>) {
      this.options = options;
      FakeTerminal.instances.push(this);
    }
    loadAddon(addon: { activate(terminal: FakeTerminal): void }) {
      addon.activate(this);
    }
    open(element: HTMLElement) {
      this.element = element;
    }
    focus() {
      this.focused += 1;
    }
    write(data: string | Uint8Array) {
      this.written.push(data);
    }
    // Only a real change of size is reported, as by the real one.
    resize(cols: number, rows: number) {
      if (cols === this.cols && rows === this.rows) return;
      this.cols = cols;
      this.rows = rows;
      this.onResize.emit({ cols, rows });
    }
    dispose() {
      this.disposed = true;
    }
    listeners(): number {
      return this.onData.size() + this.onBinary.size() + this.onResize.size();
    }
    static get last(): FakeTerminal {
      return FakeTerminal.instances.at(-1)!;
    }
  }

  class FakeFitAddon {
    static instances: FakeFitAddon[] = [];
    fits = 0;
    private terminal: FakeTerminal | null = null;
    constructor() {
      FakeFitAddon.instances.push(this);
    }
    activate(terminal: FakeTerminal) {
      this.terminal = terminal;
    }
    fit() {
      this.fits += 1;
      this.terminal?.resize(FakeTerminal.measured.cols, FakeTerminal.measured.rows);
    }
    static get last(): FakeFitAddon {
      return FakeFitAddon.instances.at(-1)!;
    }
  }

  return { FakeTerminal, FakeFitAddon };
});

vi.mock("@xterm/xterm", () => ({ Terminal: xterm.FakeTerminal }));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: xterm.FakeFitAddon }));

const apiMocks = vi.hoisted(() => ({
  createTerminalTicket: vi.fn<() => Promise<{ ticket: string; expiresInSeconds: number } | null>>(),
}));

vi.mock("@multica/core/api", () => ({
  ApiError: class ApiError extends Error {
    status: number;
    constructor(message: string, status: number) {
      super(message);
      this.status = status;
    }
  },
  api: {
    createTerminalTicket: apiMocks.createTerminalTicket,
    getBaseUrl: () => "https://multica.test",
  },
}));

const READY = JSON.stringify({ type: "ready", terminalId: "t-1", pid: 4242, cols: 120, rows: 40 });
const encoder = new TextEncoder();

/** One socket per connection attempt, driven by hand: no network, no real handshake. */
class FakeSocket {
  static instances: FakeSocket[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  binaryType = "blob";
  sent: Array<string | Uint8Array> = [];
  constructor(readonly url: string) {
    FakeSocket.instances.push(this);
  }
  send(data: string | Uint8Array) {
    this.sent.push(data);
  }
  closeCalls = 0;
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
  /** The control messages sent so far; keyboard bytes are left out. */
  control(): Array<Record<string, unknown>> {
    return this.sent
      .filter((data): data is string => typeof data === "string")
      .map((data) => JSON.parse(data) as Record<string, unknown>);
  }
  static get last(): FakeSocket {
    return FakeSocket.instances.at(-1)!;
  }
}

const observers: FakeResizeObserver[] = [];

class FakeResizeObserver {
  disconnected = false;
  constructor(readonly callback: ResizeObserverCallback) {
    observers.push(this);
  }
  observe() {}
  disconnect() {
    this.disconnected = true;
  }
}

/** Let what was asked for settle, as the event loop would between renders. */
const settle = async () => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
};

/** The server's turn: what it sends or does to the socket, shown by React. */
const serve = (action: (socket: FakeSocket) => void) =>
  act(async () => {
    action(FakeSocket.last);
  });

const sessions: TerminalSession[] = [];

function setup() {
  const session = new TerminalSession();
  sessions.push(session);
  const onReady = vi.fn();
  const onRequestClose = vi.fn();
  const handle = createRef<TerminalHandle>();
  const view = renderWithI18n(
    <Terminal ref={handle} session={session} onReady={onReady} onRequestClose={onRequestClose} />,
  );
  return { ...view, session, onReady, onRequestClose, handle, host: view.container.firstElementChild as HTMLElement };
}

/** A terminal that answered: open a shell, let the server say it is ready. */
async function readyTerminal() {
  const view = setup();
  act(() => view.session.open("/srv/app"));
  await settle();
  await serve((socket) => {
    socket.open();
    socket.receiveText(READY);
  });
  return { ...view, socket: FakeSocket.last };
}

beforeEach(() => {
  FakeSocket.instances = [];
  xterm.FakeTerminal.instances = [];
  xterm.FakeFitAddon.instances = [];
  xterm.FakeTerminal.measured = { cols: 120, rows: 40 };
  observers.length = 0;
  apiMocks.createTerminalTicket.mockResolvedValue({ ticket: "tk", expiresInSeconds: 120 });
  vi.stubGlobal("WebSocket", FakeSocket as unknown as typeof WebSocket);
  vi.stubGlobal("ResizeObserver", FakeResizeObserver as unknown as typeof ResizeObserver);
  // No canvas to resolve the theme's tokens through: the plain values stand in.
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
});

afterEach(() => {
  for (const session of sessions) session.dispose();
  sessions.length = 0;
  document.documentElement.classList.remove("dark");
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Terminal", () => {
  it("draws the terminal in its host, focused and fitted to it", () => {
    const { host } = setup();
    const term = xterm.FakeTerminal.last;

    expect(term.element).toBe(host);
    expect(host).toHaveAttribute("data-raw-keyboard");
    expect(term.focused).toBe(1);
    expect(xterm.FakeFitAddon.last.fits).toBe(1);
    expect([term.cols, term.rows]).toEqual([120, 40]);
    expect(term.options.scrollback).toBe(10_000);
  });

  it("keeps the app's own shortcuts out of it", () => {
    // The attribute is what the global shortcuts look for; the rule itself is
    // tested beside it. Here only that the terminal carries the mark.
    const { host } = setup();
    expect(host.closest("[data-raw-keyboard]")).toBe(host);
  });

  it("says it is connecting until the server answers", async () => {
    const { session, onReady } = setup();
    act(() => session.open("/srv/app"));
    expect(screen.getByRole("status")).toHaveTextContent("Connecting...");

    await settle();
    await serve((socket) => {
      socket.open();
      socket.receiveText(READY);
    });

    expect(screen.queryByRole("status")).toBeNull();
    expect(onReady).toHaveBeenCalledWith("t-1");
  });

  it("opens the shell at the size the terminal was fitted to", async () => {
    const { session } = setup();
    act(() => session.open("/srv/app"));
    await settle();
    await serve((socket) => socket.open());

    expect(FakeSocket.last.url).toBe("wss://multica.test/terminal/ws?ticket=tk");
    expect(FakeSocket.last.control()).toEqual([{ type: "open", cwd: "/srv/app", cols: 120, rows: 40 }]);
  });

  it("sends what is typed to the socket, once the terminal is ready", async () => {
    const { socket } = await readyTerminal();

    act(() => xterm.FakeTerminal.last.onData.emit("ls\r\n"));

    expect(socket.sent.at(-1)).toEqual(encoder.encode("ls\r\n"));
  });

  it("drops what is typed before the terminal is ready", async () => {
    const { session } = setup();
    act(() => session.open("/srv/app"));
    act(() => xterm.FakeTerminal.last.onData.emit("too soon\r\n"));
    await settle();
    await serve((socket) => socket.open());

    expect(FakeSocket.last.sent).toHaveLength(1);
  });

  it("sends bytes that are not text as they are", async () => {
    const { socket } = await readyTerminal();

    act(() => xterm.FakeTerminal.last.onBinary.emit("\x1b[M !!"));

    expect(socket.sent.at(-1)).toEqual(Uint8Array.from([0x1b, 0x5b, 0x4d, 0x20, 0x21, 0x21]));
  });

  it("puts the terminal's output on screen", async () => {
    await readyTerminal();

    await serve((it) => it.receiveBytes(0x1b, 0x5b, 0x33, 0x32, 0x6d, 0x24, 0x20));

    expect(xterm.FakeTerminal.last.written).toEqual([Uint8Array.from([0x1b, 0x5b, 0x33, 0x32, 0x6d, 0x24, 0x20])]);
  });

  it("tells the server the new size once the container has stopped moving", async () => {
    const { socket } = await readyTerminal();
    const observer = observers.at(-1)!;
    xterm.FakeTerminal.measured = { cols: 100, rows: 30 };

    // A window drag: many size changes in a row.
    const resizes = () => socket.control().filter((message) => message.type === "resize");
    await act(async () => {
      for (let i = 0; i < 5; i += 1) observer.callback([], observer as unknown as ResizeObserver);
    });
    expect(resizes()).toEqual([]);

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 400));
    });
    expect(resizes()).toEqual([{ type: "resize", cols: 100, rows: 30 }]);
    expect(xterm.FakeFitAddon.last.fits).toBe(2);
  });

  it("shows how the process ended", async () => {
    await readyTerminal();
    await serve((it) => it.receiveText(JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: 0 })));
    expect(screen.getByRole("status")).toHaveTextContent("Session ended with exit code 0");
  });

  it("names the signal when one ended the process", async () => {
    await readyTerminal();
    await serve((it) =>
      it.receiveText(JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: null, signal: "KILL" })),
    );
    expect(screen.getByRole("status")).toHaveTextContent("Session ended by signal KILL");
  });

  it("says only that the session ended when the server says no more", async () => {
    await readyTerminal();
    await serve((it) => it.receiveText(JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: null })));
    expect(screen.getByRole("status")).toHaveTextContent("Session ended");
  });

  it("says the server refused, in its own words too", async () => {
    const { session, onReady } = setup();
    act(() => session.open("/etc"));
    await settle();
    await serve((socket) => {
      socket.open();
      socket.receiveText(
        JSON.stringify({ type: "error", code: "path_not_allowed", message: "/etc is outside /home/me" }),
      );
    });

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("A terminal cannot be opened in this directory.");
    expect(alert).toHaveTextContent("/etc is outside /home/me");
    expect(onReady).not.toHaveBeenCalled();
  });

  it("says so when this server has no terminal to offer", async () => {
    apiMocks.createTerminalTicket.mockRejectedValue(new ApiError("API error: 404", 404, "Not Found"));
    const { session } = setup();
    act(() => session.open("/srv/app"));
    await settle();

    expect(screen.getByRole("alert")).toHaveTextContent("The terminal is not available on this server.");
    expect(FakeSocket.instances).toHaveLength(0);
  });

  it("offers a way out while it is reconnecting", async () => {
    const { onRequestClose } = await readyTerminal();
    await serve((it) => it.drop());

    expect(screen.getByRole("status")).toHaveTextContent("Reconnecting...");
    fireEvent.click(screen.getByRole("button", { name: "End session" }));
    expect(onRequestClose).toHaveBeenCalledTimes(1);
  });

  it("dresses the same terminal again when the app theme changes", async () => {
    const { host } = setup();
    const term = xterm.FakeTerminal.last;
    const theme = () => term.options.theme as { background: string };
    expect(theme().background).toBe("rgb(255, 255, 255)");

    await act(async () => {
      document.documentElement.classList.add("dark");
    });
    await settle();

    expect(xterm.FakeTerminal.instances).toHaveLength(1);
    expect(theme().background).toBe("rgb(30, 30, 33)");
    expect(host.style.backgroundColor).toBe("rgb(30, 30, 33)");
  });

  it("lets the page write to the screen and focus the terminal", async () => {
    const { handle } = await readyTerminal();

    act(() => handle.current?.write("hello"));
    act(() => handle.current?.focus());

    expect(xterm.FakeTerminal.last.written).toEqual(["hello"]);
    expect(xterm.FakeTerminal.last.focused).toBe(2);
  });

  it("focuses the terminal when the pointer lands beside the text", () => {
    const { host } = setup();
    fireEvent.pointerDown(host);
    expect(xterm.FakeTerminal.last.focused).toBe(2);
  });

  it("lets go of the terminal when it leaves the screen, without ending it", async () => {
    const { unmount, socket } = await readyTerminal();
    const term = xterm.FakeTerminal.last;
    expect(term.listeners()).toBeGreaterThan(0);

    unmount();

    expect(term.disposed).toBe(true);
    expect(term.listeners()).toBe(0);
    expect(observers.at(-1)!.disconnected).toBe(true);
    expect(socket.control().some((message) => message.type === "close")).toBe(false);
  });
});
