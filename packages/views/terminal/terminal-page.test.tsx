// @vitest-environment jsdom
import { useMemo, useState, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import type {
  TerminalHostBridge,
  TerminalHostEvent,
  TerminalHostOpenResult,
  TerminalSize,
} from "@multica/core/terminal";
import type { TreeEnsureResult, TreeRemoveResult, TreeSpec } from "@multica/core/tasktrees";
import type { Project, ProjectResource } from "@multica/core/types";
import { getTestResources } from "../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../navigation";
import { TerminalPage } from "./terminal-page";

// The terminal itself has its own tests; here only what the page wires to it.
// The desktop app's main process stands in for the owner of terminals and
// builder of trees: the bridges the page reaches through `window.desktopAPI`.

const fakeTerminalView = vi.hoisted(() => ({ resets: 0 }));

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
// reports the terminal's id once the session is ready, can be reset, and says
// why the owner refused a start.
vi.mock("./terminal", async () => {
  const { forwardRef, useEffect, useImperativeHandle } = await import("react");
  const { useTerminalSessionState } = await import("@multica/core/terminal");
  const { useT } = await import("../i18n");
  return {
    Terminal: forwardRef<{ write(data: string): void; reset(): void; focus(): void }, object>(
      function FakeTerminal(props, ref) {
        const { session, onReady } = props as {
          session: import("@multica/core/terminal").TerminalSession;
          onReady?: (terminalId: string) => void;
        };
        const state = useTerminalSessionState(session);
        const { t } = useT("terminal");
        useEffect(() => {
          if (state.status === "ready" && state.terminalId) onReady?.(state.terminalId);
        }, [state.status, state.terminalId, onReady]);
        useImperativeHandle(
          ref,
          () => ({
            write: () => {},
            reset: () => void fakeTerminalView.resets++,
            focus: () => {},
          }),
          [],
        );
        if (state.status === "error") {
          const code = state.error?.code;
          return (
            <div role="alert">
              <p>{code ? t(($) => $.errors[code]) : null}</p>
              {state.error?.message ? <p>{state.error.message}</p> : null}
            </div>
          );
        }
        return null;
      },
    ),
  };
});

const apiMocks = vi.hoisted(() => ({
  getIssue: vi.fn(),
  getIssueProjectScope: vi.fn(),
  listProjects: vi.fn(),
  listProjectResources: vi.fn(),
  reportIssueProjectTreeState: vi.fn(),
  reportIssueAgentReply: vi.fn(),
}));

vi.mock("@multica/core/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@multica/core/api")>();
  return { ...real, api: apiMocks };
});

// The list offers to add a project; the modal it opens has its own tests.
const modalMocks = vi.hoisted(() => ({ open: vi.fn() }));

vi.mock("@multica/core/modals", () => ({
  useModalStore: Object.assign(vi.fn(), {
    getState: () => ({ open: modalMocks.open }),
  }),
}));

/** The owner of terminals, driven by hand: no pty, answers the test scripts. */
class FakeTerminalHost implements TerminalHostBridge {
  static instances: FakeTerminalHost[] = [];
  openCalls: { target: unknown; size: TerminalSize }[] = [];
  attachCalls: { terminalId: string; size: TerminalSize }[] = [];
  endCalls: string[] = [];
  detachCalls: string[] = [];
  /** The answers to `open`, in order; the default names the terminal t-1. */
  openResults: TerminalHostOpenResult[] = [];
  private listeners = new Set<(event: TerminalHostEvent) => void>();

  constructor() {
    FakeTerminalHost.instances.push(this);
  }
  open(target: unknown, size: TerminalSize): Promise<TerminalHostOpenResult> {
    this.openCalls.push({ target, size });
    const result = this.openResults.shift() ?? { ok: true, terminalId: "t-1", pid: 4242, cwd: "" };
    return Promise.resolve(result);
  }
  attach(terminalId: string, size: TerminalSize) {
    this.attachCalls.push({ terminalId, size });
    return Promise.resolve({ ok: true as const, running: true, exit: null, screen: "" });
  }
  detach(terminalId: string) {
    this.detachCalls.push(terminalId);
  }
  write() {}
  resize() {}
  end(terminalId: string) {
    this.endCalls.push(terminalId);
  }
  onEvent(listener: (event: TerminalHostEvent) => void) {
    this.listeners.add(listener);
    return () => void this.listeners.delete(listener);
  }
  /** The app's turn: an event about the terminal, shown by React. */
  emit(event: TerminalHostEvent) {
    for (const listener of [...this.listeners]) listener(event);
  }
  static get last(): FakeTerminalHost {
    return FakeTerminalHost.instances.at(-1)!;
  }
}

