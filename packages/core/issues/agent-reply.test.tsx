// @vitest-environment jsdom
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { Issue, IssueTableRowsResponse, ListIssuesResponse } from "../types";
import {
  agentReplyBody,
  isIssueShown,
  patchIssueAgentReply,
  registerAgentReplyBody,
  useIssueShown,
  useMarkIssueAgentReplySeen,
} from "./agent-reply";
import { issueKeys } from "./queries";

const REPLIED_AT = "2026-11-22T10:00:00Z";
const spec = {
  scope: { kind: "workspace" as const },
  filters: {},
  sort: { field: "position" as const, direction: "asc" as const },
};
const project = { table: { ...spec, scope: { kind: "project" as const, project_id: "p1" } } };

function task(id: string, extra: Partial<Issue> = {}): Issue {
  return { id, identifier: `MUL-${id}`, title: `Task ${id}`, status: "todo", agent_replied_at: null, ...extra } as Issue;
}

/** Three caches hold `issue-1`, and each also holds a task the marker is not about. */
function caches() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(issueKeys.detail("ws-1", "issue-1"), task("issue-1"));
  qc.setQueryData(issueKeys.detail("ws-1", "issue-2"), task("issue-2"));
  const rows: IssueTableRowsResponse = {
    query_fingerprint: "", group_key: null, parent_id: null, total: 2, branch_total: 2, next_cursor: null,
    rows: [{ issue: task("issue-1"), direct_child_count: 0 }, { issue: task("issue-2"), direct_child_count: 0 }],
  };
  qc.setQueryData(issueKeys.tableRows("ws-1", spec, { kind: "none" }, null, false, null), rows);
  qc.setQueryData(issueKeys.sidebar("ws-1", "project:p1", project, 25), {
    issues: [task("issue-1"), task("issue-2")], total: 2,
  } satisfies ListIssuesResponse);
  qc.setQueryData(issueKeys.sidebar("ws-1", "project:p2", { table: { ...spec, scope: { kind: "project", project_id: "p2" } } }, 25), {
    issues: [task("issue-3")], total: 1,
  } satisfies ListIssuesResponse);
  return qc;
}

const detail = (qc: QueryClient, id: string) => qc.getQueryData<Issue>(issueKeys.detail("ws-1", id))?.agent_replied_at ?? null;
const rowsOf = (qc: QueryClient) =>
  qc.getQueryData<IssueTableRowsResponse>(issueKeys.tableRows("ws-1", spec, { kind: "none" }, null, false, null))!.rows;
const sidebarOf = (qc: QueryClient, project_id: string) =>
  qc.getQueryData<ListIssuesResponse>(issueKeys.sidebar("ws-1", `project:${project_id}`, project_id === "p1" ? project : { table: { ...spec, scope: { kind: "project", project_id } } }, 25))!.issues;

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("patchIssueAgentReply", () => {
  it("puts the marker on the task in every cache that holds it, and on nothing else", () => {
    const qc = caches();

    patchIssueAgentReply(qc, "ws-1", "issue-1", REPLIED_AT);

    expect(detail(qc, "issue-1")).toBe(REPLIED_AT);
    expect(rowsOf(qc).map((row) => row.issue.agent_replied_at)).toEqual([REPLIED_AT, null]);
    expect(sidebarOf(qc, "p1").map((issue) => issue.agent_replied_at)).toEqual([REPLIED_AT, null]);
    expect(detail(qc, "issue-2")).toBeNull();
    expect(sidebarOf(qc, "p2").map((issue) => issue.agent_replied_at)).toEqual([null]);
  });

  it("clears the marker the same way, as marking the reply seen does", () => {
    const qc = caches();
    patchIssueAgentReply(qc, "ws-1", "issue-1", REPLIED_AT);

    patchIssueAgentReply(qc, "ws-1", "issue-1", null);

    expect(detail(qc, "issue-1")).toBeNull();
    expect(rowsOf(qc)[0]?.issue.agent_replied_at).toBeNull();
    expect(sidebarOf(qc, "p1")[0]?.agent_replied_at).toBeNull();
  });
});

describe("useIssueShown", () => {
  it("keeps the task on screen while its page is mounted, and no longer", () => {
    const { unmount } = renderHook(() => useIssueShown("issue-1"));
    expect(isIssueShown("issue-1")).toBe(true);
    expect(isIssueShown("issue-2")).toBe(false);

    unmount();
    expect(isIssueShown("issue-1")).toBe(false);
  });

  it("shows nothing for a page that has no task yet", () => {
    const { rerender } = renderHook<void, { issueId: string | null }>(
      ({ issueId }) => useIssueShown(issueId),
      { initialProps: { issueId: null } },
    );
    expect(isIssueShown("issue-1")).toBe(false);

    rerender({ issueId: "issue-1" });
    expect(isIssueShown("issue-1")).toBe(true);
  });
});

describe("useMarkIssueAgentReplySeen", () => {
  it("puts the marker the server answers into every cache that holds the task", async () => {
    const markIssueAgentReplySeen = vi.fn().mockResolvedValue({ agent_replied_at: null });
    setApiInstance({ markIssueAgentReplySeen } as unknown as ApiClient);
    const qc = caches();
    patchIssueAgentReply(qc, "ws-1", "issue-1", REPLIED_AT);
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(() => useMarkIssueAgentReplySeen("ws-1", "issue-1"), { wrapper });

    act(() => result.current.mutate());

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(markIssueAgentReplySeen).toHaveBeenCalledWith("issue-1");
    expect(detail(qc, "issue-1")).toBeNull();
    expect(rowsOf(qc)[0]?.issue.agent_replied_at).toBeNull();
    expect(sidebarOf(qc, "p1")[0]?.agent_replied_at).toBeNull();
  });
});

describe("agent reply banner text", () => {
  afterEach(() => registerAgentReplyBody(null));

  it("holds what views registered, and nothing once views unmounts", () => {
    expect(agentReplyBody()).toBe("");
    registerAgentReplyBody("New reply");
    expect(agentReplyBody()).toBe("New reply");
    registerAgentReplyBody(null);
    expect(agentReplyBody()).toBe("");
  });
});
