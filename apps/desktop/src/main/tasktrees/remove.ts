import type { Place } from "./place";
import { readBaseBranch } from "./create";
import {
  GitError,
  detailOf,
  gitMust,
  gitOut,
  gitTrimmed,
  listWorktrees,
  probe,
  refsOf,
  rethrowPlace,
} from "./git";
import { BRANCH_PREFIX, taskDir } from "./layout";

/** The hidden refs that hold work kept off a task branch live below this. */
const LEFTOVER_REF_PREFIX = "refs/ayanami/leftovers/";

/** How many changed files a leftover commit's message lists. The commit itself carries every file. */
const MAX_LEFTOVER_PATHS_IN_MESSAGE = 200;

/** What removing one tree did with the work it held. The task branch stays. */
export interface Removal {
  path: string;
  branch: string;
  /** Uncommitted work was committed to the task branch, which had no commit of its own. */
  autoCommitted: boolean;
  /** The hidden ref that holds uncommitted work kept off the task branch, which had commits of its own. */
  leftoverRef: string;
}

/** One tree as it stands, judged against the task branch its place dictates. */
interface TreeState {
  path: string;
  /** The branch checked out in the tree, "" when HEAD is detached. */
  branch: string;
  /** The root of the repository's main working tree, "" when git cannot name it. */
  repository: string;
  /** The branch the tree was cut from, as its making recorded it. */
  baseBranch: string;
  /** A worktree checked out on the task branch its place dictates. `problem` says why it is not. */
  healthy: boolean;
  problem: string;
  /** The tree holds uncommitted or untracked changes. */
  dirty: boolean;
  /** The commits the tree's branch has beyond its base branch; -1 while that is not known. */
  ahead: number;
}

/** The task branch a tree of the task is on, told by its folder's name. */
function dictatedBranch(key: string, name: string): string {
  return name === key ? `${BRANCH_PREFIX}${key}` : `${BRANCH_PREFIX}${key}/${name}`;
}

async function isDirty(place: Place, path: string): Promise<boolean> {
  return (await gitOut(place, path, ["status", "--porcelain"])).trim() !== "";
}

async function describe(place: Place, path: string, taskBranch: string): Promise<TreeState> {
  const info = await probe(place, path);
  const state: TreeState = {
    path,
    branch: info.branch,
    repository: "",
    baseBranch: "",
    healthy: false,
    problem: "",
    dirty: false,
    ahead: -1,
  };
  const working = info.problem === "" && info.self === info.top;
  if (info.problem !== "") state.problem = info.problem;
  else if (!working) state.problem = `a folder inside the working tree ${info.top}`;
  else if (info.detached) state.problem = `HEAD is detached, not on the task branch ${taskBranch}`;
  else if (info.branch !== taskBranch) {
    state.problem = `on branch ${info.branch}, not on the task branch ${taskBranch}`;
  }
  if (!working) return state;

  const worktrees = await listWorktrees(place, path);
  state.repository = worktrees[0]?.main ? worktrees[0].path : "";
  state.baseBranch = await readBaseBranch(place, info.gitDir);
  state.dirty = await isDirty(place, path).catch(() => false);
  if (state.baseBranch !== "" && !info.detached) {
    const base = await refsOf(place, path, state.baseBranch).catch(() => null);
    if (base?.base) {
      const counts = await gitTrimmed(
        place,
        path,
        ["rev-list", "--left-right", "--count", `${base.base.ref}...HEAD`],
      ).catch(() => "");
      // Left is what HEAD lacks behind the base, right what it has beyond it.
      const [behind, ahead] = counts.split(/\s+/).map(Number);
      if (Number.isInteger(behind) && Number.isInteger(ahead)) state.ahead = ahead;
    }
  }
  state.healthy = state.problem === "";
  return state;
}

/** A committer identity only when the repository does not have one, so that commits still look like they came from its own setup. */
async function identityArgs(place: Place, dir: string): Promise<string[]> {
  const email = await gitTrimmed(place, dir, ["config", "user.email"]).catch(() => "");
  return email === "" ? ["-c", "user.name=Multica Agent", "-c", "user.email=agent@multica.local"] : [];
}

/**
 * Stages and commits all of the tree's changes onto its checked-out branch
 * without running the repository's commit hooks. Says false when there was
 * nothing to commit; an error means nothing was committed and the changes are
 * still only on disk.
 */
