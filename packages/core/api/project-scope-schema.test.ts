import { describe, expect, it } from "vitest";
import {
  IssueProjectScopeSchema,
  IssueProjectTreeResponseSchema,
  IssueProjectTreeSchema,
} from "./schemas";

const tree = {
  id: "tree-1",
  state: "ready",
  path: "/srv/tasks/demo/repo",
  task_branch: "task/MUL-1",
  error: "",
  created_at: "2026-11-22T10:00:00Z",
  updated_at: "2026-11-22T10:05:00Z",
};

describe("IssueProjectTreeSchema", () => {
  it("reads an unknown state as not built yet", () => {
    expect(IssueProjectTreeSchema.parse({ ...tree, state: "creating" }).state).toBe("pending");
    expect(IssueProjectTreeSchema.parse({ ...tree, state: 7 }).state).toBe("pending");
  });

  it("defaults what a tree without a report leaves out", () => {
    expect(IssueProjectTreeSchema.parse({ id: "tree-1" })).toEqual({
      id: "tree-1",
      state: "pending",
      path: "",
      task_branch: "",
      error: "",
      created_at: "",
      updated_at: "",
    });
  });
});

describe("IssueProjectScopeSchema", () => {
  it("reads each pair with its tree", () => {
    const parsed = IssueProjectScopeSchema.parse({
      items: [{ project_id: "p", base_branch: "main", position: 0, tree }],
    });
    expect(parsed.items).toEqual([{ project_id: "p", base_branch: "main", position: 0, tree }]);
  });

  it("reads a pair with a missing or malformed tree as one without a tree", () => {
    const parsed = IssueProjectScopeSchema.parse({
      items: [
        { project_id: "p", base_branch: "main" },
        { project_id: "q", base_branch: "dev", tree: "none" },
        { project_id: "r", base_branch: "dev", tree: null },
      ],
    });
    expect(parsed.items.map((item) => item.tree)).toEqual([null, null, null]);
  });

  it("drops malformed pairs and keeps the valid ones", () => {
    const parsed = IssueProjectScopeSchema.parse({ items: [{ base_branch: "main" }, "nope", { project_id: "p", base_branch: "main" }] });
    expect(parsed.items).toEqual([{ project_id: "p", base_branch: "main", position: 0, tree: null }]);
  });
});

describe("IssueProjectTreeResponseSchema", () => {
  it("reads the tree a rebuild answers", () => {
    expect(IssueProjectTreeResponseSchema.parse({ tree })).toEqual({ tree });
  });

  it("reads an answer without a usable tree as none, for the scope to be read again", () => {
    expect(IssueProjectTreeResponseSchema.parse({ tree: "built" }).tree).toBeNull();
    expect(IssueProjectTreeResponseSchema.parse({}).tree).toBeNull();
  });
});