/** The builder of trees, driven by hand: answers the test scripts. */
class FakeTreeHost {
  static instances: FakeTreeHost[] = [];
  ensureCalls: { place: unknown; taskKey: string; specs: TreeSpec[] }[] = [];
  /** The answers to `ensure`, in order; the default builds one ready tree. */
  ensureResults: TreeEnsureResult[] = [];
  /** When set, `ensure` answers with this promise until it is released. */
  private held: { promise: Promise<TreeEnsureResult>; release: (r: TreeEnsureResult) => void } | null = null;

  constructor() {
    FakeTreeHost.instances.push(this);
  }
  ensure(place: unknown, taskKey: string, specs: TreeSpec[]): Promise<TreeEnsureResult> {
    this.ensureCalls.push({ place, taskKey, specs });
    if (this.held) return this.held.promise;
    const result = this.ensureResults.shift() ?? {
      ok: true as const,
      trees: [{ path: "/srv/trees/MUL-1/proj-1", branch: "task/MUL-1" }],
    };
    return Promise.resolve(result);
  }
  remove(): Promise<TreeRemoveResult> {
    return Promise.resolve({ ok: true as const, removed: [] });
  }
  /** Holds the next `ensure` answer until it is released with a result. */
  hold() {
    let release!: (r: TreeEnsureResult) => void;
    const promise = new Promise<TreeEnsureResult>((resolve) => {
      release = resolve;
    });
    this.held = { promise, release };
  }
  release(result: TreeEnsureResult = { ok: true, trees: [{ path: "/srv/trees/MUL-1/proj-1", branch: "task/MUL-1" }] }) {
    const held = this.held;
    this.held = null;
    held?.release(result);
  }
  static get last(): FakeTreeHost {
    return FakeTreeHost.instances.at(-1)!;
  }
}

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

function folderResource(
  id: string,
  projectId: string,
  ref: ProjectResource["resource_ref"],
  over: Partial<ProjectResource> = {},
): ProjectResource {
  return {
    id,
    project_id: projectId,
    workspace_id: "ws-1",
    resource_type: "host" in ref ? "ssh_directory" : "local_directory",
    resource_ref: ref,
    label: null,
    position: 0,
    created_at: "2026-11-24T00:00:00Z",
    created_by: null,
    ...over,
  };
}

const alpha = project("alpha", "Alpha", { resource_count: 1 });
const alphaFolder = folderResource("folder-alpha", "alpha", { local_path: "/srv/alpha", daemon_id: "daemon-1" });
const beta = project("beta", "Beta", { resource_count: 1 });
const betaFolder = folderResource("folder-beta", "beta", { local_path: "C:\\code\\beta", daemon_id: "daemon-1" });
const remote = project("remote", "Remote", { resource_count: 1 });
const remoteFolder = folderResource("folder-remote", "remote", { host: "bastion", path: "/srv/remote" });
const archived = project("gamma", "Gamma", { resource_count: 1, archived_at: "2026-11-01T00:00:00Z" });
const archivedFolder = folderResource("folder-gamma", "gamma", { local_path: "/srv/gamma", daemon_id: "daemon-1" });

/** The agent the agent terminal starts: the page's own words for the task. */
function issueOf(issueId: string) {
  return {
    id: issueId,
    workspace_id: "ws-1",
    identifier: "MUL-1",
    title: "Ship it",
    description: "Do it",
  };
}

