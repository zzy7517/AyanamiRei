import { describe, expect, it } from "vitest";
import {
  actorKindForViewVariant,
  assigneeTypesForActorKind,
  UnsupportedIssueScopeError,
  issueScopeKey,
} from "./scope";
import { buildIssueSurfaceQueryPlan } from "./query-plan";

describe("issue surface scope", () => {
  it("builds stable surface keys", () => {
    expect(issueScopeKey({ type: "workspace" })).toBe("workspace:all");
    expect(issueScopeKey({ type: "workspace", actorKind: "agents" })).toBe(
      "workspace:agents",
    );
    expect(issueScopeKey({ type: "project", projectId: "p1" })).toBe(
      "project:p1",
    );
    expect(
      issueScopeKey({ type: "project", projectId: "p1", actorKind: "agents" }),
    ).toBe("project:p1:agents");
    expect(
      issueScopeKey({
        type: "actor",
        actorType: "agent",
        actorId: "a1",
        relation: "created",
      }),
    ).toBe("actor:agent:a1:created");
    expect(issueScopeKey({ type: "team", teamId: "t1" })).toBe("team:t1");
  });

  it("compiles each scope to its key, gantt filter, and create defaults", () => {
    expect(buildIssueSurfaceQueryPlan({ type: "workspace" })).toEqual({
      scopeKey: "workspace:all",
      queryFilter: {},
      createDefaults: {},
    });
    expect(
      buildIssueSurfaceQueryPlan({ type: "workspace", actorKind: "agents" }),
    ).toEqual({
      scopeKey: "workspace:agents",
      queryFilter: { assignee_types: ["agent"] },
      createDefaults: {},
    });
    expect(
      buildIssueSurfaceQueryPlan({ type: "project", projectId: "p1", actorKind: "agents" }),
    ).toEqual({
      scopeKey: "project:p1:agents",
      queryFilter: { project_id: "p1", assignee_types: ["agent"] },
      createDefaults: { project_id: "p1" },
    });
    expect(
      buildIssueSurfaceQueryPlan({
        type: "actor",
        actorType: "agent",
        actorId: "a1",
        relation: "assigned",
      }),
    ).toEqual({
      scopeKey: "actor:agent:a1:assigned",
      queryFilter: { assignee_id: "a1" },
      createDefaults: { assignee_type: "agent", assignee_id: "a1" },
    });
    expect(
      buildIssueSurfaceQueryPlan({
        type: "actor",
        actorType: "member",
        actorId: "u1",
        relation: "created",
      }),
    ).toEqual({
      scopeKey: "actor:member:u1:created",
      queryFilter: { creator_id: "u1" },
      createDefaults: {},
    });
  });

  it("throws for team until the issue API has a team filter", () => {
    const scope = { type: "team" as const, teamId: "t1" };
    expect(() => buildIssueSurfaceQueryPlan(scope)).toThrow(
      UnsupportedIssueScopeError,
    );
  });
});

describe("assigneeTypesForActorKind", () => {
  it("maps each tab to its API values", () => {
    expect(assigneeTypesForActorKind("agents")).toEqual(["agent"]);
    expect(assigneeTypesForActorKind("all")).toBeUndefined();
    expect(assigneeTypesForActorKind(undefined)).toBeUndefined();
  });
});

describe("actorKindForViewVariant", () => {
  it("resolves saved-view variants to a tab, defaulting to all", () => {
    expect(actorKindForViewVariant("agents")).toBe("agents");
    expect(actorKindForViewVariant(null)).toBe("all");
    expect(actorKindForViewVariant(undefined)).toBe("all");
    expect(actorKindForViewVariant("involved")).toBe("all");
  });
});
