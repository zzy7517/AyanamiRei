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
} from "./host";
export { TERMINAL_DEFAULT_PROJECT, terminalHost } from "./host";
export type {
  TerminalErrorInfo,
  TerminalSessionIntent,
  TerminalSessionOptions,
  TerminalSessionState,
  TerminalStatus,
} from "./client";
export { TerminalSession, useTerminalSession, useTerminalSessionState } from "./client";
