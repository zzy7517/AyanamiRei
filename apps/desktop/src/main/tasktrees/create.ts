import type { Entry, Place } from "./place";
import {
  detailOf,
  gitMust,
  gitOptional,
  gitOut,
  listWorktrees,
  openRepository,
  probe,
  refsOf,
  resolveChain,
  rethrowPlace,
  type Repository,
  type WorktreeEntry,
} from "./git";
import type { TaskLayout, Tree } from "./layout";

/**
 * Names the file in a tree's private git dir that records the branch the tree
 * was cut from. It lives with the worktree's registration, so git removes it
 * with the tree and nothing has to track it.
 */
const BASE_BRANCH_FILE = "ayanami-base-branch";

/** One tree's part of a checked plan. */
interface Step {
  tree: Tree;
  repo: Repository;
  /** The commit of the base branch the tree starts from. */
  base: string;
  /** A worktree already stands at the tree's path, on the task branch, cut from the tree's repository. */
  healthy: boolean;
  /** The task branch survived an earlier tree. */
  branchExists: boolean;
  /** The repository still registers a worktree at the tree's path although none is there. */
  stale: boolean;
}

/** Whether the worktree of a tree is registered: its `.git` entry stands. */
async function isWorkTree(place: Place, path: string): Promise<boolean> {
  return ((await place.stat([place.paths.join(path, ".git")]))[0] ?? null) !== null;
}

/**
 * Makes sure every tree of the plan that is to be built stands where the plan
 * says, and records each one's base branch.
 *
 * A tree whose task branch does not exist yet is cut from the latest commit of
 * its base branch as that ref stands there; nothing is fetched, and nothing
 * uncommitted in the project's own checkout is carried over. One whose branch
 * survived an earlier tree is checked out from that branch again. A tree that
 * already is a healthy worktree of its repository on its task branch is left
 * alone, so this is safe to repeat and is the rebuild after a deletion.
 *
 * Everything is checked before anything is made. When a tree cannot be made,
 * the trees this call added are removed again, with the task branches it
 * created, which are new and empty.
 */
