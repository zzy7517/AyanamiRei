// @vitest-environment jsdom
import { useMemo, useState, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import type { Project, ProjectResource } from "@multica/core/types";
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
  listProjects: vi.fn(),
  listProjectResources: vi.fn(),
}));

// The list offers to add a project; the modal it opens has its own tests.
const modalMocks = vi.hoisted(() => ({ open: vi.fn() }));

vi.mock("@multica/core/api", () => ({
  ApiError: class ApiError extends Error {
    constructor(message: string, readonly status: number) {
      super(message);
    }
  },
  api: {
    createTerminalTicket: apiMocks.createTerminalTicket,
    listProjects: apiMocks.listProjects,
    listProjectResources: apiMocks.listProjectResources,
    getBaseUrl: () => "https://multica.test",
  },
}));
vi.mock("@multica/core/modals", () => ({
  useModalStore: Object.assign(vi.fn(), {
    getState: () => ({ open: modalMocks.open }),
  }),
}));

const READY = JSON.stringify({ type: "ready", terminalId: "t-1", pid: 4242, cols: 120, rows: 40 });
const PAGE = "https://app.test/acme/terminal";

function project(id: string, title: string, over: Partial<Project> = {}): Project {
  return {
    id,
    workspace_id: "ws-1",
    title,
    description: null,
    icon: null,
    status: "planned",
    priority: "none",
    lead_type: null,
    lead_id: null,
    start_date: null,
    due_date: null,
    created_at: "2026-11-24T00:00:00Z",
    updated_at: "2026-11-24T00:00:00Z",
    issue_count: 0,
    done_count: 0,
    resource_count: 0,
    ...over,
  };
}

const alpha = project("alpha", "Alpha", { resource_count: 1 });
const alphaFolder: ProjectResource = {
  id: "folder-alpha",
  project_id: "alpha",
  workspace_id: "ws-1",
  resource_type: "local_directory",
  resource_ref: { local_path: "/srv/alpha", daemon_id: "daemon-1" },
  label: null,
  position: 0,
  created_at: "2026-11-24T00:00:00Z",
  created_by: null,
};
const beta = project("beta", "Beta", { resource_count: 1 });
const betaFolder: ProjectResource = {
  ...alphaFolder,
  id: "folder-beta",
  project_id: "beta",
  resource_ref: { local_path: "C:\\code\\beta", daemon_id: "daemon-1" },
};
const archived = project("gamma", "Gamma", { resource_count: 1, archived_at: "2026-11-01T00:00:00Z" });
const archivedFolder: ProjectResource = { ...alphaFolder, id: "folder-gamma", project_id: "gamma" };

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

