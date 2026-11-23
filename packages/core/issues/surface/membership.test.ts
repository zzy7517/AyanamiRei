// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Issue } from "../../types";
import {
  issueChangedDims,
  issueMatchesListFilter,
  listFilterDependsOn,
} from "./membership";

function makeIssue(overrides: Partial<Issue> = {}): Issue {
  return {
    id: "issue-1",
    workspace_id: "ws-1",
    number: 1,
    identifier: "MUL-1",
    title: "Issue 1",
    description: null,
    status: "todo",
    priority: "none",
    assignee_type: "agent",
    assignee_id: "agent-me",
    creator_type: "member",
    creator_id: "me",
    parent_issue_id: null,
    project_id: "p1",
    position: 1,
    stage: null,
    start_date: null,
    due_date: null,
    labels: [],
    metadata: {},
    properties: {},
    created_at: "2025-01-01T00:00:00Z",
    updated_at: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("issueMatchesListFilter", () => {
  it("judges assignee_id filters definitively", () => {
    const issue = makeIssue();
    expect(issueMatchesListFilter(issue, { assignee_id: "agent-me" })).toBe(true);
    expect(issueMatchesListFilter(issue, { assignee_id: "agent-bob" })).toBe(false);
  });

  it("degrades to unknown when the entity is missing the filtered field", () => {
    expect(issueMatchesListFilter({ title: "partial" }, { assignee_id: "agent-me" })).toBe("unknown");
  });

  it("judges assignee_types filters, treating unassigned as a definitive miss", () => {
    expect(issueMatchesListFilter(makeIssue(), { assignee_types: ["agent"] })).toBe(true);
    expect(issueMatchesListFilter(makeIssue({ assignee_type: null, assignee_id: null }), {
      assignee_types: ["agent"],
    })).toBe(false);
  });

  it("judges creator filters", () => {
    expect(issueMatchesListFilter(makeIssue(), { creator_id: "me" })).toBe(true);
    expect(issueMatchesListFilter(makeIssue(), { creator_id: "bob" })).toBe(false);
  });

  it("judges project filters", () => {
    expect(issueMatchesListFilter(makeIssue(), { project_id: "p1" })).toBe(true);
    expect(issueMatchesListFilter(makeIssue({ project_id: "p2" }), { project_id: "p1" })).toBe(false);
    expect(issueMatchesListFilter(makeIssue({ project_id: null }), { project_id: "p1" })).toBe(false);
  });

  it("ANDs across fields — a definitive miss beats an unknown", () => {
    expect(
      issueMatchesListFilter({ project_id: "p2" }, { project_id: "p1", assignee_id: "agent-me" }),
    ).toBe(false);
  });
});

describe("issueChangedDims", () => {
  it("treats written membership fields as changed when no base is known", () => {
    expect(issueChangedDims({ assignee_id: "agent-bob", assignee_type: "agent" })).toEqual({
      assignee: true,
      project: false,
      status: false,
    });
    expect(issueChangedDims({ project_id: null })).toEqual({
      assignee: false,
      project: true,
      status: false,
    });
  });

  it("sharpens against a base entity — writing the same value changes nothing", () => {
    const base = makeIssue();
    expect(issueChangedDims({ assignee_id: "agent-me", assignee_type: "agent" }, base)).toEqual({
      assignee: false,
      project: false,
      status: false,
    });
    expect(issueChangedDims({ status: "todo" }, base).status).toBe(false);
    expect(issueChangedDims({ status: "done" }, base).status).toBe(true);
    expect(issueChangedDims({ project_id: "p2" }, base).project).toBe(true);
  });

  it("ignores non-membership fields", () => {
    expect(issueChangedDims({ title: "x", position: 9 })).toEqual({
      assignee: false,
      project: false,
      status: false,
    });
  });
});

describe("listFilterDependsOn", () => {
  const none = { assignee: false, project: false, status: false };

  it("assignee-keyed filters react to assignee changes", () => {
    expect(listFilterDependsOn({ assignee_id: "agent-me" }, { ...none, assignee: true })).toBe(true);
    expect(listFilterDependsOn({ assignee_types: ["agent"] }, { ...none, assignee: true })).toBe(true);
    expect(listFilterDependsOn({ assignee_id: "agent-me" }, { ...none, project: true })).toBe(false);
  });

  it("project filters react to project changes", () => {
    expect(listFilterDependsOn({ project_id: "p1" }, { ...none, project: true })).toBe(true);
    expect(listFilterDependsOn({ project_id: "p1" }, { ...none, assignee: true })).toBe(false);
  });

  it("creator filters never react — creator is immutable", () => {
    expect(
      listFilterDependsOn(
        { creator_id: "me" },
        { assignee: true, project: true, status: true },
      ),
    ).toBe(false);
  });

  it("the unfiltered workspace list never reacts", () => {
    expect(listFilterDependsOn({}, { assignee: true, project: true, status: true })).toBe(false);
  });
});