export async function createTrees(place: Place, plan: TaskLayout): Promise<void> {
  const { paths } = place;
  const made = plan.trees.filter((tree) => tree.build);
  if (made.length === 0) throw new Error("a task needs at least one tree to build");

  // What stands where, and what each path really is, each in one round.
  const repoPaths = [...new Set(made.map((tree) => tree.repo))];
  const kinds = await place.stat([
    plan.root,
    plan.dir,
    paths.join(plan.dir, ".git"),
    ...made.map((tree) => tree.path),
    ...repoPaths,
  ]);
  const [rootKind = null, dirKind = null, dirGitKind = null] = kinds.map((kind) => kind ?? null);
  const treeKind = (index: number): Entry | null => kinds[3 + index] ?? null;
  const chain = await resolveChain(place, [plan.root, ...made.map((tree) => tree.path), ...repoPaths]);

  if (plan.trees.length > 1) {
    if (dirGitKind !== null) {
      throw new Error(
        `the task folder ${plan.dir} is itself a worktree and cannot hold the ${plan.trees.length} trees of this task`,
      );
    }
    if (dirKind !== null && dirKind !== "dir") {
      throw new Error(`the task folder ${plan.dir} exists and is not a folder`);
    }
  }

  // A tree inside a checkout would show up in that checkout's status, so the
  // task folder may not lie in one. The nearest folder above it that stands says.
  let above: string | null = null;
  for (let dir = plan.root; ; dir = paths.dirname(dir)) {
    above = chain.found.get(dir) ?? null;
    if (above !== null || paths.dirname(dir) === dir) break;
  }
  if (above !== null) {
    const top = await gitOptional(place, above, ["rev-parse", "--show-toplevel"]);
    if (top !== null) {
      throw new Error(
        `the task folder ${plan.root} would be inside the git working tree ${top}; task trees belong outside every repository checkout`,
      );
    }
  }

  // Each repository is opened once. Two folders of one repository share it.
  const repos: Repository[] = [];
  const repoOf: number[] = [];
  for (const [index, path] of repoPaths.entries()) {
    const kind = kinds[3 + made.length + index] ?? null;
    if (kind === null) throw new Error(`the repository ${path} does not exist`);
    let target = path;
    if (kind !== "dir") {
      target = chain.found.get(path) ?? "";
      if (target === "" || ((await place.stat([target]))[0] ?? null) !== "dir") {
        throw new Error(`the repository ${path} is not a folder`);
      }
    }
    const repo = await openRepository(place, target);
    let number = repos.findIndex((known) => known.common === repo.common);
    if (number === -1) {
      number = repos.length;
      repos.push(repo);
    }
    repoOf.push(number);
  }

  const steps: Step[] = [];
  const registrations = new Map<number, WorktreeEntry[]>();
  const pairs = new Set<string>();
  const occupied: Step[] = [];
  for (const [index, tree] of made.entries()) {
    const number = repoOf[repoPaths.indexOf(tree.repo)] ?? 0;
    const repo = repos[number]!;
    const refs = await refsOf(place, repo.root, tree.baseBranch, tree.branch);
    if (!refs.base) {
      throw new Error(
        `the repository ${JSON.stringify(tree.repo)} has no local or remote-tracking branch ${JSON.stringify(tree.baseBranch)}`,
      );
    }
    const pair = `${repo.common}\n${tree.baseBranch}`;
    if (pairs.has(pair)) {
      throw new Error(
        `the repository ${JSON.stringify(tree.repo)} is listed twice with base branch ${JSON.stringify(tree.baseBranch)}`,
      );
    }
    pairs.add(pair);

    let registered = registrations.get(number);
    if (!registered) {
      registered = await listWorktrees(place, repo.root);
      registrations.set(number, registered);
    }
    const kind = treeKind(index);
    const step: Step = {
      tree,
      repo,
      base: refs.base.commit,
      healthy: false,
      branchExists: refs.taskBranch !== null,
      stale: false,
    };
    // git names a registered worktree by its real path.
    const realish = chain.realish[1 + index] ?? tree.path;
    for (const entry of registered) {
      if (entry.path === realish) step.stale = true;
      else if (step.branchExists && entry.branch === `refs/heads/${tree.branch}`) {
        throw new Error(`the task branch ${tree.branch} is checked out at ${entry.path}; remove that worktree first`);
      }
    }
    steps.push(step);

    // An existing worktree has to be exactly the tree the plan describes.
    // Anything else but an empty folder, which git worktree add fills, is refused.
    if (kind === null) continue;
    if (kind !== "dir") throw new Error(`${tree.path} exists and is not a folder`);
    if ((await place.list(tree.path)).length > 0) {
      step.stale = false;
      occupied.push(step);
    }
  }
  for (const step of occupied) {
    const { tree } = step;
    const found = await probe(place, tree.path);
    if (found.problem !== "") throw new Error(`${tree.path} exists and is not a worktree: ${found.problem}`);
    if (found.self !== found.top) {
      throw new Error(`${tree.path} exists and is a folder inside the working tree ${found.top}`);
    }
    if (found.common !== step.repo.common) {
      throw new Error(`${tree.path} exists and is a worktree of another repository (${found.common})`);
    }
    if (found.detached) {
      throw new Error(`${tree.path} exists and has a detached HEAD, not the task branch ${tree.branch}`);
    }
    if (found.branch !== tree.branch) {
      throw new Error(`${tree.path} exists and is on branch ${found.branch}, not on the task branch ${tree.branch}`);
    }
    step.healthy = true;
  }

  if (dirKind === null) await place.mkdir(plan.dir);
  const added: Step[] = [];
  let current: Tree | null = null;
  try {
    for (const step of steps) {
      current = step.tree;
      if (step.healthy) await recordBaseBranch(place, step.tree, false);
      else {
        await clearStaleRegistration(place, step);
        // Only the add can leave a worktree behind: git registers one before
        // its checkout hooks run, so a failed add is undone too.
        added.push(step);
        await addTree(place, step);
      }
    }
  } catch (error) {
    let message = `create tree ${current?.path ?? plan.dir}: ${detailOf(error)}`;
    const rollback = await undoCreated(place, added);
    if (rollback !== "") message += `; could not undo this: ${rollback}`;
    // What was not there before is not left behind either.
    if (dirKind === null) await place.removeEmpty(plan.dir).catch(() => false);
    if (rootKind === null) await place.removeEmpty(plan.root).catch(() => false);
    throw new Error(message);
  }
}

