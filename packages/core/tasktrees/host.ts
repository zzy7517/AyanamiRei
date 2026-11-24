/**
 * The contract between the app's pages and the process that builds a task's
 * trees: the desktop app's main process, reached through
 * `window.desktopAPI.trees`. A client without that bridge builds none.
 */

import type { TerminalPlace } from "../terminal/host";

/**
 * One tree of a task, as the app is asked to make sure of it: the folder of
 * the project it belongs to, on the place it is asked for, and the branch the
 * tree starts from.
 */
export interface TreeSpec {
  repo: string;
  baseBranch: string;
  /**
   * False for a tree the server says is deleted. It still takes its place in
   * the layout, which depends on how many trees the task has there, but is
   * not made.
   */
  build?: boolean;
}

/** A tree the app made sure of. */
export interface BuiltTree {
  /** The tree's folder, on the place it was asked for. */
  path: string;
  /** The task branch the tree works on. */
  branch: string;
}

export type TreeEnsureResult =
  | { ok: true; trees: BuiltTree[] }
  /** Why no tree of the task was made. Nothing is left half made. */
  | { ok: false; error: string };

/** What removing a tree did with the work it held. The task branch stays. */
export interface TreeRemoval {
  path: string;
  branch: string;
  /** Uncommitted work was committed to the task branch, which had no commit of its own. */
  autoCommitted: boolean;
  /** The hidden ref that holds uncommitted work kept off the task branch, which had commits of its own. */
  leftoverRef: string;
}

export type TreeRemoveResult =
  /** Empty when nothing of the tree was left to remove. */
  | { ok: true; removed: TreeRemoval[] }
  /** Why the tree was kept, exactly as it was. */
  | { ok: false; error: string };

export interface TreeHostBridge {
  /**
   * Makes sure every tree of `specs` exists in the task's directory on
   * `place`, in the order of `specs`, and says where. Safe to repeat: a tree
   * that is already there is left alone, one whose task branch survives is
   * checked out from that branch again, and one that never was is cut from
   * the latest commit of its base branch.
   */
  ensure(place: TerminalPlace, taskKey: string, specs: TreeSpec[]): Promise<TreeEnsureResult>;
  /**
   * Removes the tree on `branch` of the task from `place`, after the delivery
   * rules were applied to whatever was left uncommitted in it.
   */
  remove(place: TerminalPlace, taskKey: string, tree: { branch: string }): Promise<TreeRemoveResult>;
}

/** The builder of this client's task trees, or null where there is none. */
export function treeHost(): TreeHostBridge | null {
  if (typeof window === "undefined") return null;
  const desktopAPI = (window as unknown as { desktopAPI?: { trees?: unknown } }).desktopAPI;
  const bridge = desktopAPI?.trees;
  return typeof bridge === "object" && bridge !== null ? (bridge as TreeHostBridge) : null;
}

/**
 * The key a task's directory and branches are named by: its identifier with
 * every character a name cannot hold replaced. It is what the server names a
 * task by in its list of tree work.
 */
export function taskTreeKey(identifier: string): string {
  return identifier.replace(/[^A-Za-z0-9._-]/gu, "-");
}

/** Tells places apart: this computer, or one host. */
export function placeKey(place: TerminalPlace): string {
  return place.kind === "ssh" ? `ssh:${place.host}` : "local";
}

/**
 * Why a tree could not be made or was kept. Its message is for the person at
 * the screen: it says what stands in the way and what to do about it.
 */
export class TreeFailure extends Error {
  constructor(message: string) {
    super(message);
    this.name = "TreeFailure";
  }
}
