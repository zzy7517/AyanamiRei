import type { QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { issueDetailOptions } from "../issues/queries";
import { issueProjectScopeKeys, issueProjectScopeOptions } from "../issues/project-scope";
import { projectFolderPlace } from "../projects/folders";
import { projectResourcesOptions } from "../projects/resource-queries";
import type { IssueProjectTreeStateReport, ProjectResource, ProjectScopeItem } from "../types";
import type { TerminalPlace } from "../terminal/host";
import { TreeFailure, placeKey, taskTreeKey, treeHost, type BuiltTree, type TreeSpec } from "./host";

/** What is said of a tree whose project has no folder to build it next to. */
export const NO_TREE_FOLDER = "the project has no folder on this computer or an SSH host";

export interface EnsureTaskTreesInput {
  issueId: string;
  /** The trees to see to. Every tree of the task when left out. */
  treeIds?: string[];
}

type TreeFolder = { place: TerminalPlace; path: string };

/**
 * How long what was read of a task, of its scope and of a project's folders
 * counts as how they are now. A page reads them as it opens, and making sure
 * of trees does not wait for the same answer twice; anything read before that
 * is read again, since where trees stand depends on all of them.
 */
export const READ_AGAIN_AFTER_MS = 15_000;

/**
 * The folder a project's trees are built next to: the first one the project
 * registered, wherever it is.
 */
export function treeFolder(resources: ProjectResource[] | undefined): TreeFolder | null {
  for (const resource of resources ?? []) {
    const folder = projectFolderPlace(resource);
    if (folder) return folder;
  }
  return null;
}

/**
 * Makes sure a task's trees exist where their projects' folders are, and
 * tells the server what became of them.
 *
 * A task's trees on one place are made together, from the whole list of them:
 * where each stands depends on how many there are. `treeIds` names the trees
 * the caller is waiting for; the others are only laid out, and one the server
 * says is deleted is not made. A tree that was made is reported ready and one
 * that could not be is reported failed with the reason.
 *
 * Resolves with where every tree of the places seen to stands, by tree id,
 * including those only laid out, once every tree named was reported. Rejects
 * with that reason when one of them could not be made. Does nothing where
 * there is no builder. The same call is safe to make again while it runs: the
 * builder takes tasks one at a time.
 */
export async function ensureTaskTrees(
  qc: QueryClient,
  wsId: string,
  input: EnsureTaskTreesInput,
): Promise<Map<string, BuiltTree>> {
  const host = treeHost();
  if (!host) return new Map();
  const { issueId } = input;
  const issue = await qc.fetchQuery({ ...issueDetailOptions(wsId, issueId), staleTime: READ_AGAIN_AFTER_MS });
  const scope = await qc.fetchQuery({ ...issueProjectScopeOptions(wsId, issueId), staleTime: READ_AGAIN_AFTER_MS });
  const wanted = (item: ProjectScopeItem) =>
    input.treeIds === undefined || (item.tree !== null && input.treeIds.includes(item.tree.id));

  // A place's trees, in the task's order.
  const places = new Map<string, { place: TerminalPlace; specs: TreeSpec[]; items: ProjectScopeItem[] }>();
  const withoutFolder: ProjectScopeItem[] = [];
  for (const item of scope.items) {
    if (item.tree === null) continue;
    const resources = await qc.fetchQuery({ ...projectResourcesOptions(wsId, item.project_id), staleTime: READ_AGAIN_AFTER_MS });
    const folder = treeFolder(resources.resources);
    if (!folder) {
      withoutFolder.push(item);
      continue;
    }
    const group = places.get(placeKey(folder.place)) ?? { place: folder.place, specs: [], items: [] };
    group.specs.push({
      repo: folder.path,
      baseBranch: item.base_branch,
      build: wanted(item) || item.tree.state !== "deleted",
    });
    group.items.push(item);
    places.set(placeKey(folder.place), group);
  }

  const report = (treeId: string, body: IssueProjectTreeStateReport) =>
    api.reportIssueProjectTreeState(wsId, issueId, treeId, body);
  let failure: string | null = null;
  for (const item of withoutFolder) {
    if (!wanted(item)) continue;
    failure ??= NO_TREE_FOLDER;
    await report(item.tree!.id, { state: "failed", error: NO_TREE_FOLDER });
  }
  const built = new Map<string, BuiltTree>();
  for (const { place, specs, items } of places.values()) {
    if (!items.some(wanted)) continue;
    const result = await host.ensure(place, taskTreeKey(issue.identifier), specs);
    if (result.ok) {
      for (const [index, item] of items.entries()) {
        const tree = result.trees[index];
        if (tree) built.set(item.tree!.id, tree);
      }
    }
    for (const [index, item] of items.entries()) {
      const tree = item.tree!;
      if (!result.ok) {
        if (wanted(item)) {
          failure ??= result.error;
          await report(tree.id, { state: "failed", error: result.error });
        }
        continue;
      }
      const built = result.trees[index];
      // A tree that was not made, or already reads as it is, is not reported.
      if (
        specs[index]!.build === false ||
        !built ||
        (tree.state === "ready" && tree.path === built.path && tree.task_branch === built.branch)
      ) {
        continue;
      }
      await report(tree.id, { state: "ready", path: built.path, task_branch: built.branch });
    }
  }
  await qc.invalidateQueries({ queryKey: issueProjectScopeKeys.own(wsId, issueId) });
  if (failure !== null) throw new TreeFailure(failure);
  return built;
}