/** The server's answers about projects, and the folders they registered. */
function projectsOf(...projects: Project[]) {
  apiMocks.listProjects.mockResolvedValue({ projects, total: projects.length });
  apiMocks.listProjectResources.mockImplementation(async (projectId: string) => {
    const resources = [alphaFolder, betaFolder, archivedFolder].filter(
      (resource) => resource.project_id === projectId,
    );
    return { resources, total: resources.length };
  });
}

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
  const push = vi.fn();
  // Only ever called from inside the page's own events and effects, which a
  // test has already put in an act() scope.
  const replace = vi.fn((path: string) => {
    show(new URL(path, "https://app.test"));
  });
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  function Shell({ children }: { children: ReactNode }) {
    const [url, setUrl] = useState(() => new URL(`${PAGE}${search}`));
    const resources = useMemo(() => getTestResources("en"), []);
    const navigation = useMemo<NavigationAdapter>(
      () => ({
        push,
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
        <QueryClientProvider client={qc}>
          <NavigationProvider value={navigation}>
            <WorkspaceSlugProvider slug="acme">{children}</WorkspaceSlugProvider>
          </NavigationProvider>
        </QueryClientProvider>
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
  return { ...view, push, replace, commit, asked };
}

async function opened(search = "?cwd=%2Fsrv%2Fapp") {
  const view = renderPage(search);
  await settle();
  await serve((socket) => socket.open());
  return { ...view, socket: FakeSocket.last };
}

beforeEach(() => {
  FakeSocket.instances = [];
  fakeTerminal.writes = [];
  modalMocks.open.mockReset();
  apiMocks.createTerminalTicket.mockResolvedValue({ ticket: "tk", expiresInSeconds: 120 });
  apiMocks.listProjects.mockReset().mockResolvedValue({ projects: [], total: 0 });
  apiMocks.listProjectResources.mockReset().mockResolvedValue({ resources: [], total: 0 });
  vi.stubGlobal("WebSocket", FakeSocket as unknown as typeof WebSocket);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("TerminalPage", () => {
  it("lists the default project and the folders of the projects that are not archived", async () => {
    projectsOf(alpha, beta, archived, project("delta", "Delta"));
    renderPage("");

    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Terminal");
    expect(screen.getByRole("link", { name: "Default project" })).toHaveAttribute(
      "href",
      "/acme/terminal?project=default",
    );
    // Each folder is named by its project, and the path is shown as it was registered.
    const alphaRow = await screen.findByRole("link", { name: /^Alpha/ });
    expect(within(alphaRow).getByText("/srv/alpha")).toBeInTheDocument();
    expect(alphaRow).toHaveAttribute("href", "/acme/terminal?project=folder-alpha");
    const betaRow = screen.getByRole("link", { name: /^Beta/ });
    expect(within(betaRow).getByText("C:\\code\\beta")).toBeInTheDocument();
    expect(betaRow).toHaveAttribute("href", "/acme/terminal?project=folder-beta");
    // An archived project is no longer offered, and a project without resources is not asked.
    expect(screen.queryByText("Gamma")).toBeNull();
    expect(screen.queryByText("Delta")).toBeNull();
    expect(apiMocks.listProjectResources.mock.calls.map(([id]) => id).sort()).toEqual(["alpha", "beta"]);
    expect(screen.queryByText("No project has a local folder yet.")).toBeNull();
    expect(screen.queryByRole("button", { name: "End session" })).toBeNull();
    expect(FakeSocket.instances).toHaveLength(0);
    // Nothing is shown that a reply could land on.
    expect(replyHooks.useIssueShown).toHaveBeenLastCalledWith(null);
    expect(replyHooks.useMarkAgentReplySeenWhileViewing).toHaveBeenLastCalledWith("ws-1", "");
  });

  it("says so once it knows that no project has a folder, and offers to add one", async () => {
    const list = deferred<{ projects: Project[]; total: number }>();
    apiMocks.listProjects.mockReturnValue(list.promise);
    const { replace } = renderPage("");

    // Not before the projects have answered: the folder of one may still be coming.
    expect(screen.getByRole("link", { name: "Default project" })).toBeInTheDocument();
    expect(screen.queryByText("No project has a local folder yet.")).toBeNull();

    await act(async () => {
      list.resolve({ projects: [project("delta", "Delta")], total: 1 });
      await list.promise;
    });
    expect(await screen.findByText("No project has a local folder yet.")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Add project" }));
    expect(modalMocks.open).toHaveBeenCalledWith("create-project");
    expect(replace).not.toHaveBeenCalled();
    expect(FakeSocket.instances).toHaveLength(0);
  });

  it("opens a shell in the project chosen from the list", async () => {
    projectsOf(alpha);
    const { push, commit } = renderPage("");

    fireEvent.click(await screen.findByRole("link", { name: /^Alpha/ }));
    expect(push).toHaveBeenLastCalledWith("/acme/terminal?project=folder-alpha");

    // What the router then shows.
    await commit("/acme/terminal?project=folder-alpha");
    await settle();
    await serve((socket) => socket.open());
    expect(FakeSocket.last.control()).toMatchObject([{ type: "open", project: "folder-alpha" }]);
  });

  describe("a shell in a directory of a task", () => {
    it("opens a shell in the directory the address names, and shows where it is", async () => {
      const { socket } = await opened();
      expect(socket.control()).toMatchObject([{ type: "open", cwd: "/srv/app" }]);
      expect(screen.getByText("/srv/app")).toBeInTheDocument();
      // The task chose the directory: there is nothing to type here, and no
      // list this page was opened from.
      expect(screen.queryByRole("textbox")).toBeNull();
      expect(screen.queryByRole("button", { name: "All projects" })).toBeNull();
      expect(screen.getByRole("button", { name: "Restart" })).toBeDisabled();
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

    it("ends the session when asked", async () => {
      const { socket } = await opened();
      await serve((it) => it.receiveText(READY));

      fireEvent.click(screen.getByRole("button", { name: "End session" }));

      expect(socket.control().at(-1)).toEqual({ type: "close", terminalId: "t-1" });
    });

    it("starts another shell in the same directory once the first has ended", async () => {
      const { replace } = await opened();
      await serve((it) => it.receiveText(READY));
      await serve((it) => it.receiveText(JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: 0 })));
      expect(screen.getByRole("button", { name: "Restart" })).toBeEnabled();
      expect(screen.queryByRole("button", { name: "End session" })).toBeNull();
      // The address keeps the id, so returning to the tab rejoins nothing silently.
      expect(replace).toHaveBeenCalledTimes(1);

      fireEvent.click(screen.getByRole("button", { name: "Restart" }));

      expect(replace).toHaveBeenLastCalledWith("/acme/terminal?cwd=%2Fsrv%2Fapp");
      await settle();
      expect(FakeSocket.instances).toHaveLength(2);
      await serve((socket) => socket.open());
      expect(FakeSocket.last.control()).toMatchObject([{ type: "open", cwd: "/srv/app" }]);
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
  });

  describe("a project's terminal", () => {
    const PROJECT = "?project=folder-alpha";
    const open = { type: "open", project: "folder-alpha" };
    const exit = JSON.stringify({ type: "exit", terminalId: "t-1", exitCode: 0 });
    const back = () => screen.getByRole("button", { name: "All projects" });

    it("names the project and its folder, and asks for the folder by its id", async () => {
      projectsOf(alpha);
      const { socket } = await opened(PROJECT);

      expect(await screen.findByText("Alpha")).toBeInTheDocument();
      expect(screen.getByText("/srv/alpha")).toBeInTheDocument();
      expect(socket.control()).toMatchObject([open]);
      expect(back()).toBeDisabled();
      expect(screen.getByRole("button", { name: "Restart" })).toBeDisabled();
    });

    it("writes the terminal's id into the address once the server names it", async () => {
      const { replace, asked } = await opened(PROJECT);
      await serve((socket) => socket.receiveText(READY));

      expect(replace).toHaveBeenCalledTimes(1);
      expect(asked()).toEqual({ project: "folder-alpha", t: "t-1" });
      // The id arriving in the address is not another session.
      expect(FakeSocket.instances).toHaveLength(1);
    });

    it("rejoins the terminal the address names instead of opening another", async () => {
      const { replace, socket } = await opened(`${PROJECT}&t=t-9`);
      expect(socket.control()).toEqual([{ type: "attach", terminalId: "t-9" }]);

      await serve((it) =>
        it.receiveText(JSON.stringify({ type: "ready", terminalId: "t-9", pid: 7, cols: 80, rows: 24 })),
      );
      expect(replace).not.toHaveBeenCalled();
    });

    it("lets go back to the list once the shell has ended, and not before", async () => {
      const { replace, socket } = await opened(PROJECT);
      expect(back()).toBeDisabled();
      await serve((it) => it.receiveText(READY));
      expect(back()).toBeDisabled();

      await serve((it) => it.receiveText(exit));
      expect(back()).toBeEnabled();
      fireEvent.click(back());

      // The list takes the terminal's place. The socket goes; nothing is asked
      // of a shell that has already ended.
      expect(replace).toHaveBeenLastCalledWith("/acme/terminal");
      expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Terminal");
      expect(socket.control().some((message) => message.type === "close")).toBe(false);
    });

    it("restarts the shell once it has ended, and not before", async () => {
      const { replace, asked, socket } = await opened(`${PROJECT}&t=t-1`);
      const restart = () => screen.getByRole("button", { name: "Restart" });
      expect(restart()).toBeDisabled();
      await serve((it) => it.receiveText(READY));
      expect(restart()).toBeDisabled();

      await serve((it) => it.receiveText(exit));
      expect(restart()).toBeEnabled();
      expect(screen.queryByRole("button", { name: "End session" })).toBeNull();

      fireEvent.click(restart());

      // The ended terminal's id leaves the address: a switch back to the tab
      // would otherwise rejoin a terminal that is gone.
      expect(replace).toHaveBeenLastCalledWith("/acme/terminal?project=folder-alpha");
      expect(asked()).toEqual({ project: "folder-alpha" });
      await settle();
      expect(FakeSocket.instances).toHaveLength(2);
      expect(socket.control().some((message) => message.type === "close")).toBe(false);
      await serve((it) => it.open());
      expect(FakeSocket.last.control()).toMatchObject([open]);
    });

    it("follows the address to another project, leaving the first shell running", async () => {
      const { commit, socket } = await opened(PROJECT);
      await serve((it) => it.receiveText(READY));

      await commit(`${PAGE}?project=default`);
      await settle();

      expect(FakeSocket.instances).toHaveLength(2);
      expect(socket.closeCalls).toBe(1);
      expect(socket.control().some((message) => message.type === "close")).toBe(false);
      await serve((it) => it.open());
      expect(FakeSocket.last.control()).toMatchObject([{ type: "open", project: "default" }]);
    });
  });

  describe("the default project", () => {
    it("opens without any project added, and asks for none", async () => {
      const { socket } = await opened("?project=default");

      expect(socket.control()).toMatchObject([{ type: "open", project: "default" }]);
      expect(screen.getByText("Default project")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "All projects" })).toBeDisabled();
      expect(apiMocks.listProjects).not.toHaveBeenCalled();
    });
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
      // The tree is where the agent runs: nothing is typed in on this page.
      expect(screen.queryByRole("textbox")).toBeNull();
      expect(screen.getByText("Agent terminal")).toBeInTheDocument();

      await serve((socket) => socket.receiveText(READY));
      expect(asked()).toEqual({ issue: "issue-1", tree: "tree-1", t: "t-1" });
      expect(replace).toHaveBeenCalledTimes(1);
    });

    it.each(["?issue=issue-1", "?tree=tree-1", "?t=t-1"])(
      "starts nothing while the address names %s alone",
      async (search) => {
        const { replace } = renderPage(search);

        // A tree needs its task, and an id says only which terminal to rejoin:
        // neither says what to open, so the list of projects is shown.
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Terminal");
        expect(screen.queryByText("Agent terminal")).toBeNull();
        expect(FakeSocket.instances).toHaveLength(0);
        expect(replace).not.toHaveBeenCalled();
      },
    );

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
