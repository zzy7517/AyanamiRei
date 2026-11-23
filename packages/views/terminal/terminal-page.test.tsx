// @vitest-environment jsdom
import { useMemo, useState, type ReactNode } from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import { getTestResources } from "../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../navigation";
import { TerminalPage } from "./terminal-page";

// The terminal itself has its own tests; here only what the page wires to it.

const fakeTerminal = vi.hoisted(() => ({ writes: [] as string[] }));

// The page counts the agent's replies as seen while its terminal is shown.
// Both hooks have their own tests; here only what the page hands them.
const replyHooks = vi.hoisted(() => ({
  useIssueShown: vi.fn(),
  useMarkAgentReplySeenWhileViewing: vi.fn(),
}));
vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/issues/agent-reply", () => ({ useIssueShown: replyHooks.useIssueShown }));
vi.mock("../issues/hooks/use-mark-agent-reply-seen-while-viewing", () => ({
  useMarkAgentReplySeenWhileViewing: replyHooks.useMarkAgentReplySeenWhileViewing,
}));

// Stands in for the real terminal, keeping its contract with the page: it
// reports the terminal's id once the session is ready and takes text to show.
vi.mock("./terminal", async () => {
  const { forwardRef, useEffect, useImperativeHandle } = await import("react");
  const { useTerminalSessionState } = await import("@multica/core/terminal");
  return {
    Terminal: forwardRef<{ write(data: string): void; focus(): void }, object>(
      function FakeTerminal(props, ref) {
        const { session, onReady } = props as {
          session: import("@multica/core/terminal").TerminalSession;
          onReady?: (terminalId: string) => void;
        };
        const state = useTerminalSessionState(session);
        useEffect(() => {
          if (state.status === "ready" && state.terminalId) onReady?.(state.terminalId);
        }, [state.status, state.terminalId, onReady]);
        useImperativeHandle(
          ref,
          () => ({ write: (data: string) => void fakeTerminal.writes.push(data), focus: () => {} }),
          [],
        );
        return null;
      },
    ),
  };
});

const apiMocks = vi.hoisted(() => ({
  createTerminalTicket: vi.fn<() => Promise<{ ticket: string; expiresInSeconds: number } | null>>(),
}));

vi.mock("@multica/core/api", () => ({
  ApiError: class ApiError extends Error {
    constructor(message: string, readonly status: number) {
      super(message);
    }
  },
  api: {
    createTerminalTicket: apiMocks.createTerminalTicket,
    getBaseUrl: () => "https://multica.test",
  },
}));

const READY = JSON.stringify({ type: "ready", terminalId: "t-1", pid: 4242, cols: 120, rows: 40 });
const PAGE = "https://app.test/acme/terminal";

