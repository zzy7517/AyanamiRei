import { BrowserWindow, ipcMain } from "electron";
import type { IpcMainInvokeEvent } from "electron";
import type { TerminalPlace } from "@multica/core/terminal/host";
import {
  TREES_ENSURE_CHANNEL,
  TREES_REMOVE_CHANNEL,
  type TreeEnsureResult,
  type TreeRemoveResult,
  type TreeSpec,
} from "../../shared/tasktrees";
import { TaskTreeHost, type TaskTreeHostOptions } from "./host";

/**
 * Answers the renderer's requests about tasks' trees, and keeps what answers
 * them. `options` replace the pieces a test provides.
 */
export function setupTaskTrees(options: TaskTreeHostOptions = {}): TaskTreeHost {
  const host = new TaskTreeHost({ log: (message) => console.warn(`[task-trees] ${message}`), ...options });

  ipcMain.handle(
    TREES_ENSURE_CHANNEL,
    (event: IpcMainInvokeEvent, place: unknown, taskKey: unknown, specs: unknown): Promise<TreeEnsureResult> => {
      const sender = BrowserWindow.fromWebContents(event.sender);
      return sender && isPlace(place) && typeof taskKey === "string" && isSpecs(specs)
        ? host.ensure(place, taskKey, specs)
        : Promise.resolve(notATreeRequest());
    },
  );

  ipcMain.handle(
    TREES_REMOVE_CHANNEL,
    (event: IpcMainInvokeEvent, place: unknown, taskKey: unknown, tree: unknown): Promise<TreeRemoveResult> => {
      const sender = BrowserWindow.fromWebContents(event.sender);
      const branch = isRecord(tree) ? tree.branch : undefined;
      return sender && isPlace(place) && typeof taskKey === "string" && typeof branch === "string"
        ? host.remove(place, taskKey, { branch })
        : Promise.resolve(notATreeRequest());
    },
  );

  return host;
}

function notATreeRequest(): { ok: false; error: string } {
  return { ok: false, error: "not a request this app's pages make" };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isPlace(value: unknown): value is TerminalPlace {
  if (!isRecord(value)) return false;
  if (value.kind === "local") return true;
  return value.kind === "ssh" && typeof value.host === "string" && value.host !== "";
}

function isSpecs(value: unknown): value is TreeSpec[] {
  return (
    Array.isArray(value) &&
    value.length > 0 &&
    value.every(
      (spec) =>
        isRecord(spec) &&
        typeof spec.repo === "string" &&
        typeof spec.baseBranch === "string" &&
        (spec.build === undefined || typeof spec.build === "boolean"),
    )
  );
}
