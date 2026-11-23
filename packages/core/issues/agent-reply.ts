import { useEffect } from "react";
import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { Issue, ListIssuesResponse } from "../types";
import { sidebarListEntries, tableRowEntries } from "./cache-coordinator";
import { issueKeys } from "./queries";
import { patchIssueSnapshot } from "./ws-updaters";

/**
 * The "new reply" marker of a task (`agent_replied_at`): when its agent last
 * ended a turn that no member has seen yet. It moves forward with each turn
 * and clears when a member sees the reply. Like the rest of a task it lives in
 * the issue query caches, never in a store.
 */

/** Puts the marker into every issue cache that holds the task: what the
 *  `issue:agent_replied` event and the answer to marking it seen share. */
export function patchIssueAgentReply(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  agentRepliedAt: string | null,
): void {
  const patch: Partial<Issue> = { agent_replied_at: agentRepliedAt };
  patchIssueSnapshot(qc, wsId, issueId, patch);
  // The sidebar's lists keep their tasks under a shape of their own.
  for (const [key, data] of sidebarListEntries(qc, wsId)) {
    if (!data.issues.some((issue) => issue.id === issueId)) continue;
    qc.setQueryData<ListIssuesResponse>(key, {
      ...data,
      issues: data.issues.map((issue) => (issue.id === issueId ? { ...issue, ...patch } : issue)),
    });
  }
}

/** The task as a cache already holds it: the detail first, then the lists. */
export function cachedIssue(
  qc: QueryClient,
  wsId: string,
  issueId: string,
): Pick<Issue, "identifier" | "title"> | null {
  const detail = qc.getQueryData<Issue>(issueKeys.detail(wsId, issueId));
  if (detail) return detail;
  for (const [, data] of sidebarListEntries(qc, wsId)) {
    const issue = data.issues.find((candidate) => candidate.id === issueId);
    if (issue) return issue;
  }
  for (const [, data] of tableRowEntries(qc, wsId)) {
    const row = data.rows.find((candidate) => candidate.issue.id === issueId);
    if (row) return row.issue;
  }
  return null;
}

// The tasks whose page or agent terminal is on screen right now. A window
// shows one page at a time, so a mounted page is a shown one.
const shownIssues = new Set<string>();

export function isIssueShown(issueId: string): boolean {
  return shownIssues.has(issueId);
}

/** Keeps the task on screen for as long as the caller is mounted. */
export function useIssueShown(issueId: string | null | undefined): void {
  useEffect(() => {
    if (!issueId) return;
    shownIssues.add(issueId);
    return () => {
      shownIssues.delete(issueId);
    };
  }, [issueId]);
}

let replyBody = "";

/** The words under the task's name in the "new reply" banner. Views owns them
 *  and registers them from each tree it mounts, with null when it unmounts. */
export function registerAgentReplyBody(body: string | null): void {
  replyBody = body ?? "";
}

export function agentReplyBody(): string {
  return replyBody;
}

/** Says the viewer saw the agent's reply. The caches then take the marker as
 *  the server left it, the way the event brings it. */
export function useMarkIssueAgentReplySeen(wsId: string, issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.markIssueAgentReplySeen(issueId),
    // The write is idempotent. A 4xx will not change on retry.
    retry: (failures, err) => {
      const status = (err as { status?: number }).status;
      return failures < 2 && (status === undefined || status >= 500);
    },
    retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 8000),
    onSuccess: (answer) => patchIssueAgentReply(qc, wsId, issueId, answer.agent_replied_at),
  });
}
