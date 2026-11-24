import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { issueDetailOptions } from "../issues/queries";
import { issueProjectScopeKeys, issueProjectScopeOptions } from "../issues/project-scope";
import { projectResourcesOptions } from "../projects/resource-queries";
import type { TaskTreeWork, TaskTreeWorkItem } from "../types";
import { READ_AGAIN_AFTER_MS, ensureTaskTrees, treeFolder } from "./ensure";
import { TreeFailure, taskTreeKey, treeHost } from "./host";
import { taskTreeKeys } from "./work";

export interface RemoveTaskTreeInput {
  issueId: string;
  treeId: string;
  /** What names the task's directory and branches. */
  taskKey: string;
  projectId: string;
  /** The branch the tree works on. It names the tree where the task has several. */
  taskBranch: string;
}

/**
 * Removes one tree of a task from where its project's folder is, the delivery
 * rules having been applied to what was left uncommitted in it, and says so
 * to the server. The task branch stays.
 *
 * Rejects with the reason when the tree was kept. Its state on the server is
 * then left as it was: the tree is still there.
 */
export async function removeTaskTree(
  qc: QueryClient,
  wsId: string,
  input: RemoveTaskTreeInput,
): Promise<void> {
  const host = treeHost();
  if (!host) throw new TreeFailure("this client cannot remove trees");
  const { issueId, treeId } = input;
  const resources = await qc.fetchQuery({
    ...projectResourcesOptions(wsId, input.projectId),
    staleTime: READ_AGAIN_AFTER_MS,
  });
  const folder = treeFolder(resources.resources);
  if (folder) {
    const result = await host.remove(folder.place, input.taskKey, { branch: input.taskBranch });
    if (!result.ok) throw new TreeFailure(result.error);
  }
  await api.reportIssueProjectTreeState(wsId, issueId, treeId, { state: "deleted" });
}

/**
 * Removes a tree of the task from the disk, then marks it deleted on the
 * server. Rejects with the reason when the tree was kept; nothing is taken
 * out of the cache first.
 */
export function useDeleteIssueProjectTree(wsId: string, issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (treeId: string) => {
      const issue = await qc.ensureQueryData(issueDetailOptions(wsId, issueId));
      const scope = await qc.ensureQueryData(issueProjectScopeOptions(wsId, issueId));
      const item = scope.items.find((candidate) => candidate.tree?.id === treeId);
      const tree = item?.tree;
      if (!item || !tree) throw new TreeFailure("the task no longer has this tree");
      await removeTaskTree(qc, wsId, {
        issueId,
        treeId,
        taskKey: taskTreeKey(issue.identifier),
        projectId: item.project_id,
        taskBranch: tree.task_branch,
      });
    },
    // The tree is gone when this says so, and until then the row keeps it.
    onSuccess: () => qc.invalidateQueries({ queryKey: issueProjectScopeKeys.own(wsId, issueId) }),
  });
}

/**
 * Brings the disk in step with one reading of the server's list for a
 * workspace: every tree waiting to be built is built, every tree of a task
 * that was complete for longer than the workspace keeps trees is removed, and
 * the server is told of each. One tree's failure is its own: it is reported,
 * or left for a person to see, and the others are still seen to. What could
 * not be done stays listed, for the reading after.
 *
 * Resolves once every item was seen to and both lists were read again.
 */
export async function reconcileTaskTrees(qc: QueryClient, wsId: string, work: TaskTreeWork): Promise<void> {
  const tasks = new Map<string, TaskTreeWorkItem[]>();
  for (const item of work.items) {
    // An action this client does not know is not acted on.
    if (item.action === null) continue;
    tasks.set(item.issue_id, [...(tasks.get(item.issue_id) ?? []), item]);
  }
  for (const [issueId, items] of tasks) {
    const creates = items.flatMap((item) => (item.action === "create" ? [item.tree_id] : []));
    if (creates.length > 0) {
      await ensureTaskTrees(qc, wsId, { issueId, treeIds: creates }).catch(() => undefined);
    }
    for (const item of items) {
      if (item.action !== "remove") continue;
      // A tree that was kept stays listed, so it is asked about again at the next reading.
      await removeTaskTree(qc, wsId, {
        issueId,
        treeId: item.tree_id,
        taskKey: item.task_key,
        projectId: item.project_id,
        taskBranch: item.task_branch,
      }).catch(() => undefined);
    }
  }
  // What this pass did changes both lists.
  await Promise.all([
    qc.invalidateQueries({ queryKey: taskTreeKeys.work(wsId) }),
    qc.invalidateQueries({ queryKey: issueProjectScopeKeys.all(wsId) }),
  ]);
}