/** The task's scope: one pair, whose tree the server says is ready. */
function scopeOf() {
  return {
    items: [
      {
        project_id: "alpha",
        base_branch: "main",
        position: 0,
        tree: {
          id: "tree-1",
          state: "ready",
          path: "/srv/trees/MUL-1/proj-1",
          task_branch: "task/MUL-1",
          error: "",
          created_at: "2026-11-24T00:00:00Z",
          updated_at: "2026-11-24T00:00:00Z",
        },
      },
    ],
  };
}

const settled = async () => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
};

/** The server's answers about projects, and the folders they registered. */
function projectsOf(...projects: Project[]) {
  apiMocks.listProjects.mockResolvedValue({ projects, total: projects.length });
  apiMocks.listProjectResources.mockImplementation(async (projectId: string) => {
    const resources = [alphaFolder, betaFolder, remoteFolder, archivedFolder].filter(
      (resource) => resource.project_id === projectId,
    );
    return { resources, total: resources.length };
  });
}

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

/** Opens the page and waits for the app to take its first terminal call. */
async function opened(search = "?cwd=%2Fsrv%2Fapp") {
  const view = renderPage(search);
  await waitFor(() =>
    expect(
      FakeTerminalHost.last.openCalls.length + FakeTerminalHost.last.attachCalls.length,
    ).toBeGreaterThan(0),
  );
  await settled();
  return { ...view, bridge: FakeTerminalHost.last, trees: FakeTreeHost.last };
}

/** The app's turn: the terminal's process ended, shown by React. */
const exited = (bridge: FakeTerminalHost, terminalId = "t-1") =>
  act(async () => bridge.emit({ terminalId, type: "exit", exit: { exitCode: 0 } }));

beforeEach(() => {
  vi.clearAllMocks();
  FakeTerminalHost.instances = [];
  FakeTreeHost.instances = [];
  fakeTerminalView.resets = 0;
  modalMocks.open.mockReset();
  apiMocks.getIssue.mockImplementation(async (id: string) => issueOf(id));
  apiMocks.getIssueProjectScope.mockImplementation(async () => scopeOf());
  // The workspace's projects and their folders, as any list would show them.
  projectsOf(alpha, beta, remote, archived);
  apiMocks.reportIssueProjectTreeState.mockReset().mockResolvedValue(null);
  apiMocks.reportIssueAgentReply.mockReset().mockResolvedValue(undefined);
  (window as unknown as { desktopAPI?: unknown }).desktopAPI = {
    terminal: new FakeTerminalHost(),
    trees: new FakeTreeHost(),
  };
});

afterEach(() => {
  delete (window as unknown as { desktopAPI?: unknown }).desktopAPI;
});

