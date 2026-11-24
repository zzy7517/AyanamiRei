/** What the daemon reported about the tree it built for one of a task's
 *  "project + branch" pairs. */
export type IssueProjectTreeState = "pending" | "ready" | "failed" | "deleted";

export interface IssueProjectTree {
  id: string;
  state: IssueProjectTreeState;
  /** Where the tree is on the machine that built it; empty until then and once deleted. */
  path: string;
  /** The branch the tree works on. It outlives the tree. */
  task_branch: string;
  /** Why the daemon could not build or remove the tree. */
  error: string;
  created_at: string;
  updated_at: string;
}

/** One "project + branch" pair a task works on. */
export interface ProjectScopeItem {
  project_id: string;
  base_branch: string;
  position: number;
  /** The task's tree for the pair; null when the task has none. */
  tree: IssueProjectTree | null;
}

export interface IssueProjectScope {
  items: ProjectScopeItem[];
}

/** One pair of a request that replaces a task's pairs. The branch is
 *  `base_branch` here: a task branches off it. */
export interface IssueProjectScopeItemInput {
  project_id: string;
  base_branch: string;
}

export interface ReplaceIssueProjectScopeRequest {
  items: IssueProjectScopeItemInput[];
}

/** What a desktop app reports about the tree of a pair: that it is built
 *  (`ready`, with where), that building it failed (`failed`, with why), or
 *  that it was removed (`deleted`). */
export interface IssueProjectTreeStateReport {
  state: IssueProjectTreeState;
  path?: string;
  task_branch?: string;
  error?: string;
}

/** One tree a desktop app has to act on. `task_key` is what names the task's
 *  directory and branches. Where the tree goes is where its project's folder
 *  is, which the project's resources say. */
export interface TaskTreeWorkItem {
  tree_id: string;
  issue_id: string;
  task_key: string;
  project_id: string;
  base_branch: string;
  /** The branch the tree works on; empty until the tree was built once. */
  task_branch: string;
  /** `create`: the tree waits to be built. `remove`: its task was complete
   *  for longer than the workspace keeps trees. */
  action: TaskTreeWorkAction | null;
}

export type TaskTreeWorkAction = "create" | "remove";

export interface TaskTreeWork {
  items: TaskTreeWorkItem[];
}
