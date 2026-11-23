/**
 * The viewer's own list (issues they created) — listing-level
 * subscription. Mounted globally (workspace-session-lifetime) alongside
 * useInboxRealtime so the mention bar's suggestions stay fresh regardless
 * of which tab is foregrounded.
 *
 * issue:created     — invalidate myAll(wsId). The server is the authority
 *                     on whether the viewer created the new issue, so a
 *                     fresh fetch is the cheapest correct answer.
 * issue:updated     — patch in-place across every cached list entry.
 * issue:deleted     — strip from every cached list entry.
 * issue_labels:changed — patch labels via the shared updater
 *                     (also handles the detail cache; harmless if
 *                     the issue isn't in any cached list).
 * onReconnect       — invalidate myAll(wsId) since we may have missed
 *                     a create/delete while disconnected.
 *
 * Inbox realtime (use-inbox-realtime.ts) handles its own keys and runs
 * in parallel; the two are independent.
 */
import { useQueryClient } from "@tanstack/react-query";
import { issueKeys } from "@/data/queries/issue-keys";
import { useWSSubscriptions } from "@/lib/use-ws-subscriptions";
import {
  patchIssueLabels,
  patchMyIssueList,
  removeFromMyIssueList,
} from "./issue-ws-updaters";

export function useMyListRealtime() {
  const qc = useQueryClient();

  useWSSubscriptions(
    (ws, wsId) => {
      const invalidateMyAll = () =>
        qc.invalidateQueries({ queryKey: issueKeys.myAll(wsId) });

      return [
        ws.on("issue:created", () => invalidateMyAll()),
        ws.on("issue:updated", (payload) => {
          patchMyIssueList(qc, wsId, payload.issue);
        }),
        ws.on("issue:deleted", (payload) => {
          removeFromMyIssueList(qc, wsId, payload.issue_id);
        }),
        ws.on("issue_labels:changed", (payload) => {
          patchIssueLabels(
            qc,
            wsId,
            payload.issue_id,
            payload.labels,
            payload.issue_revision,
          );
        }),
        ws.onReconnect(invalidateMyAll),
      ];
    },
    [qc],
  );
}