describe("TerminalPage", () => {
  it("lists the default project and the folders of the projects that are not archived", async () => {
    projectsOf(alpha, beta, remote, archived, project("delta", "Delta"));
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
    // An SSH folder names its host, as a person would.
    const remoteRow = screen.getByRole("link", { name: /^Remote/ });
    expect(within(remoteRow).getByText("bastion:/srv/remote")).toBeInTheDocument();
    // An archived project is no longer offered, and a project without resources is not asked.
    expect(screen.queryByText("Gamma")).toBeNull();
    expect(screen.queryByText("Delta")).toBeNull();
    expect(apiMocks.listProjectResources.mock.calls.map(([id]) => id).sort()).toEqual([
      "alpha",
      "beta",
      "remote",
    ]);
    expect(screen.queryByText("No project has a folder yet.")).toBeNull();
    expect(screen.queryByRole("button", { name: "End session" })).toBeNull();
    expect(FakeTerminalHost.last.openCalls).toHaveLength(0);
    // Nothing is shown that a reply could land on.
    expect(replyHooks.useIssueShown).toHaveBeenLastCalledWith(null);
    expect(replyHooks.useMarkAgentReplySeenWhileViewing).toHaveBeenLastCalledWith("ws-1", "");
  });

  it("says so once it knows that no project has a folder, and offers to add one", async () => {
    let resolveList!: (value: { projects: Project[]; total: number }) => void;
    apiMocks.listProjects.mockReturnValue(
      new Promise((resolve) => {
        resolveList = resolve;
      }),
    );
    const { replace } = renderPage("");

    // Not before the projects have answered: the folder of one may still be coming.
    expect(screen.getByRole("link", { name: "Default project" })).toBeInTheDocument();
    expect(screen.queryByText("No project has a folder yet.")).toBeNull();

    await act(async () => {
      resolveList({ projects: [project("delta", "Delta")], total: 1 });
    });
    expect(await screen.findByText("No project has a folder yet.")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Add project" }));
    expect(modalMocks.open).toHaveBeenCalledWith("create-project");
    expect(replace).not.toHaveBeenCalled();
    expect(FakeTerminalHost.last.openCalls).toHaveLength(0);
  });

  it("opens a shell in the project chosen from the list", async () => {
    projectsOf(alpha);
    const { push, commit } = renderPage("");

    fireEvent.click(await screen.findByRole("link", { name: /^Alpha/ }));
    expect(push).toHaveBeenLastCalledWith("/acme/terminal?project=folder-alpha");

    // What the router then shows.
    await commit("/acme/terminal?project=folder-alpha");
    await waitFor(() => expect(FakeTerminalHost.last.openCalls).toHaveLength(1));
    expect(FakeTerminalHost.last.openCalls[0]!.target).toEqual({
      kind: "shell",
      place: { kind: "local" },
      cwd: "/srv/alpha",
    });
  });

  describe("a shell in a directory of a task", () => {
    it("opens a shell in the directory the address names, and shows where it is", async () => {
      const { bridge } = await opened();
      expect(bridge.openCalls[0]!.target).toEqual({
        kind: "shell",
        place: { kind: "local" },
        cwd: "/srv/app",
      });
      expect(screen.getByText("/srv/app")).toBeInTheDocument();
      // The task chose the directory: there is nothing to type here.
      expect(screen.queryByRole("textbox")).toBeNull();
      expect(screen.getByRole("button", { name: "All projects" })).toBeDisabled();
      expect(screen.getByRole("button", { name: "Restart" })).toBeDisabled();
    });

    it("opens a shell on the host the address names", async () => {
      const { bridge } = await opened("?cwd=%2Fsrv%2Fapp&host=bastion");
      expect(bridge.openCalls[0]!.target).toEqual({
        kind: "shell",
        place: { kind: "ssh", host: "bastion" },
        cwd: "/srv/app",
      });
      expect(screen.getByText("bastion:/srv/app")).toBeInTheDocument();
    });

    it("writes the terminal's id into the address once the owner names it", async () => {
      const { replace, asked } = await opened();

      expect(replace).toHaveBeenCalledTimes(1);
      expect(replace.mock.lastCall![0]).toMatch(/^\/acme\/terminal\?/);
      expect(asked()).toEqual({ cwd: "/srv/app", t: "t-1" });
    });

    it("keeps the terminal running when that id comes back in the address", async () => {
      const { replace, bridge } = await opened();
      // The router shows the address that was asked for, id and all.
      expect(replace).toHaveBeenCalledTimes(1);

      expect(bridge.openCalls).toHaveLength(1);
      expect(bridge.attachCalls).toHaveLength(0);
      expect(bridge.detachCalls).toHaveLength(0);
      expect(bridge.endCalls).toHaveLength(0);
      expect(replace).toHaveBeenCalledTimes(1);
    });

    it("rejoins the terminal the address names instead of opening another", async () => {
      const { replace, bridge } = await opened("?cwd=%2Fsrv%2Fapp&t=t-9");
      expect(bridge.attachCalls).toEqual([{ terminalId: "t-9", size: { cols: 80, rows: 24 } }]);
      expect(bridge.openCalls).toHaveLength(0);
      expect(replace).not.toHaveBeenCalled();
    });

    it("ends the session when asked", async () => {
      const { bridge } = await opened();

      fireEvent.click(screen.getByRole("button", { name: "End session" }));

      expect(bridge.endCalls).toEqual(["t-1"]);
    });

    it("starts another shell in the same directory once the first has ended", async () => {
      const { replace, bridge } = await opened();
      await exited(bridge);
      expect(screen.getByRole("button", { name: "Restart" })).toBeEnabled();
      expect(screen.queryByRole("button", { name: "End session" })).toBeNull();
      // The address keeps the id, so returning to the tab rejoins nothing silently.
      expect(replace).toHaveBeenCalledTimes(1);

      fireEvent.click(screen.getByRole("button", { name: "Restart" }));

      expect(replace).toHaveBeenLastCalledWith("/acme/terminal?cwd=%2Fsrv%2Fapp");
      await settled();
      // The ended terminal is left to itself; a new one opens in its place.
      expect(bridge.detachCalls).toEqual(["t-1"]);
      expect(bridge.endCalls).toHaveLength(0);
      expect(bridge.openCalls).toHaveLength(2);
      expect(bridge.openCalls[1]!.target).toEqual({
        kind: "shell",
        place: { kind: "local" },
        cwd: "/srv/app",
      });
    });

    it("follows the address to another directory, leaving the first shell running", async () => {
      const { commit, bridge } = await opened();

      await commit("https://app.test/acme/terminal?cwd=%2Fsrv%2Fother");
      await settled();

      expect(bridge.openCalls).toHaveLength(2);
      expect(bridge.detachCalls).toEqual(["t-1"]);
      expect(bridge.endCalls).toHaveLength(0);
      expect(bridge.openCalls[1]!.target).toEqual({
        kind: "shell",
        place: { kind: "local" },
        cwd: "/srv/other",
      });
    });

    it("leaves the terminal running when the page goes away", async () => {
      const { unmount, bridge } = await opened();
      unmount();

      expect(bridge.detachCalls).toEqual(["t-1"]);
      expect(bridge.endCalls).toHaveLength(0);
    });
  });

  describe("a project's terminal", () => {
    const PROJECT = "?project=folder-alpha";
    const open = { kind: "shell", place: { kind: "local" }, cwd: "/srv/alpha" };
    const back = () => screen.getByRole("button", { name: "All projects" });

    it("names the project and its folder, and asks for the folder by its id", async () => {
      projectsOf(alpha);
      const { bridge } = await opened(PROJECT);

      expect(await screen.findByText("Alpha")).toBeInTheDocument();
      expect(screen.getByText("/srv/alpha")).toBeInTheDocument();
      expect(bridge.openCalls[0]!.target).toEqual(open);
      expect(back()).toBeDisabled();
      expect(screen.getByRole("button", { name: "Restart" })).toBeDisabled();
    });

    it("writes the terminal's id into the address once the owner names it", async () => {
      projectsOf(alpha);
      const { replace, asked, bridge } = await opened(PROJECT);

      expect(replace).toHaveBeenCalledTimes(1);
      expect(asked()).toEqual({ project: "folder-alpha", t: "t-1" });
      // The id arriving in the address is not another session.
      expect(bridge.openCalls).toHaveLength(1);
    });

    it("rejoins the terminal the address names instead of opening another", async () => {
      projectsOf(alpha);
      const { replace, bridge } = await opened(`${PROJECT}&t=t-9`);
      expect(bridge.attachCalls).toEqual([{ terminalId: "t-9", size: { cols: 80, rows: 24 } }]);
      expect(bridge.openCalls).toHaveLength(0);
      expect(replace).not.toHaveBeenCalled();
    });

    it("lets go back to the list once the shell has ended, and not before", async () => {
      projectsOf(alpha);
      const { replace, bridge } = await opened(PROJECT);
      expect(back()).toBeDisabled();

      await exited(bridge);
      expect(back()).toBeEnabled();
      fireEvent.click(back());

      // The list takes the terminal's place. Nothing is asked of a shell that
      // has already ended.
      expect(replace).toHaveBeenLastCalledWith("/acme/terminal");
      expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Terminal");
      expect(bridge.endCalls).toHaveLength(0);
    });

    it("restarts the shell once it has ended, and not before", async () => {
      projectsOf(alpha);
      const { replace, asked, bridge } = await opened(`${PROJECT}&t=t-1`);
      const restart = () => screen.getByRole("button", { name: "Restart" });
      expect(restart()).toBeDisabled();

      await exited(bridge);
      expect(restart()).toBeEnabled();
      expect(screen.queryByRole("button", { name: "End session" })).toBeNull();

      fireEvent.click(restart());

      // The ended terminal's id leaves the address: a switch back to the tab
      // would otherwise rejoin a terminal that is gone.
      expect(replace).toHaveBeenLastCalledWith("/acme/terminal?project=folder-alpha");
      expect(asked()).toEqual({ project: "folder-alpha" });
      await settled();
      expect(bridge.endCalls).toHaveLength(0);
      // The first call was the rejoin; the second opens the shell again.
      expect(bridge.attachCalls).toHaveLength(1);
      expect(bridge.openCalls).toHaveLength(1);
      expect(bridge.openCalls[0]!.target).toEqual(open);
    });

    it("follows the address to another project, leaving the first shell running", async () => {
      projectsOf(alpha);
      const { commit, bridge } = await opened(PROJECT);

      await commit(`${PAGE}?project=default`);
      await settled();

      expect(bridge.openCalls).toHaveLength(2);
      expect(bridge.detachCalls).toEqual(["t-1"]);
      expect(bridge.endCalls).toHaveLength(0);
      expect(bridge.openCalls[1]!.target).toEqual({ kind: "defaultProject" });
    });
  });

  describe("the default project", () => {
    it("opens without any project added, and asks for none", async () => {
      const { bridge } = await opened("?project=default");

      expect(bridge.openCalls[0]!.target).toEqual({ kind: "defaultProject" });
      expect(screen.getByText("Default project")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "All projects" })).toBeDisabled();
      expect(apiMocks.listProjects).not.toHaveBeenCalled();
    });
  });

  describe("an agent's terminal", () => {
    /** Each task has its own address, so no page is shown a remembered tree. */
    const AGENT = (issueId: string) => `?issue=${issueId}&tree=tree-1`;

    it("counts the agent's replies as seen while this terminal is shown", async () => {
      await opened(AGENT("issue-1"));

      expect(replyHooks.useIssueShown).toHaveBeenLastCalledWith("issue-1");
      expect(replyHooks.useMarkAgentReplySeenWhileViewing).toHaveBeenLastCalledWith("ws-1", "issue-1");
    });

    it("makes sure of the tree, then starts the agent in it, and asks for no directory", async () => {
      projectsOf(alpha);
      const { replace, asked, bridge, trees } = await opened(AGENT("issue-2"));

      // The tree was made sure of first: built next to the project's folder.
      expect(trees.ensureCalls).toHaveLength(1);
      expect(trees.ensureCalls[0]!.taskKey).toBe("MUL-1");
      expect(trees.ensureCalls[0]!.specs).toEqual([{ repo: "/srv/alpha", baseBranch: "main", build: true }]);
      expect(bridge.openCalls[0]!.target).toEqual({
        kind: "agent",
        place: { kind: "local" },
        cwd: "/srv/trees/MUL-1/proj-1",
        trees: [
          {
            path: "/srv/trees/MUL-1/proj-1",
            repoName: "Alpha",
            branch: "task/MUL-1",
            baseBranch: "main",
          },
        ],
        task: {
          id: "issue-2",
          workspaceId: "ws-1",
          identifier: "MUL-1",
          title: "Ship it",
          description: "Do it",
        },
      });
      // The tree is where the agent runs: nothing is typed in on this page.
      expect(screen.queryByRole("textbox")).toBeNull();
      expect(screen.getByText("Agent terminal")).toBeInTheDocument();

      expect(asked()).toEqual({ issue: "issue-2", tree: "tree-1", t: "t-1" });
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
        expect(FakeTerminalHost.last.openCalls).toHaveLength(0);
        expect(replace).not.toHaveBeenCalled();
      },
    );

    it("rejoins the session the address names instead of starting the agent again", async () => {
      const { replace, bridge } = await opened(`${AGENT("issue-3")}&t=t-9`);

      expect(bridge.attachCalls).toEqual([{ terminalId: "t-9", size: { cols: 80, rows: 24 } }]);
      expect(bridge.openCalls).toHaveLength(0);
      expect(FakeTreeHost.last.ensureCalls).toHaveLength(0);
      expect(replace).not.toHaveBeenCalled();
    });

    it("restarts the agent once its terminal has ended, and not before", async () => {
      const { replace, asked, bridge } = await opened(`${AGENT("issue-4")}&t=t-1`);
      const restart = () => screen.getByRole("button", { name: "Restart" });
      expect(restart()).toBeDisabled();

      await exited(bridge);
      expect(restart()).toBeEnabled();
      expect(screen.queryByRole("button", { name: "End session" })).toBeNull();

      fireEvent.click(restart());

      // The ended session's id leaves the address: a switch back to the tab
      // would otherwise rejoin a terminal that is gone.
      expect(replace).toHaveBeenLastCalledWith("/acme/terminal?issue=issue-4&tree=tree-1");
      expect(asked()).toEqual({ issue: "issue-4", tree: "tree-1" });
      await settled();
      expect(bridge.endCalls).toHaveLength(0);
      expect(bridge.attachCalls).toHaveLength(1);
      expect(bridge.openCalls).toHaveLength(1);
      expect(bridge.openCalls[0]!.target).toMatchObject({ kind: "agent", cwd: "/srv/trees/MUL-1/proj-1" });
    });

    it("follows the address from a tree to a directory, leaving the agent running", async () => {
      const { commit, bridge } = await opened(AGENT("issue-5"));

      await commit(`${PAGE}?cwd=%2Fsrv%2Fother`);
      await settled();

      expect(bridge.openCalls).toHaveLength(2);
      expect(bridge.detachCalls).toEqual(["t-1"]);
      expect(bridge.endCalls).toHaveLength(0);
      expect(screen.queryByText("Agent terminal")).toBeNull();
      expect(bridge.openCalls[1]!.target).toEqual({
        kind: "shell",
        place: { kind: "local" },
        cwd: "/srv/other",
      });
    });

    it("says so while the tree is being made sure of, and offers to try again when it fails", async () => {
      const trees = FakeTreeHost.last;
      trees.hold();
      renderPage(AGENT("issue-7"));

      // Making sure of the tree is announced while it runs.
      expect(await screen.findByText("Building the worktrees...")).toBeInTheDocument();
      expect(FakeTerminalHost.last.openCalls).toHaveLength(0);

      // A build that fails names its reason and offers to try again.
      trees.release({ ok: false, error: "no such branch: develop" });
      const alert = await screen.findByRole("alert");
      expect(alert).toHaveTextContent("no such branch: develop");

      // A second try that succeeds opens the agent.
      fireEvent.click(screen.getByRole("button", { name: "Retry" }));
      await waitFor(() => expect(FakeTerminalHost.last.openCalls).toHaveLength(1));
      expect(screen.queryByRole("alert")).toBeNull();
    });

    it("starts the agent again once the tree refused a start is there again", async () => {
      projectsOf(alpha);
      const bridge = FakeTerminalHost.last;
      const trees = FakeTreeHost.last;
      // First showing: the tree is made sure of, and the agent starts in it.
      const first = renderPage(AGENT("issue-6"));
      await waitFor(() => expect(bridge.openCalls).toHaveLength(1));
      first.unmount();

      // The tree's folder is removed by hand meanwhile. Shown again, the page
      // offers the remembered tree at once; the app refuses the start, and
      // makes sure of the tree again, held open until the refusal is seen.
      trees.hold();
      bridge.openResults = [{ ok: false, code: "tree_not_ready", message: "" }];
      const again = renderPage(AGENT("issue-6"));

      const alert = await screen.findByRole("alert");
      expect(alert).toHaveTextContent("The worktree is not built yet.");
      expect(bridge.openCalls).toHaveLength(2);
      expect(trees.ensureCalls).toHaveLength(2);

      // The tree is there again: the refused start is made without a click.
      trees.release();
      await waitFor(() => expect(bridge.openCalls).toHaveLength(3));
      expect(screen.queryByRole("alert")).toBeNull();
      expect(again.replace).toHaveBeenLastCalledWith(
        "/acme/terminal?issue=issue-6&tree=tree-1&t=t-1",
      );
    });
  });
});
