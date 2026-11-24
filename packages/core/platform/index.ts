export { CoreProvider } from "./core-provider";
export type { CoreProviderProps, ClientIdentity } from "./types";
export { SystemUserInitializer } from "./system-user-initializer";
export { defaultStorage } from "./storage";
export { createPersistStorage } from "./persist-storage";
export { createWorkspaceAwareStorage, setCurrentWorkspace, getCurrentSlug, getCurrentWsId, subscribeToCurrentSlug, registerForWorkspaceRehydration } from "./workspace-storage";
export { clearWorkspaceStorage } from "./storage-cleanup";
export { clearClientSessionData } from "./session-cleanup";
export {
  registerSystemNotificationClickHandler,
  isWebNotificationSupported,
  getWebNotificationPermission,
  requestWebNotificationPermission,
  showWebNotification,
  showSystemNotification,
  systemNotificationSuppressedByFocus,
  systemNotificationPath,
  type SystemNotificationPayload,
  type WebNotificationPermission,
} from "./system-notification";
