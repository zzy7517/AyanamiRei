import type { TerminalPlace } from "@multica/core/terminal/host";
import type { BuiltTree, TreeEnsureResult, TreeRemoveResult, TreeSpec } from "../../shared/tasktrees";
import { createTrees } from "./create";
import { layout, sanitize, validComponent, type Tree } from "./layout";
import { localPlace, sshPlace, type Place } from "./place";
import { removeTree } from "./remove";

/** The folder, in the home folder on each place, that holds every task's folder. */
const TASK_TREE_ROOT_FOLDER = "ayanami-tasks";

export interface TaskTreeHostOptions {
  /** The place a terminal's place names. A test provides its own. */
  placeFor?: (place: TerminalPlace) => Place;
  log?: (message: string) => void;
}

type Answer<T> = { ok: true; value: T } | { ok: false; error: string };

/**
 * Builds and removes the trees of tasks where their projects' folders are:
 * on this computer, or on an SSH host. The app's pages ask through the main
 * process; nothing here talks to a server.
 *
 * One task on one place is seen to at a time, in the order asked.
 */
export class TaskTreeHost {
  private readonly placeFor: (place: TerminalPlace) => Place;
  private readonly log: (message: string) => void;
  /** What the last request for a task on a place waits for. */
  private readonly tails = new Map<string, Promise<void>>();
  /** The task folder root of each place, read once. */
  private readonly roots = new Map<string, Promise<string>>();
  /** The trees this run of the app found standing, with everything checked. */
  private readonly checked = new Set<string>();

  constructor(options: TaskTreeHostOptions = {}) {
    this.placeFor = options.placeFor ?? ((place: TerminalPlace) => (place.kind === "ssh" ? sshPlace(place.host) : localPlace()));
    this.log = options.log ?? (() => undefined);
  }

  /**
   * Makes sure every tree of `specs` exists in the task's folder on `place`,
   * in the order of `specs`, and says where each stands.
   */
  ensure(place: TerminalPlace, taskKey: string, specs: TreeSpec[]): Promise<TreeEnsureResult> {
    return this.answer(
      place,
      taskKey,
      async (where, key) => {
        const plan = layout(where.paths, await this.root(where), key, specs);
        const made = plan.trees.filter((tree) => tree.build);
        const standing = await where.stat(made.map((tree) => where.paths.join(tree.path, ".git")));
        // A tree checked earlier in this run of the app and still standing
        // needs no second look: opening a terminal asks every time.
        if (
          made.length > 0 &&
          made.every((tree, index) => standing[index] && this.checked.has(this.id(where, tree.path)))
        ) {
          return plan.trees.map(built);
        }
        await createTrees(where, plan);
        for (const tree of made) this.checked.add(this.id(where, tree.path));
        return plan.trees.map(built);
      },
    ).then((answer) =>
      answer.ok ? { ok: true as const, trees: answer.value } : { ok: false as const, error: answer.error },
    );
  }

  /** Removes the tree of the task that is on `tree.branch` from `place`, the delivery rules applied. */
  remove(place: TerminalPlace, taskKey: string, tree: { branch: string }): Promise<TreeRemoveResult> {
    return this.answer(
      place,
      taskKey,
      async (where, key) => {
        const removed = await removeTree(where, await this.root(where), key, tree.branch);
        for (const removal of removed) this.checked.delete(this.id(where, removal.path));
        return removed;
      },
    ).then((answer) =>
      answer.ok ? { ok: true as const, removed: answer.value } : { ok: false as const, error: answer.error },
    );
  }

  private root(where: Place): Promise<string> {
    let root = this.roots.get(where.name);
    if (!root) {
      root = where.home().then((home) => where.paths.join(home, TASK_TREE_ROOT_FOLDER));
      this.roots.set(where.name, root);
      // A home folder that could not be read is asked for again next time.
      root.catch(() => this.roots.delete(where.name));
    }
    return root;
  }

  private id(where: Place, path: string): string {
    return `${where.name}\n${path}`;
  }

  /** Runs `work` for one task on one place after that task's last request, and never rejects. */
  private async answer<T>(
    place: TerminalPlace,
    taskKey: string,
    work: (where: Place, key: string) => Promise<T>,
  ): Promise<Answer<T>> {
    const key = sanitize(taskKey);
    if (!validComponent(key)) return { ok: false, error: `${JSON.stringify(taskKey)} is not a usable task key` };
    let where: Place;
    try {
      where = this.placeFor(place);
    } catch (error) {
      return { ok: false, error: describe(error) };
    }
    const task = `${where.name}\n${key}`;
    const previous = this.tails.get(task) ?? Promise.resolve();
    const run = previous.then(async () => {
      try {
        return { ok: true as const, value: await work(where, key) };
      } catch (error) {
        this.log(`${key} on ${where.name}: ${describe(error)}`);
        return { ok: false as const, error: describe(error) };
      }
    });
    const tail = run.then(
      () => undefined,
      () => undefined,
    );
    this.tails.set(task, tail);
    void tail.then(() => {
      if (this.tails.get(task) === tail) this.tails.delete(task);
    });
    return run;
  }
}

function built(tree: Tree): BuiltTree {
  return { path: tree.path, branch: tree.branch };
}

/** A failure in words for the person at the screen. */
function describe(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}
