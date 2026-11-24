// @vitest-environment jsdom
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import { EMPTY_ISSUE_PROJECT_SCOPE } from "../api/schemas";
import { taskTreeKeys } from "../tasktrees/work";
import type { IssueProjectScope, IssueProjectTree, ProjectScopeItem } from "../types";
import {
  ISSUE_TREE_POLL_INTERVAL_MS,
  issueProjectScopeKeys,
  issueProjectScopeOptions,
  useIssueProjectScope,
  useRebuildIssueProjectTree,
  useReplaceIssueProjectScope,
} from "./project-scope";

const tree = (state: IssueProjectTree["state"]): IssueProjectTree => ({
  id: "tree-1",
  state,
  path: state === "ready" ? "/srv/trees/MUL-1/repo" : "",
  task_branch: "task/MUL-1",
  error: state === "failed" ? "no such branch" : "",
  created_at: "2026-11-22T10:00:00Z",
  updated_at: "2026-11-22T10:05:00Z",
});

const item = (branch: string, state: IssueProjectTree["state"] = "pending"): ProjectScopeItem => ({
  project_id: "project-1",
  base_branch: branch,
  position: 0,
  tree: tree(state),
});
const own = { items: [item("main", "ready")] };
const built = { items: [item("main")] };
const pairs = [{ project_id: "project-1", base_branch: "develop" }];
const ownKey = issueProjectScopeKeys.own("ws-1", "issue-1");

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function setup(overrides: Partial<ApiClient>) {
  setApiInstance({ ...overrides } as unknown as ApiClient);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return { queryClient, wrapper };
}

describe("useIssueProjectScope", () => {
  it("reads a task's pairs under a workspace-scoped key and keeps the loaded reference", async () => {
    const getIssueProjectScope = vi.fn().mockResolvedValue(own);
    const { queryClient, wrapper } = setup({ getIssueProjectScope });
    const { result, rerender } = renderHook(() => useIssueProjectScope("ws-1", "issue-1"), {
      wrapper,
    });

    await waitFor(() => expect(result.current.data).toEqual(own));
    expect(getIssueProjectScope).toHaveBeenCalledWith("issue-1");
    const data = result.current.data;
    rerender();
    expect(result.current.data).toBe(data);
    expect(queryClient.getQueryData(ownKey)).toBe(data);
    expect(issueProjectScopeOptions("ws-1", "issue-1").queryKey).toEqual(ownKey);
    expect(ownKey).toContain("ws-1");
  });

  it("does not fetch without a task and reads as the shared empty scope", () => {
    const getIssueProjectScope = vi.fn();
    const { wrapper } = setup({ getIssueProjectScope });
    const { result, rerender } = renderHook(() => useIssueProjectScope("ws-1", undefined), {
      wrapper,
    });

    rerender();
    expect(result.current.data).toBe(EMPTY_ISSUE_PROJECT_SCOPE);
    expect(result.current.isLoading).toBe(false);
    expect(getIssueProjectScope).not.toHaveBeenCalled();
  });

  it("reads a scope with a waiting tree again every few seconds, and stops once no tree waits", async () => {
    vi.useFakeTimers();
    // A finished read reaches the page one tick after it answered, so every
    // step moves the clock by at least a tick.
    const advance = (ms: number) =>
      act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
      });
    // Waiting twice, built on the third read.
    const getIssueProjectScope = vi
      .fn<() => Promise<IssueProjectScope>>()
      .mockResolvedValueOnce(built)
      .mockResolvedValueOnce(built)
      .mockResolvedValue(own);
    const { wrapper } = setup({ getIssueProjectScope });
    const { result } = renderHook(() => useIssueProjectScope("ws-1", "issue-1"), { wrapper });
    await advance(1);
    expect(result.current.data).toEqual(built);
    expect(getIssueProjectScope).toHaveBeenCalledTimes(1);

    // Not yet: the next read is a few seconds away.
    await advance(ISSUE_TREE_POLL_INTERVAL_MS - 2);
    expect(getIssueProjectScope).toHaveBeenCalledTimes(1);

    await advance(1);
    expect(getIssueProjectScope).toHaveBeenCalledTimes(2);
    await advance(ISSUE_TREE_POLL_INTERVAL_MS);
    expect(getIssueProjectScope).toHaveBeenCalledTimes(3);
    await advance(1);
    expect(result.current.data).toEqual(own);

    // No tree waits anymore, so the scope is not read again.
    await advance(ISSUE_TREE_POLL_INTERVAL_MS * 10);
    expect(getIssueProjectScope).toHaveBeenCalledTimes(3);
  });

  it("reads a scope whose trees are all built once only", async () => {
    vi.useFakeTimers();
    const getIssueProjectScope = vi.fn().mockResolvedValue(own);
    const { wrapper } = setup({ getIssueProjectScope });
    renderHook(() => useIssueProjectScope("ws-1", "issue-1"), { wrapper });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ISSUE_TREE_POLL_INTERVAL_MS * 5);
    });
    expect(getIssueProjectScope).toHaveBeenCalledTimes(1);
  });
});

