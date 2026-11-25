// @vitest-environment jsdom
import { createRef } from "react";
import { act, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
  TerminalHostBridge,
  TerminalHostEvent,
  TerminalHostAttachResult,
  TerminalHostOpenResult,
  TerminalSize,
} from "@multica/core/terminal";
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
    resets = 0;
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
    reset() {
      this.resets += 1;
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

vi.mock("@multica/core/api", () => ({
  api: { reportIssueAgentReply: vi.fn(() => Promise.resolve(undefined)) },
}));

/**
 * The desktop app's main process, driven by hand. One bridge per test: a
 * session follows it through `window.desktopAPI.terminal`, and the test speaks
 * for the app by resolving `open` and emitting events.
 */
class FakeBridge implements TerminalHostBridge {
  static instances: FakeBridge[] = [];
  openCalls: { target: unknown; size: TerminalSize }[] = [];
  attachCalls: { terminalId: string; size: TerminalSize }[] = [];
  writeCalls: { terminalId: string; data: string }[] = [];
  resizeCalls: { terminalId: string; size: TerminalSize }[] = [];
  endCalls: string[] = [];
  detachCalls: string[] = [];
  /** The answers to `open`, in order; the default names the terminal t-1. */
  openResults: TerminalHostOpenResult[] = [];
  attachResults: Array<Extract<TerminalHostAttachResult, { ok: true }> | Extract<TerminalHostAttachResult, { ok: false }>> = [];
  private listeners = new Set<(event: TerminalHostEvent) => void>();

  constructor() {
    FakeBridge.instances.push(this);
  }
  open(target: unknown, size: TerminalSize): Promise<TerminalHostOpenResult> {
    this.openCalls.push({ target, size });
    const result = this.openResults.shift() ?? { ok: true, terminalId: "t-1", pid: 4242, cwd: "/srv/app" };
    return Promise.resolve(result);
  }
  attach(terminalId: string, size: TerminalSize): Promise<TerminalHostAttachResult> {
    this.attachCalls.push({ terminalId, size });
    const result = this.attachResults.shift() ?? { ok: true, running: true, exit: null, screen: "" };
    return Promise.resolve(result);
  }
  detach(terminalId: string) {
    this.detachCalls.push(terminalId);
  }
  write(terminalId: string, data: string) {
    this.writeCalls.push({ terminalId, data });
  }
  resize(terminalId: string, size: TerminalSize) {
    this.resizeCalls.push({ terminalId, size });
  }
  end(terminalId: string) {
    this.endCalls.push(terminalId);
  }
  onEvent(listener: (event: TerminalHostEvent) => void) {
    this.listeners.add(listener);
    return () => void this.listeners.delete(listener);
  }
  /** The app's turn: an event about one of its terminals, shown by React. */
  emit(event: TerminalHostEvent) {
    for (const listener of [...this.listeners]) listener(event);
  }
  static get last(): FakeBridge {
    return FakeBridge.instances.at(-1)!;
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

const shellIn = (cwd: string) => ({ kind: "shell", place: { kind: "local" }, cwd }) as const;

const sessions: TerminalSession[] = [];

function setup(target: ReturnType<typeof shellIn> = shellIn("/srv/app"), terminalId?: string) {
  const bridge = new FakeBridge();
  (window as unknown as { desktopAPI: unknown }).desktopAPI = { terminal: bridge };
  const session = new TerminalSession({ target, terminalId });
  sessions.push(session);
  const onReady = vi.fn();
  const handle = createRef<TerminalHandle>();
  const view = renderWithI18n(<Terminal ref={handle} session={session} onReady={onReady} />);
  return { ...view, session, onReady, handle, bridge, host: view.container.firstElementChild as HTMLElement };
}

/** A terminal that answered: open it, let the owner name and run it. */
async function readyTerminal() {
  const view = setup();
  act(() => view.session.start());
  await settle();
  return { ...view, bridge: FakeBridge.last };
}

beforeEach(() => {
  FakeBridge.instances = [];
  xterm.FakeTerminal.instances = [];
  xterm.FakeFitAddon.instances = [];
  xterm.FakeTerminal.measured = { cols: 120, rows: 40 };
  observers.length = 0;
  vi.stubGlobal("ResizeObserver", FakeResizeObserver as unknown as typeof ResizeObserver);
  // No canvas to resolve the theme's tokens through: the plain values stand in.
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
});

afterEach(() => {
  for (const session of sessions) session.dispose();
  sessions.length = 0;
  delete (window as unknown as { desktopAPI?: unknown }).desktopAPI;
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

  it("says it is opening until the owner answers", async () => {
    const { session, onReady } = setup();
    act(() => session.start());
    expect(screen.getByRole("status")).toHaveTextContent("Opening...");

    await settle();

    expect(screen.queryByRole("status")).toBeNull();
    expect(onReady).toHaveBeenCalledWith("t-1");
  });

  it("opens the shell at the size the terminal was fitted to", async () => {
    const { session } = setup();
    act(() => session.start());
    await settle();

    expect(FakeBridge.last.openCalls).toEqual([
      { target: shellIn("/srv/app"), size: { cols: 120, rows: 40 } },
    ]);
  });

  it("sends what is typed to the owner, once the terminal is ready", async () => {
    const { bridge } = await readyTerminal();

    act(() => xterm.FakeTerminal.last.onData.emit("ls\r\n"));

    expect(bridge.writeCalls.at(-1)).toEqual({ terminalId: "t-1", data: "ls\r\n" });
  });

  it("drops what is typed before the terminal is ready", async () => {
    const { session, bridge } = setup();
    act(() => xterm.FakeTerminal.last.onData.emit("too soon\r\n"));
    act(() => session.start());
    await settle();

    expect(bridge.writeCalls).toHaveLength(0);
  });

  it("sends bytes that are not text as they are", async () => {
    const { bridge } = await readyTerminal();

    act(() => xterm.FakeTerminal.last.onBinary.emit("\x1b[M !!"));

    // The keyboard arrives as text; the bytes it carries are kept whole.
    expect(bridge.writeCalls.at(-1)).toEqual({ terminalId: "t-1", data: "\x1b[M !!" });
  });

  it("puts the terminal's output on screen", async () => {
    await readyTerminal();

    await act(async () =>
      FakeBridge.last.emit({ terminalId: "t-1", type: "output", data: "\x1b[32m$ " }),
    );

    expect(xterm.FakeTerminal.last.written).toEqual(["\x1b[32m$ "]);
  });

  it("tells the owner the new size once the container has stopped moving", async () => {
    const { bridge } = await readyTerminal();
    const observer = observers.at(-1)!;
    xterm.FakeTerminal.measured = { cols: 100, rows: 30 };

    // A window drag: many size changes in a row.
    await act(async () => {
      for (let i = 0; i < 5; i += 1) observer.callback([], observer as unknown as ResizeObserver);
    });
    expect(bridge.resizeCalls).toEqual([]);

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 400));
    });
    expect(bridge.resizeCalls).toEqual([{ terminalId: "t-1", size: { cols: 100, rows: 30 } }]);
    expect(xterm.FakeFitAddon.last.fits).toBe(2);
  });

  it("shows how the process ended", async () => {
    await readyTerminal();
    await act(async () =>
      FakeBridge.last.emit({ terminalId: "t-1", type: "exit", exit: { exitCode: 0 } }),
    );
    expect(screen.getByRole("status")).toHaveTextContent("Session ended with exit code 0");
  });

  it("names the signal when one ended the process", async () => {
    await readyTerminal();
    await act(async () =>
      FakeBridge.last.emit({ terminalId: "t-1", type: "exit", exit: { exitCode: 0, signal: 9 } }),
    );
    expect(screen.getByRole("status")).toHaveTextContent("Session ended by signal 9");
  });

  it("says only that the session ended when the owner says no more", async () => {
    await readyTerminal();
    // An exit described without a code.
    const ended = { terminalId: "t-1", type: "exit", exit: { exitCode: null } } as unknown as TerminalHostEvent;
    await act(async () => FakeBridge.last.emit(ended));
    expect(screen.getByRole("status")).toHaveTextContent("Session ended");
  });

  it("says why the owner refused, in its own words too", async () => {
    const { session, onReady, bridge } = setup(shellIn("/etc"));
    bridge.openResults = [{ ok: false, code: "not_found", message: "/etc is not there any more." }];
    act(() => session.start());
    await settle();

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("The folder is not there any more.");
    expect(alert).toHaveTextContent("/etc is not there any more.");
    expect(onReady).not.toHaveBeenCalled();
  });

  it("says so when this client has no owner for terminals", async () => {
    // No desktopAPI: jsdom's window carries none, and no bridge is built.
    const session = new TerminalSession({ target: shellIn("/srv/app") });
    sessions.push(session);
    renderWithI18n(<Terminal session={session} />);
    act(() => session.start());
    await settle();

    expect(screen.getByRole("alert")).toHaveTextContent("Terminals open in the desktop app only.");
    expect(FakeBridge.instances).toHaveLength(0);
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
    const { unmount, bridge, session } = await readyTerminal();
    const term = xterm.FakeTerminal.last;
    expect(term.listeners()).toBeGreaterThan(0);

    unmount();

    expect(term.disposed).toBe(true);
    expect(term.listeners()).toBe(0);
    expect(observers.at(-1)!.disconnected).toBe(true);
    // The session outlives the view: the terminal runs on, still followed.
    expect(bridge.endCalls).toHaveLength(0);
    expect(bridge.detachCalls).toHaveLength(0);
    session.dispose();
    await settle();
  });

  describe("a terminal the address names", () => {
    it("rejoins it with its screen instead of opening another", async () => {
      const { session, bridge, onReady } = setup(shellIn("/srv/app"), "t-9");
      bridge.attachResults = [{ ok: true, running: true, exit: null, screen: "old screen" }];
      act(() => session.start());
      await settle();

      expect(bridge.attachCalls).toEqual([{ terminalId: "t-9", size: { cols: 120, rows: 40 } }]);
      expect(bridge.openCalls).toHaveLength(0);
      expect(onReady).toHaveBeenCalledWith("t-9");
      // The screen it had reached is drawn whole, before what follows it.
      expect(xterm.FakeTerminal.last.resets).toBe(1);
      expect(xterm.FakeTerminal.last.written).toEqual(["old screen"]);

      await act(async () => FakeBridge.last.emit({ terminalId: "t-9", type: "output", data: "more" }));
      expect(xterm.FakeTerminal.last.written).toEqual(["old screen", "more"]);
    });

    it("opens the target again when no terminal has that id any more", async () => {
      const { session, bridge } = setup(shellIn("/srv/app"), "t-9");
      bridge.attachResults = [{ ok: false, code: "not_found", message: "" }];
      act(() => session.start());
      await settle();

      expect(bridge.attachCalls).toHaveLength(1);
      expect(bridge.openCalls).toHaveLength(1);
      expect(screen.queryByRole("alert")).toBeNull();
    });
  });

  it("stops following a terminal it is disposed of, without ending it", async () => {
    const { session, bridge } = await readyTerminal();

    act(() => session.dispose());
    await settle();

    expect(bridge.detachCalls).toEqual(["t-1"]);
    expect(bridge.endCalls).toHaveLength(0);
  });
});