async function commitEverything(place: Place, path: string, message: string): Promise<boolean> {
  try {
    await gitMust(place, path, ["add", "-A"]);
  } catch (error) {
    rethrowPlace(error);
    throw new Error(`git add: ${detailOf(error)}`);
  }
  try {
    // --no-verify: the project's commit hooks are written for a person's own
    // workflow (interactive linters, test suites, signing prompts), and a hook
    // failing here would mean losing the work to save a lint run. It does not
    // turn off commit.gpgSign, which is why a commit that fails keeps the tree.
    await gitMust(place, path, [...(await identityArgs(place, path)), "commit", "--no-verify", "-m", message]);
  } catch (error) {
    rethrowPlace(error);
    if (error instanceof GitError && error.output.includes("nothing to commit")) return false;
    throw new Error(`git commit: ${detailOf(error)}`);
  }
  return true;
}

/** Explains a leftover commit to whoever finds it with `git log`. */
function leftoverMessage(branch: string, files: string[]): string {
  const lines = [
    "ayanami: changes the agent left uncommitted",
    "",
    `The agent made commits of its own on ${branch}, so these changes`,
    "were kept off the branch instead of being committed onto it. This",
    "commit's parent is what the branch delivered: `git show --stat <this commit>`",
    "lists the changes, and `git restore --source=<this commit> -- <path>`",
    "brings one back.",
    "",
  ];
  files.slice(0, MAX_LEFTOVER_PATHS_IN_MESSAGE).forEach((file) => lines.push(file));
  if (files.length > MAX_LEFTOVER_PATHS_IN_MESSAGE) {
    lines.push(`…and ${files.length - MAX_LEFTOVER_PATHS_IN_MESSAGE} more`);
  }
  return lines.join("\n");
}

const leftoverRef = (branch: string, key: string) => `${LEFTOVER_REF_PREFIX}${branch}/${key}`;

/**
 * Records everything the tree holds beyond `tip` as one commit under a hidden
 * ref, without touching the branch, the tree or its index, and returns the
 * ref. Returns "" when there is nothing to save. The ref is created, never
 * moved: a second saving goes under another name rather than over the first.
 */
async function saveLeftovers(place: Place, path: string, branch: string, tip: string, key: string): Promise<string> {
  const scratch = await place.tempDir();
  try {
    // The tree `git add -A` would stage, built in a private copy of the tree's
    // index, seeded from it because the modes it recorded cannot be told without it.
    const index = place.paths.join(scratch, "index");
    const source = await gitTrimmed(place, path, ["rev-parse", "--git-path", "index"]);
    try {
      await place.copy(place.paths.isAbsolute(source) ? source : place.paths.join(path, source), index);
    } catch {
      throw new Error("cannot copy the worktree index");
    }
    const env = { GIT_INDEX_FILE: index };
    try {
      await gitMust(place, path, ["add", "-A"], env);
    } catch (error) {
      rethrowPlace(error);
      throw new Error(`stage the worktree in a private index: ${detailOf(error)}`);
    }
    const tree = await gitTrimmed(place, path, ["write-tree"], env);
    const tipTree = await gitTrimmed(place, path, ["rev-parse", "--verify", `${tip}^{tree}`]);
    if (tree === tipTree) return "";

    const changed = await gitTrimmed(place, path, ["diff", "--name-status", "--no-renames", tipTree, tree]);
    const files = changed === "" ? [] : changed.split("\n");
    const commit = (
      await gitOut(place, path, [
        ...(await identityArgs(place, path)),
        "commit-tree",
        tree,
        "-p",
        tip,
        "-m",
        leftoverMessage(branch, files),
      ])
    ).trim();
    if (commit === "") throw new Error("git commit-tree returned no commit");

    // The empty old value makes update-ref refuse a ref that already exists.
    const ref = leftoverRef(branch, key);
    const saved = await gitMust(place, path, ["update-ref", ref, commit, ""]).then(
      () => ref,
      (error: unknown) => {
        rethrowPlace(error);
        return null;
      },
    );
    if (saved !== null) return saved;
    const other = leftoverRef(branch, `${key}-${commit.slice(0, 12)}`);
    await gitMust(place, path, ["update-ref", other, commit, ""]).catch((error: unknown) => {
      rethrowPlace(error);
      throw new Error(`git update-ref ${other}: ${detailOf(error)}`);
    });
    return other;
  } finally {
    await place.removeRecursively(scratch).catch(() => undefined);
  }
}

/**
 * Applies the delivery rules to what is left uncommitted in the tree, then
 * removes its worktree. On any failure the tree stays exactly as it is and the
 * error says why it was kept.
 */
