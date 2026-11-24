import { useQueries, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useWorkspaceList } from "@multica/core/workspace";
import { reconcileTaskTrees, taskTreeWorkOptions, treeHost } from "@multica/core/tasktrees";

/**
 * Keeps the disk in step with the server's lists of tree work, for every
 * workspace this account is in whichever one the window shows: a tree waiting
 * to be built is built, and one of a task that was complete for long enough
 * is removed. Each reading of a list gets one pass, however it came to be read
 * again; the pass's own reading of the list is not another. What a pass could
 * not do stays listed for the reading after. Nothing runs where this window
 * cannot build.
 */
export function TaskTreeBridge() {
  const qc = useQueryClient();
  const canBuild = treeHost() !== null;
  const { workspaces } = useWorkspaceList({ enabled: canBuild });
  const lists = useQueries({
    queries: workspaces.map(({ id }) => ({ ...taskTreeWorkOptions(id), enabled: canBuild })),
    combine: (results) =>
      results.flatMap((result, index) => {
        const wsId = workspaces[index]?.id;
        return wsId !== undefined && result.data
          ? [{ wsId, work: result.data, readAt: result.dataUpdatedAt }]
          : [];
      }),
  });
  // When the last pass for each workspace ended. A list read no later than
  // that was read by that pass, at its end.
  const passes = useRef(new Map<string, number>());
  const running = useRef(new Set<string>());
  const [, lookAgain] = useState(0);

  useEffect(() => {
    for (const { wsId, work, readAt } of lists) {
      // An empty list is not work.
      if (work.items.length === 0 || running.current.has(wsId)) continue;
      if (readAt <= (passes.current.get(wsId) ?? 0)) continue;
      running.current.add(wsId);
      // On the promise: the result is this window's to keep track of.
      reconcileTaskTrees(qc, wsId, work)
        .catch(() => undefined)
        .then(() => {
          passes.current.set(wsId, Date.now());
          running.current.delete(wsId);
          // A reading may have arrived while the pass ran.
          lookAgain((count) => count + 1);
        });
    }
  }, [lists, qc]);

  return null;
}