/** One socket per connection attempt, driven by hand: no network, no real handshake. */
class FakeSocket {
  static instances: FakeSocket[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  sent: string[] = [];
  closeCalls = 0;
  constructor(readonly url: string) {
    FakeSocket.instances.push(this);
  }
  send(data: string | Uint8Array) {
    if (typeof data === "string") this.sent.push(data);
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
  /** The control messages sent so far. */
  control(): Array<Record<string, unknown>> {
    return this.sent.map((data) => JSON.parse(data) as Record<string, unknown>);
  }
  static get last(): FakeSocket {
    return FakeSocket.instances.at(-1)!;
  }
}

const settle = async () => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
};

/** The server's turn: what it sends, shown by React. */
const serve = (action: (socket: FakeSocket) => void) =>
  act(async () => {
    action(FakeSocket.last);
  });

function renderPage(search: string) {
  // A router in miniature: the address is state in the shell, and the adapter
  // is a new object whenever it changes, as it is where the page really runs.
  let show: (url: URL) => void = () => {};
  // Only ever called from inside the page's own events and effects, which a
  // test has already put in an act() scope.
  const replace = vi.fn((path: string) => {
    show(new URL(path, "https://app.test"));
  });
  function Shell({ children }: { children: ReactNode }) {
    const [url, setUrl] = useState(() => new URL(`${PAGE}${search}`));
    const resources = useMemo(() => getTestResources("en"), []);
    const navigation = useMemo<NavigationAdapter>(
      () => ({
        push: vi.fn(),
        replace,
        back: vi.fn(),
        pathname: url.pathname,
        searchParams: url.searchParams,
        hash: "",
        getShareableUrl: (path) => path,
      }),
      [url],
    );
    show = setUrl;
    return (
      <I18nProvider locale="en" resources={resources}>
        <NavigationProvider value={navigation}>
          <WorkspaceSlugProvider slug="acme">{children}</WorkspaceSlugProvider>
        </NavigationProvider>
      </I18nProvider>
    );
  }
  const view = render(<TerminalPage />, { wrapper: Shell });
  /** The router showing another address than the one `replace` asked for. */
  const commit = (next: string) =>
    act(async () => {
      show(new URL(next, "https://app.test"));
    });
  /** The query of the address `replace` last asked for. */
  const asked = () => Object.fromEntries(new URL(replace.mock.lastCall![0], "https://app.test").searchParams);
  return { ...view, replace, commit, asked };
}

const directory = () => screen.getByLabelText("Directory") as HTMLInputElement;
const type = (value: string) => fireEvent.change(directory(), { target: { value } });

async function opened(search = "?cwd=%2Fsrv%2Fapp") {
  const view = renderPage(search);
  await settle();
  await serve((socket) => socket.open());
  return { ...view, socket: FakeSocket.last };
}

beforeEach(() => {
  FakeSocket.instances = [];
  fakeTerminal.writes = [];
  apiMocks.createTerminalTicket.mockResolvedValue({ ticket: "tk", expiresInSeconds: 120 });
  vi.stubGlobal("WebSocket", FakeSocket as unknown as typeof WebSocket);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("TerminalPage", () => {
  it("asks for a directory while the address names none", async () => {
    renderPage("");
    expect(directory().value).toBe("");
    expect(screen.queryByRole("button", { name: "End session" })).toBeNull();
    expect(FakeSocket.instances).toHaveLength(0);
    // Nothing is shown that a reply could land on.
    expect(replyHooks.useIssueShown).toHaveBeenLastCalledWith(null);
    expect(replyHooks.useMarkAgentReplySeenWhileViewing).toHaveBeenLastCalledWith("ws-1", "");
  });

  it("opens a shell in the directory the address names", async () => {
    await opened();
    expect(FakeSocket.last.control()).toMatchObject([{ type: "open", cwd: "/srv/app" }]);
    expect(directory().value).toBe("/srv/app");
    expect(directory()).toBeDisabled();
  });

  it("writes the terminal's id into the address once the server names it", async () => {
    const { replace, asked } = await opened();
    await serve((socket) => socket.receiveText(READY));

    expect(replace).toHaveBeenCalledTimes(1);
    expect(replace.mock.lastCall![0]).toMatch(/^\/acme\/terminal\?/);
    expect(asked()).toEqual({ cwd: "/srv/app", t: "t-1" });
  });

  it("keeps the terminal running when that id comes back in the address", async () => {
    const { replace, socket } = await opened();
    await serve((it) => it.receiveText(READY));
    // The router shows the address that was asked for, id and all.
    expect(replace).toHaveBeenCalledTimes(1);

    expect(FakeSocket.instances).toHaveLength(1);
    expect(socket.closeCalls).toBe(0);
    expect(socket.control().some((message) => message.type === "close")).toBe(false);
    expect(replace).toHaveBeenCalledTimes(1);
  });

  it("rejoins the terminal the address names instead of opening another", async () => {
    const { replace, socket } = await opened("?cwd=%2Fsrv%2Fapp&t=t-9");
    expect(socket.control()).toEqual([{ type: "attach", terminalId: "t-9" }]);

    await serve((it) =>
      it.receiveText(JSON.stringify({ type: "ready", terminalId: "t-9", pid: 7, cols: 80, rows: 24 })),
    );
    expect(replace).not.toHaveBeenCalled();
  });

  it("says on the reattached screen that nothing is replayed", async () => {
    await opened("?cwd=%2Fsrv%2Fapp&t=t-9");
    expect(fakeTerminal.writes).toEqual([]);
    await serve((it) => it.receiveText(READY));
    expect(fakeTerminal.writes).toHaveLength(1);
    expect(fakeTerminal.writes[0]).toContain("Reattached. The screen stays blank until the program writes again.");
  });

  it("opens the directory typed in, in place of the address", async () => {
    const { asked } = renderPage("");
    type("/srv/other");
    fireEvent.click(screen.getByRole("button", { name: "Open" }));

    expect(asked()).toEqual({ cwd: "/srv/other" });
    await settle();
    expect(FakeSocket.instances).toHaveLength(1);
    await serve((socket) => socket.open());
    expect(FakeSocket.last.control()).toMatchObject([{ type: "open", cwd: "/srv/other" }]);
  });

  it("refuses what cannot be a directory on the machine", async () => {
    const { replace } = renderPage("");
    type("srv/app");
    fireEvent.click(screen.getByRole("button", { name: "Open" }));

    expect(screen.getByText("Enter an absolute path.")).toBeInTheDocument();
    expect(directory()).toHaveAttribute("aria-invalid", "true");
    expect(replace).not.toHaveBeenCalled();
    expect(FakeSocket.instances).toHaveLength(0);
  });

  it("ends the session when asked", async () => {
    const { socket } = await opened();
    await serve((it) => it.receiveText(READY));

    fireEvent.click(screen.getByRole("button", { name: "End session" }));

    expect(socket.control().at(-1)).toEqual({ type: "close", terminalId: "t-1" });
  });

  it("starts another shell in the same directory once the first has ended", async () => {
    const { replace, socket } = await opened();
    await serve((it) => it.receiveText(READY));
    await serve((it) => it.receiveText(JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: 0 })));
    expect(directory()).toBeEnabled();
    expect(screen.queryByRole("button", { name: "End session" })).toBeNull();
    // The address keeps the id, so returning to the tab rejoins nothing silently.
    expect(replace).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("button", { name: "Open" }));

    expect(replace).toHaveBeenLastCalledWith("/acme/terminal?cwd=%2Fsrv%2Fapp");
    await settle();
    expect(FakeSocket.instances).toHaveLength(2);
    await serve((socket) => socket.open());
    expect(socket.control()).toMatchObject([{ type: "open", cwd: "/srv/app" }]);
  });

  it("follows the address to another directory, leaving the first shell running", async () => {
    const { commit, socket } = await opened();
    await serve((it) => it.receiveText(READY));

    await commit(`${PAGE}?cwd=%2Fsrv%2Fother`);
    await settle();

    expect(FakeSocket.instances).toHaveLength(2);
    expect(socket.closeCalls).toBe(1);
    expect(socket.control().some((message) => message.type === "close")).toBe(false);
    await serve((it) => it.open());
    expect(FakeSocket.last.control()).toMatchObject([{ type: "open", cwd: "/srv/other" }]);
  });

  it("closes the socket without ending the terminal when the page goes away", async () => {
    const { unmount, socket } = await opened();
    await serve((it) => it.receiveText(READY));
    unmount();

    expect(socket.closeCalls).toBe(1);
    expect(socket.control().some((message) => message.type === "close")).toBe(false);
  });

  describe("an agent's terminal", () => {
    const AGENT = "?issue=issue-1&tree=tree-1";
    const open = { type: "open", agent: { issue_id: "issue-1", tree_id: "tree-1" } };
    const exit = JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: 0 });

    it("counts the agent's replies as seen while this terminal is shown", async () => {
      await opened(AGENT);

      expect(replyHooks.useIssueShown).toHaveBeenLastCalledWith("issue-1");
      expect(replyHooks.useMarkAgentReplySeenWhileViewing).toHaveBeenLastCalledWith("ws-1", "issue-1");
    });

    it("starts the agent in the tree the address names, and asks for no directory", async () => {
      const { replace, asked } = await opened(AGENT);

      expect(FakeSocket.last.control()).toMatchObject([open]);
      expect(screen.queryByLabelText("Directory")).toBeNull();
      expect(screen.getByText("Agent terminal")).toBeInTheDocument();

      await serve((socket) => socket.receiveText(READY));
      expect(asked()).toEqual({ issue: "issue-1", tree: "tree-1", t: "t-1" });
      expect(replace).toHaveBeenCalledTimes(1);
    });

    it.each(["?issue=issue-1", "?tree=tree-1"])("starts nothing while the address names %s alone", async (search) => {
      const { replace } = renderPage(search);

      // An address that names neither a tree nor a directory asks for one.
      expect(directory().value).toBe("");
      expect(screen.queryByText("Agent terminal")).toBeNull();
      expect(FakeSocket.instances).toHaveLength(0);
      expect(replace).not.toHaveBeenCalled();
    });

    it("rejoins the session the address names instead of starting the agent again", async () => {
      const { replace, socket } = await opened(`${AGENT}&t=t-9`);

      expect(socket.control()).toEqual([{ type: "attach", terminalId: "t-9" }]);
      await serve((it) =>
        it.receiveText(JSON.stringify({ type: "ready", terminalId: "t-9", pid: 7, cols: 80, rows: 24 })),
      );
      expect(replace).not.toHaveBeenCalled();
    });

    it("restarts the agent once its terminal has ended, and not before", async () => {
      const { replace, asked, socket } = await opened(`${AGENT}&t=t-1`);
      const restart = () => screen.getByRole("button", { name: "Restart" });
      expect(restart()).toBeDisabled();
      await serve((it) => it.receiveText(READY));
      expect(restart()).toBeDisabled();

      await serve((it) => it.receiveText(exit));

      expect(restart()).toBeEnabled();
      expect(screen.queryByRole("button", { name: "End session" })).toBeNull();

      fireEvent.click(restart());

      // The ended session's id leaves the address: a switch back to the tab
      // would otherwise rejoin a terminal that is gone.
      expect(replace).toHaveBeenLastCalledWith("/acme/terminal?issue=issue-1&tree=tree-1");
      expect(asked()).toEqual({ issue: "issue-1", tree: "tree-1" });
      await settle();
      expect(FakeSocket.instances).toHaveLength(2);
      expect(socket.control().some((message) => message.type === "close")).toBe(false);
      await serve((it) => it.open());
      expect(FakeSocket.last.control()).toMatchObject([open]);

      // A session this page started is not rejoined, so nothing is announced.
      const announcements = fakeTerminal.writes.length;
      await serve((it) => it.receiveText(READY));
      expect(fakeTerminal.writes).toHaveLength(announcements);
    });

    it("follows the address from a tree to a directory, leaving the agent running", async () => {
      const { commit, socket } = await opened(AGENT);
      await serve((it) => it.receiveText(READY));

      await commit(`${PAGE}?cwd=%2Fsrv%2Fother`);
      await settle();

      expect(FakeSocket.instances).toHaveLength(2);
      expect(socket.closeCalls).toBe(1);
      expect(socket.control().some((message) => message.type === "close")).toBe(false);
      expect(screen.queryByText("Agent terminal")).toBeNull();
      await serve((it) => it.open());
      expect(FakeSocket.last.control()).toMatchObject([{ type: "open", cwd: "/srv/other" }]);
    });
  });
});