async function finalizeAndRemove(place: Place, path: string, taskBranch: string, key: string): Promise<Removal> {
  const state = await describe(place, path, taskBranch);
  const removal: Removal = { path, branch: taskBranch, autoCommitted: false, leftoverRef: "" };
  const kept = (reason: string): Error =>
    state.healthy
      ? new Error(
          `${reason}; the work is preserved at ${path} (listed by \`git worktree list\` in ${state.repository || path})`,
        )
      : new Error(reason);

  if (!state.healthy) throw kept(`refusing to remove ${path}: ${state.problem}`);

  // An unresolved merge is never committed: `git add -A` would turn the
  // conflict markers into a commit. The tree is kept for someone to finish the
  // merge.
  let unmerged: string[];
  try {
    unmerged = (await gitOut(place, path, ["diff", "--name-only", "--diff-filter=U", "-z"]))
      .split("\0")
      .filter((name) => name !== "");
  } catch (error) {
    rethrowPlace(error);
    throw kept(`could not check the tree on ${taskBranch} for an unresolved merge: ${detailOf(error)}`);
  }
  if (unmerged.length > 0) {
    throw kept(`refusing to remove the tree on ${taskBranch}: ${unmerged.join(", ")} are still unmerged`);
  }

  // Not being able to tell is treated as dirty: a commit that finds nothing
  // costs an error at worst, assuming clean risks deleting work.
  const dirty = await isDirty(place, path).catch(() => true);
  if (dirty) {
    // A branch without a commit of its own takes everything left uncommitted;
    // one that has commits keeps them off, and the leftovers go under a hidden
    // ref. An unknown ahead count, the base branch being gone, counts as no
    // commit of its own: the side that keeps the work on the branch.
    if (state.ahead > 0) {
      let tip: string;
      try {
        tip = (await gitOut(place, path, ["rev-parse", "--verify", "HEAD"])).trim();
      } catch (error) {
        rethrowPlace(error);
        throw kept(`could not resolve the tip of ${taskBranch}: ${detailOf(error)}`);
      }
      try {
        removal.leftoverRef = await saveLeftovers(place, path, taskBranch, tip, key);
      } catch (error) {
        rethrowPlace(error);
        throw kept(`could not save the changes left uncommitted on ${taskBranch}: ${detailOf(error)}`);
      }
    } else {
      try {
        removal.autoCommitted = await commitEverything(
          place,
          path,
          `chore(task): uncommitted changes from ${taskBranch}`,
        );
      } catch (error) {
        rethrowPlace(error);
        throw kept(`could not commit the uncommitted changes to ${taskBranch}: ${detailOf(error)}`);
      }
    }
  }

  try {
    await gitMust(place, state.repository || path, ["worktree", "remove", "--force", path]);
  } catch (error) {
    rethrowPlace(error);
    throw kept(`could not remove the worktree of ${taskBranch}: ${detailOf(error)}`);
  }
  return removal;
}

/**
 * Removes the tree of the task that is on `branch`, from the task's folder
 * under `root`, after the delivery rules were applied to what was left
 * uncommitted in it. The task branch stays, and so does anything else in the
 * task's folder; that folder is removed once it holds nothing.
 *
 * Each tree is handled on its own. One that cannot be finalized is kept, and
 * the error says why. Nothing is removed when the tree is not there anymore.
 */
export async function removeTree(
  place: Place,
  root: string,
  key: string,
  branch: string,
): Promise<Removal[]> {
  const { paths } = place;
  const dir = taskDir(paths, root, key);
  const [dirKind = null, dirGitKind = null] = (await place.stat([dir, paths.join(dir, ".git")]))
    .map((kind) => kind ?? null);
  if (dirKind === null) return [];
  if (dirKind !== "dir") throw new Error(`the task folder ${dir} is not a folder`);

  let trees: { path: string; name: string }[];
  if (dirGitKind !== null) {
    trees = [{ path: dir, name: key }];
  } else {
    // A link standing in for a folder is not followed: a worktree elsewhere is not a tree of the task.
    const names = await place.list(dir);
    const kinds = await place.stat(names.flatMap((name) => [paths.join(dir, name), paths.join(dir, name, ".git")]));
    trees = names.flatMap((name, index) =>
      kinds[2 * index] === "dir" && (kinds[2 * index + 1] ?? null) !== null
        ? [{ path: paths.join(dir, name), name }]
        : [],
    );
  }

  const removed: Removal[] = [];
  const kept: string[] = [];
  for (const tree of trees) {
    if (dictatedBranch(key, tree.name) !== branch) continue;
    try {
      removed.push(await finalizeAndRemove(place, tree.path, branch, key));
    } catch (error) {
      rethrowPlace(error);
      kept.push(detailOf(error));
    }
  }
  if (kept.length > 0) throw new Error(kept.join("\n"));
  // git removes the folder of a task with a single tree together with its worktree.
  const left = (await place.stat([dir]))[0] ?? null;
  if (left === "dir" && (await place.list(dir)).length === 0) await place.removeEmpty(dir);
  return removed;
}
