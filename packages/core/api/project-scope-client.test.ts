// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { EMPTY_ISSUE_PROJECT_SCOPE } from "./schemas";

const issueItem = {
  project_id: "project-1", base_branch: "main", position: 0,
  tree: { id: "t1", state: "pending", path: "", task_branch: "", error: "", created_at: "", updated_at: "" },
};

function respond(body: unknown) {
  const fetchMock = vi.fn().mockImplementation(async () =>
    new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => vi.unstubAllGlobals());

describe("project + branch scope client methods", () => {
  it("reads a task's scope and replaces it with the whole request body", async () => {
    const fetchMock = respond({ items: [issueItem] });
    const client = new ApiClient("https://api.example.test");

    const scope = await client.getIssueProjectScope("issue-1");
    const replaced = await client.replaceIssueProjectScope("issue-1", {
      items: [{ project_id: "project-1", base_branch: "main" }],
    });

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "https://api.example.test/api/issues/issue-1/project-scope",
      "https://api.example.test/api/issues/issue-1/project-scope",
    ]);
    expect(scope).toEqual({ items: [issueItem] });
    expect(replaced).toEqual({ items: [issueItem] });
    const [, put] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(put.method).toBe("PUT");
    expect(JSON.parse(String(put.body))).toEqual({
      items: [{ project_id: "project-1", base_branch: "main" }],
    });
  });

  it("reads a malformed read as an empty scope and a malformed replace as unknown", async () => {
    respond({ items: "invalid" });
    const client = new ApiClient("https://api.example.test");

    expect(await client.getIssueProjectScope("issue-1")).toEqual(EMPTY_ISSUE_PROJECT_SCOPE);
    expect(await client.replaceIssueProjectScope("issue-1", { items: [] })).toBeNull();
  });

  it("drops a malformed pair and keeps the rest, with defaults for what is missing", async () => {
    respond({
      items: [
        { project_id: "project-2", base_branch: "release" },
        { base_branch: "only-a-branch" },
        { project_id: "project-1", base_branch: "main" },
      ],
    });
    const client = new ApiClient("https://api.example.test");

    const scope = await client.getIssueProjectScope("issue-1");

    expect(
      scope.items.map((pair) => [pair.project_id, pair.base_branch, pair.position, pair.tree]),
    ).toEqual([
      ["project-2", "release", 0, null],
      ["project-1", "main", 0, null],
    ]);
  });
});

describe("task tree client methods", () => {
  const treePath = "https://api.example.test/api/issues/issue-1/project-scope/t1";

  it("deletes a tree and reads the tree a rebuild answers", async () => {
    const fetchMock = respond({ tree: issueItem.tree });
    fetchMock.mockImplementationOnce(async () => new Response(null, { status: 204 }));
    const client = new ApiClient("https://api.example.test");

    await client.deleteIssueProjectTree("issue-1", "t1");
    const rebuilt = await client.rebuildIssueProjectTree("issue-1", "t1");

    expect(fetchMock.mock.calls.map(([url, init]) => [url, init?.method])).toEqual([
      [`${treePath}/tree`, "DELETE"],
      [`${treePath}/rebuild`, "POST"],
    ]);
    expect(rebuilt).toEqual(issueItem.tree);
  });

  it("reads a malformed rebuild answer as unknown", async () => {
    respond({ tree: "waiting" });
    const client = new ApiClient("https://api.example.test");

    expect(await client.rebuildIssueProjectTree("issue-1", "t1")).toBeNull();
  });

  it("rejects a refusal with its code, as a tree already waiting to be rebuilt", async () => {
    const refusal = {
      error: "the tree is already waiting to be built",
      code: "tree_pending",
    };
    const fetchMock = vi.fn().mockImplementation(async () =>
      new Response(JSON.stringify(refusal), {
        status: 409,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    await expect(client.rebuildIssueProjectTree("issue-1", "t1")).rejects.toMatchObject({
      name: "ApiError",
      status: 409,
      message: "the tree is already waiting to be built",
      body: { code: "tree_pending", error: "the tree is already waiting to be built" },
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
