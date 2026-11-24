// @vitest-environment node
import { execFile } from "child_process";
import { mkdirSync, mkdtempSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { promisify } from "util";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import type { TerminalPlace } from "@multica/core/terminal/host";
import { TaskTreeHost } from "./host";
import { localPlace, sshPlace, type Place } from "./place";

const run = promisify(execFile);

/**
 * An SSH host to run everything on as well, for when a throwaway one is up:
 * `AYANAMI_TREE_SSH_HOST=<alias>`, with the alias in the ssh config the test
 * process uses. The host's home folder gets a `tree-lab` folder, and whatever
 * `ayanami-tasks` holds there is removed first.
 */
const SSH_HOST = process.env.AYANAMI_TREE_SSH_HOST;
if (SSH_HOST) vi.setConfig({ testTimeout: 120_000, hookTimeout: 120_000 });

/** A real repository on one place, in a home folder apart from the person's. */
interface Fixture {
  place: Place;
  /** A folder of the test's own on the place. */
  scratch: string;
  /** Where the host puts every task's folder. */
  tasks: string;
  project: string;
  git(dir: string, ...args: string[]): Promise<string>;
}

async function onThisComputer(): Promise<Fixture> {
  // The person's own git settings have no say in what is tested.
  process.env.GIT_CONFIG_GLOBAL = "/dev/null";
  process.env.GIT_CONFIG_NOSYSTEM = "1";
  const scratch = mkdtempSync(join(tmpdir(), "ayanami-trees-test-"));
  const home = join(scratch, "home");
  mkdirSync(home);
  return {
    place: { ...localPlace(), home: async () => home },
    scratch,
    tasks: join(home, "ayanami-tasks"),
    project: join(scratch, "code", "app"),
    git: async (dir, ...args) => {
      const { stdout } = await run("git", ["-C", dir, ...args], { encoding: "utf8" });
      return stdout.trim();
    },
  };
}

async function onSshHost(host: string): Promise<Fixture> {
  const place = sshPlace(host);
  const home = await place.home();
  const scratch = place.paths.join(home, "tree-lab");
  await place.removeRecursively(place.paths.join(home, "ayanami-tasks"));
  return {
    place,
    scratch,
    tasks: place.paths.join(home, "ayanami-tasks"),
    project: place.paths.join(scratch, "code", "app"),
    git: async (dir, ...args) => {
      const answer = await place.git(dir, args);
      if (answer.code !== 0) throw new Error(answer.stderr.trim());
      return answer.stdout.trim();
    },
  };
}

const variants: { name: string; make: () => Promise<Fixture> }[] = [
  { name: "on this computer", make: onThisComputer },
];
if (SSH_HOST) variants.push({ name: `on the SSH host ${SSH_HOST}`, make: () => onSshHost(SSH_HOST) });

const local: TerminalPlace = { kind: "local" };

async function exists(place: Place, path: string): Promise<boolean> {
  return (await place.stat([path])).every((entry) => entry !== null && entry !== undefined);
}

async function commit(f: Fixture, dir: string, message: string, file: string, text: string): Promise<string> {
  await f.place.write(f.place.paths.join(dir, file), `${text}\n`);
  await f.git(dir, "add", "-A");
  await f.git(dir, "commit", "-m", message);
  return f.git(dir, "rev-parse", "HEAD");
}

async function seed(f: Fixture): Promise<void> {
  const { project } = f;
  await f.place.mkdir(project);
  await f.git(project, "init", "-b", "main");
  await f.git(project, "config", "user.name", "Test");
  await f.git(project, "config", "user.email", "test@example.invalid");
  await f.git(project, "config", "commit.gpgsign", "false");
  await commit(f, project, "one", "one.txt", "one");
  await commit(f, project, "two", "two.txt", "two");
  await f.git(project, "branch", "dev");
}

const hostFor = (f: Fixture) => new TaskTreeHost({ placeFor: () => f.place });
const treeOf = (f: Fixture, key: string, name?: string) =>
  f.place.paths.join(f.tasks, key, ...(name === undefined ? [] : [name]));

describe.each(variants)("a task's trees $name", (variant) => {
  let f: Fixture;
  beforeAll(async () => {
    f = await variant.make();
    await seed(f);
  });
  afterAll(async () => {
    await f.place.removeRecursively(f.scratch).catch(() => undefined);
  });

  it("are cut from the latest commit of their base branch, and the project's own checkout is left alone", async () => {
    await f.place.write(f.place.paths.join(f.project, "draft.txt"), "not committed in the project\n");
    const specs = [{ repo: f.project, baseBranch: "main" }];
    const tree = treeOf(f, "MUL-1");

    const answer = await hostFor(f).ensure(local, "MUL-1", specs);
    expect(answer).toEqual({ ok: true, trees: [{ path: tree, branch: "task/MUL-1" }] });
    expect(await f.git(tree, "rev-parse", "--abbrev-ref", "HEAD")).toBe("task/MUL-1");
    expect(await f.git(tree, "rev-parse", "HEAD")).toBe(await f.git(f.project, "rev-parse", "main"));
    expect(await exists(f.place, f.place.paths.join(tree, "two.txt"))).toBe(true);
    expect(await exists(f.place, f.place.paths.join(tree, "draft.txt"))).toBe(false);
    expect(await f.git(tree, "status", "--porcelain")).toBe("");

    // Asking again changes nothing, in the tree or in the project.
    expect(await hostFor(f).ensure(local, "MUL-1", specs)).toEqual(answer);
    expect(await exists(f.place, f.place.paths.join(f.project, "draft.txt"))).toBe(true);
  });

  it("are checked out from their task branch again once the branch has work of its own", async () => {
    const specs = [{ repo: f.project, baseBranch: "main" }];
    const tree = treeOf(f, "MUL-1");
    const work = await commit(f, tree, "the agent's work", "work.txt", "done");
    const host = hostFor(f);

    // "Remove the tree": the branch and its work stay.
    expect(await host.remove(local, "MUL-1", { branch: "task/MUL-1" })).toEqual({
      ok: true,
      removed: [{ path: tree, branch: "task/MUL-1", autoCommitted: false, leftoverRef: "" }],
    });
    expect(await exists(f.place, tree)).toBe(false);
    expect(await f.git(f.project, "rev-parse", "task/MUL-1")).toBe(work);
    expect(await host.ensure(local, "MUL-1", specs)).toEqual({ ok: true, trees: [{ path: tree, branch: "task/MUL-1" }] });
    expect(await f.git(tree, "rev-parse", "HEAD")).toBe(work);

    // A tree folder deleted by hand, which leaves its registration behind, is made the same way.
    await f.place.removeRecursively(tree);
    expect(await host.ensure(local, "MUL-1", specs)).toEqual({ ok: true, trees: [{ path: tree, branch: "task/MUL-1" }] });
    expect(await exists(f.place, f.place.paths.join(tree, "work.txt"))).toBe(true);
  });

  it("of a task with several stand in subfolders named by project and branch", async () => {
    const answer = await hostFor(f).ensure(local, "MUL-2", [
      { repo: f.project, baseBranch: "main" },
      { repo: f.project, baseBranch: "dev" },
    ]);
    expect(answer).toEqual({
      ok: true,
      trees: [
        { path: treeOf(f, "MUL-2", "app--main"), branch: "task/MUL-2/app--main" },
        { path: treeOf(f, "MUL-2", "app--dev"), branch: "task/MUL-2/app--dev" },
      ],
    });
    expect(await exists(f.place, f.place.paths.join(treeOf(f, "MUL-2", "app--dev"), ".git"))).toBe(true);
  });

  it("keep a place for one that is only laid out, without making it", async () => {
    const answer = await hostFor(f).ensure(local, "MUL-3", [
      { repo: f.project, baseBranch: "main", build: false },
      { repo: f.project, baseBranch: "dev" },
    ]);
    expect(answer).toEqual({
      ok: true,
      trees: [
        { path: treeOf(f, "MUL-3", "app--main"), branch: "task/MUL-3/app--main" },
        { path: treeOf(f, "MUL-3", "app--dev"), branch: "task/MUL-3/app--dev" },
      ],
    });
    expect(await exists(f.place, treeOf(f, "MUL-3", "app--main"))).toBe(false);
    expect(await exists(f.place, f.place.paths.join(treeOf(f, "MUL-3", "app--dev"), ".git"))).toBe(true);
  });

  it("say why one could not be made, and leave nothing behind", async () => {
    const answer = await hostFor(f).ensure(local, "MUL-9", [{ repo: f.project, baseBranch: "nope" }]);
    expect(answer).toEqual({
      ok: false,
      error: `the repository "${f.project}" has no local or remote-tracking branch "nope"`,
    });
    expect(await exists(f.place, treeOf(f, "MUL-9"))).toBe(false);
  });
});

describe.each(variants)("removing a tree $name", (variant) => {
  let f: Fixture;
  beforeAll(async () => {
    f = await variant.make();
    await seed(f);
  });
  afterAll(async () => {
    await f.place.removeRecursively(f.scratch).catch(() => undefined);
  });

  it("commits what was left uncommitted when the task branch has no commit of its own", async () => {
    const branch = "task/MUL-3/app--dev";
    const tree = treeOf(f, "MUL-3", "app--dev");
    await hostFor(f).ensure(local, "MUL-3", [
      { repo: f.project, baseBranch: "main" },
      { repo: f.project, baseBranch: "dev" },
    ]);
    await f.place.write(f.place.paths.join(tree, "left.txt"), "left\n");

    expect(await hostFor(f).remove(local, "MUL-3", { branch })).toEqual({
      ok: true,
      removed: [{ path: tree, branch, autoCommitted: true, leftoverRef: "" }],
    });
    expect(await exists(f.place, tree)).toBe(false);
    expect(await f.git(f.project, "log", "-1", "--format=%s", branch)).toBe(
      `chore(task): uncommitted changes from ${branch}`,
    );
    expect(await f.git(f.project, "show", "--name-only", "--format=", branch)).toBe("left.txt");
  });

  it("keeps what was left uncommitted off a task branch that has commits of its own", async () => {
    const branch = "task/MUL-2/app--dev";
    const tree = treeOf(f, "MUL-2", "app--dev");
    await hostFor(f).ensure(local, "MUL-2", [
      { repo: f.project, baseBranch: "main" },
      { repo: f.project, baseBranch: "dev" },
    ]);
    const tip = await commit(f, tree, "the agent's work", "work.txt", "done");
    await f.place.write(f.place.paths.join(tree, "left.txt"), "left\n");

    const kept = `refs/ayanami/leftovers/${branch}/MUL-2`;
    expect(await hostFor(f).remove(local, "MUL-2", { branch })).toEqual({
      ok: true,
      removed: [{ path: tree, branch, autoCommitted: false, leftoverRef: kept }],
    });
    expect(await f.git(f.project, "rev-parse", branch)).toBe(tip);
    expect(await f.git(f.project, "rev-parse", `${kept}^`)).toBe(tip);
    expect(await f.git(f.project, "log", "-1", "--format=%s", kept)).toBe("ayanami: changes the agent left uncommitted");
    expect(await f.git(f.project, "show", "--name-only", "--format=", kept)).toBe("left.txt");
  });

  it("keeps a tree with an unresolved merge, and says why", async () => {
    const branch = "task/MUL-4/app--main";
    const tree = treeOf(f, "MUL-4", "app--main");
    await hostFor(f).ensure(local, "MUL-4", [
      { repo: f.project, baseBranch: "main" },
      { repo: f.project, baseBranch: "dev" },
    ]);
    await commit(f, tree, "this side", "one.txt", "from main");
    await commit(f, treeOf(f, "MUL-4", "app--dev"), "that side", "one.txt", "from dev");
    await f.git(tree, "merge", "task/MUL-4/app--dev").catch(() => undefined);

    const answer = await hostFor(f).remove(local, "MUL-4", { branch });
    expect(answer).toEqual({ ok: false, error: expect.stringContaining("one.txt are still unmerged") });
    expect(await exists(f.place, f.place.paths.join(tree, ".git"))).toBe(true);
  });

  it("removes the task folder once it holds nothing", async () => {
    await hostFor(f).ensure(local, "MUL-1", [{ repo: f.project, baseBranch: "main" }]);
    // The one tree is the task folder.
    expect(await hostFor(f).remove(local, "MUL-1", { branch: "task/MUL-1" })).toMatchObject({ ok: true });
    expect(await exists(f.place, treeOf(f, "MUL-1"))).toBe(false);
    expect(await exists(f.place, f.tasks)).toBe(true);

    // With several, the folder goes once the last tree is gone.
    await hostFor(f).ensure(local, "MUL-5", [
      { repo: f.project, baseBranch: "main" },
      { repo: f.project, baseBranch: "dev" },
    ]);
    await hostFor(f).remove(local, "MUL-5", { branch: "task/MUL-5/app--main" });
    expect(await exists(f.place, treeOf(f, "MUL-5", "app--dev"))).toBe(true);
    await hostFor(f).remove(local, "MUL-5", { branch: "task/MUL-5/app--dev" });
    expect(await exists(f.place, treeOf(f, "MUL-5"))).toBe(false);
  });
});
