// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

const seenPath = "https://api.example.test/api/issues/issue-1/agent-reply/seen";

function respond(body: unknown) {
  const fetchMock = vi.fn().mockImplementation(async () =>
    new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => vi.unstubAllGlobals());

describe("marking an agent's reply seen", () => {
  it("posts to the task and reads the marker the server left", async () => {
    const fetchMock = respond({ agent_replied_at: null });
    const client = new ApiClient("https://api.example.test");

    expect(await client.markIssueAgentReplySeen("issue-1")).toEqual({ agent_replied_at: null });
    expect(fetchMock.mock.calls.map(([url, init]) => [url, init?.method])).toEqual([[seenPath, "POST"]]);
  });

  it("reads a malformed answer as cleared, which is what was asked for", async () => {
    respond({ agent_replied_at: 42 });
    const client = new ApiClient("https://api.example.test");
    expect(await client.markIssueAgentReplySeen("issue-1")).toEqual({ agent_replied_at: null });

    respond("not an answer");
    expect(await client.markIssueAgentReplySeen("issue-1")).toEqual({ agent_replied_at: null });
  });
});
