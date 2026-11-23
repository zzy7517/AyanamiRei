// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { WSClient } from "../api/ws-client";
import { registerAgentReplyBody, useIssueShown } from "../issues/agent-reply";
import { issueKeys } from "../issues/queries";
import { notificationPreferenceKeys } from "../notification-preferences/queries";
import type { IssueAgentRepliedPayload } from "../types/events";
import type { Issue } from "../types/issue";
import type { NotificationPreferences } from "../types/notification-preference";
import { workspaceKeys } from "../workspace/queries";
import { createAgentReplyNotifier, useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

vi.mock("../search-index/instance", () => ({
  forgetLocalSearchIndex: vi.fn(async () => undefined),
}));

vi.mock("../platform/workspace-storage", () => ({
  getCurrentWsId: vi.fn(() => "ws-a"),
  getCurrentSlug: () => "workspace-a",
  createWorkspaceAwareStorage: (adapter: unknown) => adapter,
  registerForWorkspaceRehydration: () => {},
}));

vi.mock("../paths", () => ({
  useHasOnboarded: () => true,
  resolvePostAuthDestination: () => "/",
}));

const REPLIED_AT = "2026-11-22T10:00:00Z";
const task = { id: "issue-1", identifier: "MUL-123", title: "Fix login", status: "todo" } as Issue;
const banner = {
  slug: "workspace-a",
  itemId: `agent-reply:issue-1:${REPLIED_AT}`,
  issueKey: "issue-1",
  title: "MUL-123 Fix login",
  body: "New reply",
};
const sidebarKey = issueKeys.sidebar("ws-a", "project:p1", { table: { scope: { kind: "project", project_id: "p1" }, filters: {}, sort: { field: "updated_at", direction: "desc" } } }, 25);

function reply(overrides: Partial<IssueAgentRepliedPayload> = {}): IssueAgentRepliedPayload {
  return { issue_id: "issue-1", agent_replied_at: REPLIED_AT, ...overrides };
}

describe("agent reply banners", () => {
  let qc: QueryClient;
  const showNotification = vi.fn();

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qc.setQueryData(workspaceKeys.list(), [{ id: "ws-a", slug: "workspace-a" }]);
    qc.setQueryData(notificationPreferenceKeys.all("ws-a"), { workspace_id: "ws-a", preferences: {} });
    registerAgentReplyBody("New reply");
    showNotification.mockReset();
    vi.stubGlobal("desktopAPI", { showNotification });
  });

  afterEach(() => {
    cleanup();
    qc.clear();
    registerAgentReplyBody(null);
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("names the task and says a reply is waiting", async () => {
    qc.setQueryData(issueKeys.detail("ws-a", "issue-1"), task);

    await createAgentReplyNotifier(qc)("ws-a", reply());

    expect(showNotification).toHaveBeenCalledExactlyOnceWith(banner);
  });

  it("takes the name from a list when the task page was never opened", async () => {
    qc.setQueryData(sidebarKey, { issues: [task], total: 1 });
    const fetchQuery = vi.spyOn(qc, "fetchQuery");

    await createAgentReplyNotifier(qc)("ws-a", reply());

    expect(showNotification).toHaveBeenCalledExactlyOnceWith(banner);
    expect(fetchQuery).not.toHaveBeenCalled();
  });

  it("reads the task when no cache holds it", async () => {
    const fetchQuery = vi.spyOn(qc, "fetchQuery").mockResolvedValue(task);

    await createAgentReplyNotifier(qc)("ws-a", reply());

    expect(showNotification).toHaveBeenCalledExactlyOnceWith(banner);
    expect(fetchQuery).toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: issueKeys.detail("ws-a", "issue-1"), staleTime: 0, retry: false }),
    );
  });

  it("says only that a reply arrived when the task cannot be read", async () => {
    vi.spyOn(qc, "fetchQuery").mockRejectedValue(new Error("offline"));

    await createAgentReplyNotifier(qc)("ws-a", reply());

    expect(showNotification).toHaveBeenCalledExactlyOnceWith({ ...banner, title: "New reply", body: "" });
  });

  it("says nothing while the task's page is on screen, and names it again afterwards", async () => {
    qc.setQueryData(issueKeys.detail("ws-a", "issue-1"), task);
    const notify = createAgentReplyNotifier(qc);
    const page = renderHook(() => useIssueShown("issue-1"));

    await notify("ws-a", reply());
    expect(showNotification).not.toHaveBeenCalled();

    page.unmount();
    await notify("ws-a", reply());
    expect(showNotification).toHaveBeenCalledExactlyOnceWith(banner);
  });

  it("says nothing when the viewer opens the task while its name is being read", async () => {
    let finish!: (value: Issue) => void;
    const fetchQuery = vi.spyOn(qc, "fetchQuery").mockReturnValue(
      new Promise<Issue>((resolve) => { finish = resolve; }),
    );
    const pending = createAgentReplyNotifier(qc)("ws-a", reply());
    await waitFor(() => expect(fetchQuery).toHaveBeenCalledTimes(1));
    renderHook(() => useIssueShown("issue-1"));

    await act(async () => {
      finish(task);
      await pending;
    });

    expect(showNotification).not.toHaveBeenCalled();
  });

  it.each([
    [{}, true],
    [{ comments: "muted" }, true],
    [{ system_notifications: "muted" }, false],
  ] as [NotificationPreferences, boolean][])("respects the workspace's preferences %j", async (preferences, visible) => {
    qc.setQueryData(issueKeys.detail("ws-a", "issue-1"), task);
    qc.setQueryData(notificationPreferenceKeys.all("ws-a"), { workspace_id: "ws-a", preferences });

    await createAgentReplyNotifier(qc)("ws-a", reply());

    expect(showNotification).toHaveBeenCalledTimes(visible ? 1 : 0);
  });

  it.each([
    ["the clearing of the marker", reply({ agent_replied_at: null })],
    ["an event without a task", reply({ issue_id: "" })],
  ])("says nothing for %s", async (_case, payload) => {
    await createAgentReplyNotifier(qc)("ws-a", payload);
    expect(showNotification).not.toHaveBeenCalled();
  });

  it("says nothing outside the desktop app, which has no bridge of its own", async () => {
    qc.setQueryData(issueKeys.detail("ws-a", "issue-1"), task);
    vi.stubGlobal("desktopAPI", undefined);

    await createAgentReplyNotifier(qc)("ws-a", reply());

    expect(showNotification).not.toHaveBeenCalled();
  });

  it("patches every cache that holds the task when the event arrives, and banners once", async () => {
    const ws = {
      on: vi.fn(() => () => {}),
      onAny: vi.fn(() => () => {}),
      onReconnect: vi.fn(() => () => {}),
    } as unknown as WSClient;
    const stores = {
      authStore: Object.assign(() => ({}), {
        getState: () => ({ user: { id: "u1" } }),
        subscribe: () => () => {},
        setState: () => {},
        destroy: () => {},
      }),
    } as unknown as RealtimeSyncStores;
    function Wrapper({ children }: { children: ReactNode }) {
      return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    }
    renderHook(() => useRealtimeSync(ws, stores), { wrapper: Wrapper });
    const handler = vi.mocked(ws.on).mock.calls.find(([event]) => event === "issue:agent_replied")?.[1] as (
      payload: unknown,
    ) => void;
    const onAny = vi.mocked(ws.onAny).mock.calls[0]![0];
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const seen = { ...task, agent_replied_at: null };
    qc.setQueryData(issueKeys.detail("ws-a", "issue-1"), seen);
    qc.setQueryData(sidebarKey, { issues: [seen], total: 1 });

    handler(reply());
    await waitFor(() => expect(showNotification).toHaveBeenCalledTimes(1));

    expect(qc.getQueryData(issueKeys.detail("ws-a", "issue-1"))).toMatchObject({ agent_replied_at: REPLIED_AT });
    expect(qc.getQueryData<{ issues: Issue[] }>(sidebarKey)?.issues[0]).toMatchObject({ agent_replied_at: REPLIED_AT });
    expect(showNotification).toHaveBeenCalledExactlyOnceWith(banner);

    handler(reply({ agent_replied_at: null }));
    expect(qc.getQueryData(issueKeys.detail("ws-a", "issue-1"))).toMatchObject({ agent_replied_at: null });
    expect(qc.getQueryData<{ issues: Issue[] }>(sidebarKey)?.issues[0]).toMatchObject({ agent_replied_at: null });
    expect(showNotification).toHaveBeenCalledTimes(1);

    // The event has its own handler, so the generic one leaves the lists alone.
    invalidate.mockClear();
    onAny({ type: "issue:agent_replied", payload: {} });
    expect(invalidate).not.toHaveBeenCalled();
  });
});
