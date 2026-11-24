export type {
  AgentTerminalTask,
  AgentTerminalTree,
  TerminalErrorCode,
  TerminalExit,
  TerminalHostAttachResult,
  TerminalHostBridge,
  TerminalHostEvent,
  TerminalHostOpenResult,
  TerminalHostTarget,
  TerminalPlace,
  TerminalSize,
} from "@multica/core/terminal/host";

/** Main to renderer: what a terminal printed, how it ended, a finished agent turn. */
export const TERMINAL_EVENT_CHANNEL = "terminal:event";

export const TERMINAL_OPEN_CHANNEL = "terminal:open";
export const TERMINAL_ATTACH_CHANNEL = "terminal:attach";
export const TERMINAL_DETACH_CHANNEL = "terminal:detach";
export const TERMINAL_WRITE_CHANNEL = "terminal:write";
export const TERMINAL_RESIZE_CHANNEL = "terminal:resize";
export const TERMINAL_END_CHANNEL = "terminal:end";
