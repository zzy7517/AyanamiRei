import type { Place } from "./place";
import { PlaceError, firstLine } from "./place";

/** git refused what was asked. `detail` is git's own explanation, on its first line. */
export class GitError extends Error {
  readonly detail: string;
  /** Everything git printed. */
  readonly output: string;

  constructor(args: string[], run: { code: number; stdout: string; stderr: string }) {
    const detail = firstLine(run.stderr) || run.stderr.trim() || `exit status ${run.code}`;
    super(`git ${args.join(" ")}: ${detail}`);
    this.name = "GitError";
    this.detail = detail;
    this.output = `${run.stdout}${run.stderr}`;
  }
}

/** Why an operation failed, in git's own words for git and in the message's for the rest. */
export function detailOf(error: unknown): string {
  return error instanceof Error ? (error instanceof GitError ? error.detail : error.message) : String(error);
}

/** To call first in a catch that wraps: a place that failed keeps its own message. */
export function rethrowPlace(error: unknown): void {
  if (error instanceof PlaceError) throw error;
}

/**
 * Runs git for what it prints. Rejects with a `GitError` when git refused,
 * and with a `PlaceError` when the place could not run git at all.
 */
export async function gitOut(
  place: Place,
  dir: string,
  args: string[],
  env?: Record<string, string>,
): Promise<string> {
  const run = await place.git(dir, args, env);
  if (run.code !== 0) throw new GitError(args, run);
  return run.stdout;
}

/** `gitOut`, trimmed: for values, which never begin or end in whitespace. */
export async function gitTrimmed(
  place: Place,
  dir: string,
  args: string[],
  env?: Record<string, string>,
): Promise<string> {
  return (await gitOut(place, dir, args, env)).trim();
}

/** `gitTrimmed` that answers null when git refused, and still fails when the place did. */
export async function gitOptional(place: Place, dir: string, args: string[]): Promise<string | null> {
  try {
    return await gitTrimmed(place, dir, args);
  } catch (error) {
    if (error instanceof PlaceError) throw error;
    return null;
  }
}

/** A git command that only has to succeed. Rejects with a `GitError` when it did not. */
export async function gitMust(place: Place, dir: string, args: string[], env?: Record<string, string>): Promise<void> {
  await gitOut(place, dir, args, env);
}

/** The root of a repository's main working tree, and what every worktree of it shares. */
export interface Repository {
  root: string;
  common: string;
}

/**
 * The repository the project's folder belongs to. The folder may be the
 * repository's root or any folder inside it. It has to hold at least one
 * commit, since that is what a tree is cut from.
 */
export async function openRepository(place: Place, path: string): Promise<Repository> {
  let described: string[];
  try {
    // git answers the common dir relative to the working tree it ran in when
    // that is the main one, and absolute from a linked worktree.
    described = (await gitTrimmed(place, path, ["rev-parse", "--show-toplevel", "--git-common-dir"])).split("\n");
  } catch (error) {
    if (error instanceof PlaceError) throw error;
    throw new Error(`the repository ${path} is not a git working tree: ${detailOf(error)}`);
  }
  const [root = "", commonDir = ""] = described.map((line) => line.trim());
  if (root === "" || commonDir === "") throw new Error(`the repository ${path} is not a git working tree`);
  const common = place.paths.isAbsolute(commonDir) ? commonDir : place.paths.join(root, commonDir);
  if ((await gitOptional(place, root, ["rev-parse", "--verify", "--quiet", "HEAD"])) === null) {
    throw new Error(`the repository ${path} has no commit to start a tree from; make an initial commit first`);
  }
  return { root, common };
}

/** A base branch as git knows it. */
export interface BaseRef {
  /** The full name of the ref: a local branch, a remote-tracking branch, or the name the spec gave when it already is a ref. */
  ref: string;
  /** The commit the ref holds, as it stands there. Nothing is fetched. */
  commit: string;
}

/**
 * What the base branch of a tree and its task branch stand at, in one look.
 * The base is the first of the local branch, the remote-tracking branch, and
 * the name itself when it already is a ref, that exists. `taskBranch` is null
 * when the task branch was not asked for or does not exist yet.
 */