/**
 * Drops the registration a repository keeps for the step's path although no
 * worktree stands there, so that the path is free for the tree. Nothing else
 * of the repository is pruned.
 */
async function clearStaleRegistration(place: Place, step: Step): Promise<void> {
  if (!step.stale) return;
  // git removes the registration of a path only when nothing stands there, and
  // refuses an empty folder, which is what this make has just created for the
  // tree to fill.
  if (((await place.stat([step.tree.path]))[0] ?? null) !== null) await place.remove(step.tree.path);
  try {
    await gitMust(place, step.repo.root, ["worktree", "remove", "--force", step.tree.path]);
  } catch (error) {
    rethrowPlace(error);
    throw new Error(`clear the worktree registration left behind at the path: ${detailOf(error)}`);
  }
}

/**
 * Makes the worktree of one step. A missing task branch is created at the base
 * commit; passing the commit rather than the branch keeps git from configuring
 * an upstream on the task branch when the base is a remote-tracking branch.
 */
async function addTree(place: Place, step: Step): Promise<void> {
  const { tree } = step;
  await gitOut(
    place,
    step.repo.root,
    step.branchExists
      ? ["worktree", "add", tree.path, tree.branch]
      : ["worktree", "add", tree.path, "-b", tree.branch, step.base],
  );
  await recordBaseBranch(place, tree, true);
}

/**
 * Removes the trees a failed make added. A task branch the call created goes
 * with its tree while it still stands at its base commit, as new and empty as
 * it was made; one that survived an earlier tree, or received a commit
 * meanwhile, stays. Says what could not be undone.
 */
async function undoCreated(place: Place, added: Step[]): Promise<string> {
  const failures: string[] = [];
  for (const step of [...added].reverse()) {
    const { tree } = step;
    // A step that failed before git registered its tree leaves nothing to
    // remove; a worktree still standing after the remove is a failure.
    let removed = true;
    try {
      await gitMust(place, step.repo.root, ["worktree", "remove", "--force", tree.path]);
    } catch {
      removed = false;
    }
    if (!removed && (await isWorkTree(place, tree.path))) {
      failures.push(`roll back ${tree.path}`);
      continue;
    }
    if (step.branchExists) continue;
    const at = await gitOptional(place, step.repo.root, ["rev-parse", "--verify", `refs/heads/${tree.branch}`]);
    if (at !== step.base) continue;
    try {
      await gitMust(place, step.repo.root, ["branch", "-D", "--", tree.branch]);
    } catch (error) {
      rethrowPlace(error);
      failures.push(`roll back branch ${tree.branch}: ${detailOf(error)}`);
    }
  }
  return failures.join("; ");
}

/** Stores the tree's base branch in the worktree's private git dir, where removal reads it back. */
async function recordBaseBranch(place: Place, tree: Tree, overwrite: boolean): Promise<void> {
  let gitDir: string;
  try {
    gitDir = (await gitOut(place, tree.path, ["rev-parse", "--absolute-git-dir"])).trim();
  } catch (error) {
    rethrowPlace(error);
    throw new Error(`locate the git dir of the worktree: ${detailOf(error)}`);
  }
  const file = place.paths.join(gitDir, BASE_BRANCH_FILE);
  if (!overwrite && ((await place.stat([file]))[0] ?? null) !== null) return;
  try {
    await place.write(file, `${tree.baseBranch}\n`);
  } catch (error) {
    rethrowPlace(error);
    throw new Error(`record the base branch: ${detailOf(error)}`);
  }
}

/** The base branch a tree was cut from, as its making recorded it; "" when nothing was recorded. */
export async function readBaseBranch(place: Place, gitDir: string): Promise<string> {
  return (await place.read(place.paths.join(gitDir, BASE_BRANCH_FILE))).trim();
}
