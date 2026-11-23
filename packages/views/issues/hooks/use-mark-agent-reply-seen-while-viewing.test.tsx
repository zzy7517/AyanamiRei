import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createQueryClient } from "@multica/core/query-client";
import { api } from "@multica/core/api";
import { patchIssueAgentReply } from "@multica/core/issues/agent-reply";
import { issueKeys } from "@multica/core/issues/queries";
import type { Issue } from "@multica/core/types";
import { useMarkAgentReplySeenWhileViewing } from "./use-mark-agent-reply-seen-while-viewing";

const foreground = vi.hoisted(() => ({ value: true }));

vi.mock("@multica/core/api", () => ({ api: { getIssue: vi.fn(), markIssueAgentReplySeen: vi.fn() } }));
vi.mock("../../common/use-app-foreground", () => ({ useAppForeground: () => foreground.value }));

const FIRST = "2026-11-22T10:00:00Z";
const SECOND = "2026-11-22T10:05:00Z";
const detailKey = issueKeys.detail("ws-1", "issue-1");
const task = (agent_replied_at: string | null): Issue =>
  ({ id: "issue-1", identifier: "MUL-1", title: "Fix login", status: "todo", agent_replied_at }) as Issue;

let client: ReturnType<typeof createQueryClient>;
const wrapper = ({ children }: { children: ReactNode }) => (
  <QueryClientProvider client={client}>{children}</QueryClientProvider>
);

beforeEach(() => {
  client = createQueryClient();
  foreground.value = true;
  vi.mocked(api.markIssueAgentReplySeen).mockResolvedValue({ agent_replied_at: null });
});

afterEach(() => {
  client.clear();
  vi.resetAllMocks();
});

describe("useMarkAgentReplySeenWhileViewing", () => {
  it("marks the reply seen when the task opens with one waiting", async () => {
    client.setQueryData(detailKey, task(FIRST));
    vi.mocked(api.getIssue).mockResolvedValue(task(FIRST));

    renderHook(() => useMarkAgentReplySeenWhileViewing("ws-1", "issue-1"), { wrapper });

    await waitFor(() => expect(api.markIssueAgentReplySeen).toHaveBeenCalledExactlyOnceWith("issue-1"));
    // The caches take the marker as the server left it.
    await waitFor(() => expect(client.getQueryData<Issue>(detailKey)?.agent_replied_at).toBeNull());
  });

  it("marks each reply seen as it arrives while the task stays open, and nothing for its clearing", async () => {
    client.setQueryData(detailKey, task(null));
    renderHook(() => useMarkAgentReplySeenWhileViewing("ws-1", "issue-1"), { wrapper });
    const seen = vi.mocked(api.markIssueAgentReplySeen);

    // What the `issue:agent_replied` event does to the caches.
    act(() => patchIssueAgentReply(client, "ws-1", "issue-1", FIRST));
    await waitFor(() => expect(seen).toHaveBeenCalledTimes(1));

    act(() => patchIssueAgentReply(client, "ws-1", "issue-1", SECOND));
    await waitFor(() => expect(seen).toHaveBeenCalledTimes(2));

    act(() => patchIssueAgentReply(client, "ws-1", "issue-1", null));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(seen).toHaveBeenCalledTimes(2);
  });

  it("writes a waiting reply once, however often the task renders", async () => {
    client.setQueryData(detailKey, task(FIRST));
    vi.mocked(api.getIssue).mockResolvedValue(task(FIRST));
    const view = renderHook(() => useMarkAgentReplySeenWhileViewing("ws-1", "issue-1"), { wrapper });
    await waitFor(() => expect(api.markIssueAgentReplySeen).toHaveBeenCalledTimes(1));
    client.setQueryData(detailKey, task(FIRST));
    view.rerender();

    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(api.markIssueAgentReplySeen).toHaveBeenCalledTimes(1);
  });

  it("marks nothing while the app is in the background, and the waiting reply once it returns", async () => {
    client.setQueryData(detailKey, task(FIRST));
    foreground.value = false;
    const view = renderHook(() => useMarkAgentReplySeenWhileViewing("ws-1", "issue-1"), { wrapper });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(api.markIssueAgentReplySeen).not.toHaveBeenCalled();

    foreground.value = true;
    view.rerender();
    await waitFor(() => expect(api.markIssueAgentReplySeen).toHaveBeenCalledTimes(1));
  });

  it("marks nothing for a task whose replies were all seen", async () => {
    client.setQueryData(detailKey, task(null));
    vi.mocked(api.getIssue).mockResolvedValue(task(null));

    renderHook(() => useMarkAgentReplySeenWhileViewing("ws-1", "issue-1"), { wrapper });

    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(api.markIssueAgentReplySeen).not.toHaveBeenCalled();
  });

  it("asks nothing about a page that shows no task", async () => {
    renderHook(() => useMarkAgentReplySeenWhileViewing("ws-1", ""), { wrapper });

    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(api.getIssue).not.toHaveBeenCalled();
    expect(api.markIssueAgentReplySeen).not.toHaveBeenCalled();
  });
});
