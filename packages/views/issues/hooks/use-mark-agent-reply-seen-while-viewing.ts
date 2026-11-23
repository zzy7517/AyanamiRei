import { useEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { useMarkIssueAgentReplySeen } from "@multica/core/issues/agent-reply";
import { issueDetailOptions } from "@multica/core/issues/queries";
import { useAppForeground } from "../../common/use-app-foreground";

/**
 * Marks the agent's reply seen while a task is on screen: when it opens with
 * a reply waiting, and each time an agent ends a turn while it stays open —
 * the viewer is watching, so no reply is left marked. Nothing is marked while
 * the app is in the background.
 *
 * The marker is read from the task the caches hold, which the
 * `issue:agent_replied` event keeps current. Each marker is written at most
 * once per viewing: the mutation's own retries cover transient failures, and
 * the next reply or reopening the task tries again.
 */
export function useMarkAgentReplySeenWhileViewing(wsId: string, issueId: string) {
  const foreground = useAppForeground();
  const { data: repliedAt = null, isSuccess } = useQuery({
    ...issueDetailOptions(wsId, issueId),
    select: (issue) => issue.agent_replied_at ?? null,
    enabled: !!issueId,
  });
  const { mutate: markSeen } = useMarkIssueAgentReplySeen(wsId, issueId);
  const written = useRef<string | null>(null);

  useEffect(() => {
    if (!issueId || !isSuccess || !foreground || !repliedAt) return;
    const reply = `${issueId}|${repliedAt}`;
    if (written.current === reply) return;
    written.current = reply;
    markSeen();
  }, [issueId, isSuccess, foreground, repliedAt, markSeen]);
}
