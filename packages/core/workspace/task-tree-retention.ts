import type { Workspace } from "../types";

/**
 * The workspace setting with how many days a completed task keeps its trees
 * before they are removed. The server decides from it alone.
 */
export const TASK_TREE_RETENTION_DAYS_KEY = "task_tree_retention_days";

/** Days a completed task keeps its trees when the workspace has not set them. The server keeps the same default. */
export const TASK_TREE_RETENTION_DAYS_DEFAULT = 5;

/** The most a workspace may keep them; the server refuses a longer time. */
export const TASK_TREE_RETENTION_DAYS_MAX = 36500;

function isTaskTreeRetentionDays(days: unknown): days is number {
  return (
    typeof days === "number" &&
    Number.isInteger(days) &&
    days >= 1 &&
    days <= TASK_TREE_RETENTION_DAYS_MAX
  );
}

/** The whole number of days typed into the retention field, or null for anything else. */
export function parseTaskTreeRetentionDays(input: string): number | null {
  const days = Number(input);
  return isTaskTreeRetentionDays(days) ? days : null;
}

/**
 * How many days a completed task keeps its trees in this workspace. A stored
 * value that is not a whole number from 1 to the most reads as the default,
 * as it does on the server.
 */
export function taskTreeRetentionDays(
  workspace: Pick<Workspace, "settings"> | null | undefined,
): number {
  const stored = (workspace?.settings as Record<string, unknown> | undefined)?.[
    TASK_TREE_RETENTION_DAYS_KEY
  ];
  return isTaskTreeRetentionDays(stored) ? stored : TASK_TREE_RETENTION_DAYS_DEFAULT;
}