describe("useReplaceIssueProjectScope", () => {
  it("replaces the task's pairs under the base_branch wire name and caches the response", async () => {
    const replaceIssueProjectScope = vi.fn().mockResolvedValue(own);
    const { queryClient, wrapper } = setup({ replaceIssueProjectScope });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useReplaceIssueProjectScope("ws-1", "issue-1"), {
      wrapper,
    });

    await act(async () => {
      await result.current.mutateAsync({ items: pairs });
    });

    expect(replaceIssueProjectScope).toHaveBeenCalledWith("issue-1", { items: pairs });
    expect(queryClient.getQueryData(ownKey)).toEqual(own);
    // Trees the saved scope asks for are the app's to build: its list is read again.
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: taskTreeKeys.work("ws-1") });
  });

  it("writes the pairs of a task the request names, and caches them under that task", async () => {
    // A task created a moment ago: the hook was mounted before its id existed.
    const replaceIssueProjectScope = vi.fn().mockResolvedValue(own);
    const { queryClient, wrapper } = setup({ replaceIssueProjectScope });
    const { result } = renderHook(() => useReplaceIssueProjectScope("ws-1", ""), {
      wrapper,
    });

    await act(async () => {
      await result.current.mutateAsync({ issue_id: "issue-9", items: pairs });
    });

    expect(replaceIssueProjectScope).toHaveBeenCalledWith("issue-9", { items: pairs });
    expect(queryClient.getQueryData(issueProjectScopeKeys.own("ws-1", "issue-9"))).toEqual(own);
  });

  it("refetches instead of caching when the response to the write is malformed", async () => {
    const replaceIssueProjectScope = vi.fn().mockResolvedValue(null);
    const { queryClient, wrapper } = setup({ replaceIssueProjectScope });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useReplaceIssueProjectScope("ws-1", "issue-1"), {
      wrapper,
    });

    await act(async () => {
      await result.current.mutateAsync({ items: pairs });
    });

    expect(queryClient.getQueryData(ownKey)).toBeUndefined();
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ownKey });
  });

  it("rejects, as a refused removal does, without touching the cache", async () => {
    const replaceIssueProjectScope = vi.fn().mockRejectedValue(
      new Error("a pair whose tree is ready cannot be removed"),
    );
    const { queryClient, wrapper } = setup({ replaceIssueProjectScope });
    queryClient.setQueryData(ownKey, own);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useReplaceIssueProjectScope("ws-1", "issue-1"), {
      wrapper,
    });

    await expect(result.current.mutateAsync({ items: [] })).rejects.toThrow(/tree is ready/);

    expect(queryClient.getQueryData(ownKey)).toEqual(own);
    expect(invalidate).not.toHaveBeenCalled();
  });
});

describe("tree writes", () => {
  it.each([["useRebuildIssueProjectTree", useRebuildIssueProjectTree, "rebuildIssueProjectTree"]] as const)("reads the scope again after %s succeeds", async (_name, useWrite, method) => {
    const write = vi.fn().mockResolvedValue(undefined);
    const after = { items: [item("main", "deleted")] };
    const getIssueProjectScope = vi.fn().mockResolvedValue(built);
    const { queryClient, wrapper } = setup({ [method]: write, getIssueProjectScope });
    const setQueryData = vi.spyOn(queryClient, "setQueryData");
    const { result } = renderHook(
      () => ({ scope: useIssueProjectScope("ws-1", "issue-1"), write: useWrite("ws-1", "issue-1") }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.scope.data).toEqual(built));
    setQueryData.mockClear();

    // What the server answers once it has dealt with the tree.
    getIssueProjectScope.mockResolvedValue(after);
    await act(async () => {
      await result.current.write.mutateAsync("tree-1");
    });

    expect(write).toHaveBeenCalledWith("issue-1", "tree-1");
    // The answer is one tree; the scope shown is the server's whole, read again.
    expect(setQueryData).not.toHaveBeenCalled();
    expect(getIssueProjectScope).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(result.current.scope.data).toEqual(after));
  });

});
