import { BrowserWindow, ipcMain, webContents } from "electron";
import type { IpcMainEvent, IpcMainInvokeEvent, WebContents } from "electron";
import {
  TERMINAL_ATTACH_CHANNEL,
  TERMINAL_DETACH_CHANNEL,
  TERMINAL_END_CHANNEL,
  TERMINAL_EVENT_CHANNEL,
  TERMINAL_OPEN_CHANNEL,
  TERMINAL_RESIZE_CHANNEL,
  TERMINAL_WRITE_CHANNEL,
  type TerminalHostAttachResult,
  type TerminalHostOpenResult,
  type TerminalHostTarget,
} from "../../shared/terminal";
import { AgentHooks } from "./agent-hooks";
import { dimensionsOf, TerminalHost, type TerminalHostOptions, type TerminalSender } from "./host";
import { nodePtyFactory } from "./pty";

/**
 * Owns the app's terminals and answers the renderer's requests about them.
 * `options` replace the pieces a test provides.
 */
export async function setupTerminal(
  options: Partial<TerminalHostOptions> = {},
): Promise<TerminalHost> {
  // Without the receiver no agent starts, but shells do.
  const hooks = await AgentHooks.listen().catch((error: unknown) => {
    console.warn("[terminal] the agent hook receiver did not start:", error);
    return null;
  });
  const host = new TerminalHost({
    spawn: nodePtyFactory,
    hooks,
    broadcast: (event) => {
      for (const contents of webContents.getAllWebContents()) {
        if (contents.getType() === "window" && !contents.isDestroyed()) {
          contents.send(TERMINAL_EVENT_CHANNEL, event);
        }
      }
    },
    log: (message) => console.warn(`[terminal] ${message}`),
    ...options,
  });

  ipcMain.handle(
    TERMINAL_OPEN_CHANNEL,
    (event: IpcMainInvokeEvent, target: unknown, size: unknown): Promise<TerminalHostOpenResult> => {
      const sender = senderOf(event.sender);
      const dimensions = dimensionsOf(size);
      return sender && isTarget(target) && dimensions
        ? host.open(target, dimensions, sender)
        : Promise.resolve(notATerminalRequest());
    },
  );

  ipcMain.handle(
    TERMINAL_ATTACH_CHANNEL,
    (event: IpcMainInvokeEvent, terminalId: unknown, size: unknown): Promise<TerminalHostAttachResult> => {
      const sender = senderOf(event.sender);
      const dimensions = dimensionsOf(size);
      return sender && typeof terminalId === "string" && dimensions
        ? host.attach(terminalId, dimensions, sender)
        : Promise.resolve(notATerminalRequest());
    },
  );

  ipcMain.on(TERMINAL_DETACH_CHANNEL, (event: IpcMainEvent, terminalId: unknown) => {
    const sender = senderOf(event.sender);
    if (sender && typeof terminalId === "string") host.detach(terminalId, sender);
  });

  ipcMain.on(TERMINAL_WRITE_CHANNEL, (event: IpcMainEvent, terminalId: unknown, data: unknown) => {
    const sender = senderOf(event.sender);
    if (sender && typeof data === "string") host.write(terminalId as string, sender, data);
  });

  ipcMain.on(TERMINAL_RESIZE_CHANNEL, (event: IpcMainEvent, terminalId: unknown, size: unknown) => {
    const sender = senderOf(event.sender);
    const dimensions = dimensionsOf(size);
    if (sender && dimensions) host.resize(terminalId as string, sender, dimensions);
  });

  ipcMain.on(TERMINAL_END_CHANNEL, (_event: IpcMainEvent, terminalId: unknown) => {
    if (typeof terminalId === "string") host.end(terminalId);
  });

  return host;
}

/** Forgets a renderer's terminals once it is gone, as a closed window's are. */
export function watchTerminalRenderer(host: TerminalHost, contents: WebContents): void {
  contents.once("destroyed", () => host.detachAll(contents.id));
}

/** Only a window of this app asks for terminals, and it hears while it exists. */
function senderOf(contents: WebContents): TerminalSender | null {
  if (!BrowserWindow.fromWebContents(contents)) return null;
  return {
    id: contents.id,
    send: (channel, payload) => {
      if (!contents.isDestroyed()) contents.send(channel, payload);
    },
  };
}

function notATerminalRequest(): { ok: false; code: "bad_request"; message: string } {
  return { ok: false, code: "bad_request", message: "not a request this app's terminal page makes" };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isPlace(value: unknown): boolean {
  if (!isRecord(value)) return false;
  if (value.kind === "local") return true;
  return value.kind === "ssh" && typeof value.host === "string" && value.host !== "";
}

function isTree(value: unknown): boolean {
  return (
    isRecord(value) &&
    ["path", "repoName", "branch", "baseBranch"].every((field) => typeof value[field] === "string")
  );
}

/** Whether the renderer asked for something a terminal can run. */
function isTarget(value: unknown): value is TerminalHostTarget {
  if (!isRecord(value)) return false;
  switch (value.kind) {
    case "defaultProject":
      return true;
    case "shell":
      return isPlace(value.place) && typeof value.cwd === "string";
    case "agent": {
      const task = value.task;
      return (
        isPlace(value.place) &&
        typeof value.cwd === "string" &&
        isRecord(task) &&
        ["id", "workspaceId", "identifier", "title", "description"].every(
          (field) => typeof task[field] === "string",
        ) &&
        Array.isArray(value.trees) &&
        value.trees.length > 0 &&
        value.trees.every(isTree)
      );
    }
    default:
      return false;
  }
}
