// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Workspace } from "../types";
import {
  parseTaskTreeRetentionDays,
  taskTreeRetentionDays,
  TASK_TREE_RETENTION_DAYS_DEFAULT,
} from "./task-tree-retention";

function ws(settings: Record<string, unknown>): Pick<Workspace, "settings"> {
  return { settings };
}

describe("taskTreeRetentionDays", () => {
  it("reads 5 days when the workspace has not set them", () => {
    expect(taskTreeRetentionDays(null)).toBe(TASK_TREE_RETENTION_DAYS_DEFAULT);
    expect(taskTreeRetentionDays(undefined)).toBe(TASK_TREE_RETENTION_DAYS_DEFAULT);
    expect(taskTreeRetentionDays(ws({ github_enabled: false }))).toBe(5);
  });

  it("reads the workspace's own number of days", () => {
    expect(taskTreeRetentionDays(ws({ task_tree_retention_days: 30 }))).toBe(30);
    expect(taskTreeRetentionDays(ws({ task_tree_retention_days: 1 }))).toBe(1);
  });

  it.each([0, -3, 2.5, 36501, "7", null, true])(
    "reads a stored %s as the default, as the server does",
    (stored) => {
      expect(
        taskTreeRetentionDays(ws({ task_tree_retention_days: stored })),
      ).toBe(TASK_TREE_RETENTION_DAYS_DEFAULT);
    },
  );
});

describe("parseTaskTreeRetentionDays", () => {
  it.each([
    ["1", 1],
    ["30", 30],
    ["36500", 36500],
  ])("takes %s as %i days", (input, days) => {
    expect(parseTaskTreeRetentionDays(input)).toBe(days);
  });

  it.each(["", "0", "-3", "2.5", "36501", "abc"])("refuses %j", (input) => {
    expect(parseTaskTreeRetentionDays(input)).toBeNull();
  });
});