export async function refsOf(
  place: Place,
  dir: string,
  base: string,
  taskBranch?: string,
): Promise<{ base: BaseRef | null; taskBranch: string | null }> {
  const candidates = [`refs/heads/${base}`, `refs/remotes/${base}`];
  if (base.startsWith("refs/")) candidates.push(base);
  const wanted = taskBranch === undefined ? null : `refs/heads/${taskBranch}`;
  // A pattern also matches every ref below it, so each line is matched by its full name.
  const out = await gitOut(place, dir, [
    "for-each-ref",
    "--format=%(refname)%00%(objectname)%00%(*objectname)",
    ...candidates,
    ...(wanted === null ? [] : [wanted]),
  ]);
  const commits = new Map<string, string>();
  for (const line of out.split("\n")) {
    const [name = "", object = "", peeled = ""] = line.split("\0");
    if (name === "") continue;
    commits.set(name, peeled || object);
  }
  const ref = candidates.find((candidate) => commits.has(candidate));
  const branch = wanted === null ? undefined : commits.get(wanted);
  return {
    base: ref === undefined ? null : { ref, commit: commits.get(ref)! },
    taskBranch: branch === undefined ? null : branch,
  };
}

/** One worktree a repository has registered. */
export interface WorktreeEntry {
  path: string;
  /** "refs/heads/<name>", or "" when HEAD is detached. */
  branch: string;
  main: boolean;
}

export async function listWorktrees(place: Place, repoPath: string): Promise<WorktreeEntry[]> {
  const out = await gitOut(place, repoPath, ["worktree", "list", "--porcelain"]);
  const entries: WorktreeEntry[] = [];
  for (const line of out.split("\n")) {
    if (line.startsWith("worktree ")) {
      entries.push({ path: line.slice("worktree ".length), branch: "", main: entries.length === 0 });
    } else if (line.startsWith("branch ") && entries.length > 0) {
      entries[entries.length - 1]!.branch = line.slice("branch ".length);
    }
  }
  return entries;
}

/**
 * The chain of links above each path, resolved as far as it stands: what a
 * path is once it exists, which is how git will name it. One round trip.
 */
export interface ResolvedChain {
  /** Each path as given, with the links that exist resolved and the rest kept as written. */
  realish: string[];
  /** Every folder on the way down to each path, resolved; null where it does not stand. */
  found: Map<string, string | null>;
}

export async function resolveChain(place: Place, paths: string[]): Promise<ResolvedChain> {
  const rules = place.paths;
  const ancestors = new Set<string>();
  for (const path of paths) {
    for (let dir = path; ; dir = rules.dirname(dir)) {
      ancestors.add(dir);
      if (rules.dirname(dir) === dir) break;
    }
  }
  const order = [...ancestors];
  const resolved = await place.resolve(order);
  const found = new Map(order.map((dir, index): [string, string | null] => [dir, resolved[index] ?? null]));
  const realish = paths.map((path) => {
    const tail: string[] = [];
    for (let dir = path; ; dir = rules.dirname(dir)) {
      const target = found.get(dir);
      if (target) return rules.join(target, ...tail.reverse());
      tail.push(rules.basename(dir));
      if (rules.dirname(dir) === dir) return rules.normalize(path);
    }
  });
  return { realish, found };
}

/** What git says about the working tree at one path, with every path resolved. */
export interface TreeInfo {
  /** The working tree's private git dir. */
  gitDir: string;
  common: string;
  /** The working tree's root as git reports it. */
  top: string;
  /** The path itself, resolved. Equal to `top` when the path is the root of that working tree. */
  self: string;
  /** The checked-out branch, or "" when HEAD is detached. */
  branch: string;
  detached: boolean;
  /** Why the path is not a working tree at all; "" when it is one. */
  problem: string;
}

const NO_TREE: TreeInfo = {
  gitDir: "",
  common: "",
  top: "",
  self: "",
  branch: "",
  detached: false,
  problem: "",
};

export async function probe(place: Place, path: string): Promise<TreeInfo> {
  let lines: string[];
  try {
    lines = (
      await gitOut(place, path, ["rev-parse", "--show-toplevel", "--absolute-git-dir", "--git-common-dir"])
    ).split("\n");
  } catch (error) {
    if (error instanceof PlaceError) throw error;
    return { ...NO_TREE, self: path, problem: `not a git working tree (${detailOf(error)})` };
  }
  const [top = "", gitDir = "", commonDir = ""] = lines.map((line) => line.trim());
  if (top === "" || gitDir === "" || commonDir === "") {
    return { ...NO_TREE, self: path, problem: "git did not describe the working tree" };
  }
  const common = place.paths.isAbsolute(commonDir) ? commonDir : place.paths.join(top, commonDir);
  const [realGitDir, realCommon, realTop, self] = await place.resolve([gitDir, common, top, path]);
  const info: TreeInfo = {
    gitDir: realGitDir ?? gitDir,
    common: realCommon ?? common,
    top: realTop ?? top,
    self: self ?? place.paths.normalize(path),
    branch: "",
    detached: false,
    problem: "",
  };
  const head = await gitOptional(place, path, ["symbolic-ref", "--quiet", "HEAD"]);
  if (head === null) info.detached = true;
  else info.branch = head.replace(/^refs\/heads\//, "");
  return info;
}
