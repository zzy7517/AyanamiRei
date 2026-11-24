import { afterEach, describe, expect, it, vi } from "vitest";
import { configStore } from "../config";
import { ApiClient } from "./client";
import { setApiRequestObserver } from "./request-observer";

afterEach(() => {
  configStore.getState().setAgentConversationStartersSupported(false);
  vi.unstubAllGlobals();
});

describe("ApiClient complete issue deliverables", () => {
  it("loads a posting comment's thread and validates the comment response", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ invalid: true }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    expect(await client.listComments("issue", { thread: "historical/comment" })).toEqual([]);
    expect(fetchMock.mock.calls[0]?.[0]).toBe("https://api.example.test/api/issues/issue/comments?thread=historical%2Fcomment");
  });

});

describe("ApiClient issue environments", () => {
  const env = {
    id: "env-1", issue_id: "issue-1", task_id: "task-1", source: "config", name: "web",
    state: "running", started_at: "2026-11-10T00:00:00Z", last_seen_at: "2026-11-10T00:00:00Z",
  };

  function stub(body: unknown) {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(body), {
      status: 200, headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  it("reads an older server's environments as read-only", async () => {
    stub({ environments: [env] });
    const result = await new ApiClient("https://api.example.test").listIssueEnvironments("issue-1");
    expect(result.configs).toEqual([]);
    expect(result.pending_starts).toEqual([]);
    expect(result.failed_starts).toEqual([]);
    expect(result.environments[0]).toMatchObject({
      repo: "", excerpt: "", controllable: false, can_control: false, pending_action: null, last_action: null, origin: null,
    });
  });

  it("drops an action record it cannot read without losing the environment", async () => {
    stub({
      environments: [{ ...env, controllable: true, can_control: true, pending_action: { id: "cmd-1", action: "teleport", created_at: "x" } }],
      configs: [{ runtime_id: "rt-1", task_id: "task-1", reported_at: "x", environments: [{ repo: "app", name: "web" }] }, { bad: true }],
      pending_starts: [{ id: "cmd-2", action: "start", status: "queued", created_at: "x" }],
      failed_starts: [{ id: "cmd-4", action: "start", status: "expired", task_id: "task-1", created_at: "x" }, { id: "cmd-5" }],
    });
    const result = await new ApiClient("https://api.example.test").listIssueEnvironments("issue-1");
    expect(result.environments[0]).toMatchObject({ controllable: true, can_control: true, pending_action: null });
    expect(result.configs).toEqual([{ runtime_id: "rt-1", task_id: "task-1", reported_at: "x", can_control: false, environments: [{ repo: "app", name: "web", label: "", start: "" }] }]);
    expect(result.pending_starts[0]).toMatchObject({ id: "cmd-2", status: "failed" });
    expect(result.failed_starts).toEqual([expect.objectContaining({ id: "cmd-4", status: "expired", task_id: "task-1" })]);
  });

  it("posts a start with the run and environment it names", async () => {
    const fetchMock = stub({ id: "cmd-3", action: "start", status: "pending", created_at: "x" });
    const result = await new ApiClient("https://api.example.test").startIssueEnvironment("issue-1", { task_id: "task-1", repo: "app", name: "web" });
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("https://api.example.test/api/issues/issue-1/environments/start");
    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({ task_id: "task-1", repo: "app", name: "web" });
    expect(result).toMatchObject({ id: "cmd-3", status: "pending", output: null });
  });
});

describe("ApiClient request observer", () => {
  afterEach(() => setApiRequestObserver(null));

  function observe() {
    const events: unknown[] = [];
    setApiRequestObserver({
      onStart: (request: { requestId: string; method: string; path: string; body: string | null }) =>
        events.push({ phase: "start", ...request }),
      onEnd: (result: { requestId: string; status: number | null; error: string | null }) =>
        events.push({ phase: "end", ...result }),
    });
    return events;
  }

  it("reports each request's start and its status, sharing the X-Request-ID", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("[]", {
      status: 200, headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);
    const events = observe();
    await new ApiClient("https://api.example.test").listMembers("ws-1");

    const sentId = (fetchMock.mock.calls[0]?.[1]?.headers as Record<string, string>)["X-Request-ID"];
    expect(events).toEqual([
      { phase: "start", requestId: sentId, method: "GET", path: "/api/workspaces/ws-1/members", body: null },
      { phase: "end", requestId: sentId, status: 200, error: null },
    ]);
  });

  it("reports the server's error message for a failed request", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: "version conflict" }), {
      status: 500, headers: { "Content-Type": "application/json" },
    })));
    const events = observe();
    await expect(new ApiClient("https://api.example.test").listMembers("ws-1")).rejects.toThrow();
    expect(events[1]).toMatchObject({ phase: "end", status: 500, error: "version conflict" });
  });

  it("reports a request that never got a response", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
    const events = observe();
    await expect(new ApiClient("https://api.example.test").listMembers("ws-1")).rejects.toThrow();
    expect(events[1]).toMatchObject({ phase: "end", status: null, error: "TypeError: Failed to fetch" });
  });
});
