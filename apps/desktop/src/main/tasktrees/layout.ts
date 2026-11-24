import type { PathRules } from "./place";

/** Every branch a task's tree checks out starts with this. */
export const BRANCH_PREFIX = "task/";

/** One tree of a task, as the app's pages ask for it. */
export interface TreeSpec {
  /** An absolute path to the project's folder: the repository's root or a folder inside it. */
  repo: string;
  /** A local branch ("main") or a remote-tracking branch ("origin/main"). Nothing is fetched. */
  baseBranch: string;
  /** False for a tree that is only laid out, not made. */
  build?: boolean;
}

/** One tree's place in a task. */
export interface Tree {
  /** Identifies the tree inside its task and, when the task has several, names its subfolder. */
  slug: string;
  /** The worktree's folder. */
  path: string;
  repo: string;
  baseBranch: string;
  /** The task branch checked out in the worktree. */
  branch: string;
  /** Whether the tree is made sure of, and not only laid out. */
  build: boolean;
}

/** Where a task's trees live. */
export interface TaskLayout {
  root: string;
  /** The task key the layout was computed from. */
  key: string;
  /** The task's folder. */
  dir: string;
  trees: Tree[];
}

/** `text` with every character outside [A-Za-z0-9._-] replaced by "-". */
export function sanitize(text: string): string {
  return text.replace(/[^A-Za-z0-9._-]/gu, "-");
}

/** Whether `text` can be one component of a branch name, and so also a task folder's name. */
export function validComponent(text: string): boolean {
  return (
    text !== "" && !text.startsWith(".") && !text.endsWith(".") && !text.endsWith(".lock") && !text.includes("..")
  );
}

/** Whether git would accept `name` as a branch name. */
export function validBranchName(name: string): boolean {
  if (name === "" || name === "@" || name.startsWith("-") || name.includes("@{")) return false;
  for (const char of name) {
    const code = char.codePointAt(0) ?? 0;
    if (code < 0x20 || code === 0x7f || " ~^:?*[\\".includes(char)) return false;
  }
  return name.split("/").every(validComponent);
}

/**
 * The task folder of `taskKey` under `root`. `root` has to be an absolute path
 * that is not a filesystem root: the task folders need one of their own.
 */
export function taskDir(paths: PathRules, root: string, taskKey: string): string {
  if (root === "" || !paths.isAbsolute(root)) {
    throw new Error(`the task folder ${JSON.stringify(root)} must be an absolute path`);
  }
  const cleaned = paths.normalize(root);
  if (paths.dirname(cleaned) === cleaned) {
    throw new Error(
      `the task folder ${JSON.stringify(root)} is a filesystem root; task trees need a folder of their own`,
    );
  }
  const key = sanitize(taskKey);
  if (!validComponent(key)) throw new Error(`${JSON.stringify(taskKey)} is not a usable task key`);
  return paths.join(cleaned, key);
}

/**
 * Where the trees of a task live: its folder and, for each spec in order, the
 * worktree's path and task branch. Nothing on disk is touched.
 *
 * With one spec the task folder is the worktree and the task branch is
 * task/<task key>. With more, the task folder is a plain folder, each worktree
 * is the subfolder named by its slug, and the task branch is
 * task/<task key>/<slug>. A slug is the name of the repository's folder, or
 * that name and "--" and the base branch when the repository is listed twice.
 */
export function layout(paths: PathRules, root: string, taskKey: string, specs: TreeSpec[]): TaskLayout {
  const dir = taskDir(paths, root, taskKey);
  if (specs.length === 0) throw new Error("a task needs at least one tree");

  const repos = specs.map((spec, index) => {
    if (spec.repo === "" || !paths.isAbsolute(spec.repo)) {
      throw new Error(`tree ${index + 1}: the repository path ${JSON.stringify(spec.repo)} must be an absolute path`);
    }
    return paths.normalize(spec.repo);
  });
  const counts = new Map<string, number>();
  for (const repo of repos) counts.set(repo, (counts.get(repo) ?? 0) + 1);
  const slugs = repos.map((repo, index) => {
    let slug = sanitize(paths.basename(repo));
    if ((counts.get(repo) ?? 0) > 1) slug += `--${sanitize(specs[index]!.baseBranch)}`;
    if (!validComponent(slug)) {
      throw new Error(
        `tree ${index + 1}: the repository ${JSON.stringify(specs[index]!.repo)} yields no usable slug ${JSON.stringify(slug)}`,
      );
    }
    return slug;
  });
  specs.forEach((spec, index) => {
    if (!validBranchName(spec.baseBranch)) {
      throw new Error(`tree ${index + 1}: ${JSON.stringify(spec.baseBranch)} is not a usable branch name`);
    }
  });

  const key = sanitize(taskKey);
  const plan: TaskLayout = { root: paths.normalize(root), key, dir, trees: [] };
  const taken = new Map<string, number>();
  specs.forEach((spec, index) => {
    const slug = slugs[index]!;
    const tree: Tree = {
      slug,
      path: dir,
      repo: repos[index]!,
      baseBranch: spec.baseBranch,
      branch: `${BRANCH_PREFIX}${key}`,
      build: spec.build !== false,
    };
    if (specs.length > 1) {
      tree.path = paths.join(dir, slug);
      tree.branch += `/${slug}`;
    }
    if (!validBranchName(tree.branch)) {
      throw new Error(
        `tree ${index + 1}: ${JSON.stringify(taskKey)} yields no usable task branch ${JSON.stringify(tree.branch)}`,
      );
    }
    for (const claim of [tree.path, tree.branch]) {
      const other = taken.get(claim);
      if (other !== undefined) {
        throw new Error(`trees ${other + 1} and ${index + 1} would both be ${claim === tree.path ? "at" : "on branch"} ${claim}`);
      }
      taken.set(claim, index);
    }
    plan.trees.push(tree);
  });
  return plan;
}
