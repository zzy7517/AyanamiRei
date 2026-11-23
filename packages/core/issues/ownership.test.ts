import { describe, expect, it } from "vitest";
import { isOwnIssue } from "./ownership";

const agents = [
  { id: "agent-mine", owner_id: "me" },
  { id: "agent-theirs", owner_id: "someone" },
];

describe("isOwnIssue", () => {
  it("is the viewer's when assigned to an agent they own", () => {
    expect(isOwnIssue({ assignee_type: "agent", assignee_id: "agent-mine" }, "me", agents)).toBe(true);
  });

  it("is someone else's when assigned to another owner's agent or to nobody", () => {
    expect(isOwnIssue({ assignee_type: "agent", assignee_id: "agent-theirs" }, "me", agents)).toBe(false);
    expect(isOwnIssue({ assignee_type: null, assignee_id: null }, "me", agents)).toBe(false);
  });

  it("is never the viewer's without a viewer", () => {
    expect(isOwnIssue({ assignee_type: "agent", assignee_id: "agent-mine" }, null, agents)).toBe(false);
  });
});
