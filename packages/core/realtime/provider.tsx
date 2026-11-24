"use client";

import {
  createContext,
  use,
  useEffect,
  useMemo,
  useState,
  useCallback,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { WSClient, type WSConnectionStatus } from "../api/ws-client";
import type { WSEventType } from "../types";
import type { ClientIdentity } from "../platform/types";
import type { StoreApi, UseBoundStore } from "zustand";
import type { AuthState } from "../auth/store";
import {
  getCurrentSlug,
  getCurrentWsId,
  subscribeToCurrentSlug,
} from "../platform/workspace-storage";
import { useLocalSearchIndexSync } from "../search-index/hooks";
import { useGitHubConnectionStore } from "../github/connection-store";
import { createLogger } from "../logger";
import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

type EventHandler = (payload: unknown, actorId?: string, actorType?: string) => void;

/** `offline` means no connection is wanted: signed out or outside a workspace. */
export type WSStatus = WSConnectionStatus | "offline";

interface WSContextValue {
  /** WebSocket URL of the live doc co-editing service. */
  collabUrl: string;
  subscribe: (event: WSEventType, handler: EventHandler) => () => void;
  onReconnect: (callback: () => void) => () => void;
  subscribeStatus: (listener: () => void) => () => void;
  getStatus: () => WSStatus;
}

const WSContext = createContext<WSContextValue | null>(null);

/** The co-editing service is served next to realtime: `…/ws` → `…/collab`. */
export function collabUrlFromWsUrl(wsUrl: string): string {
  const url = new URL(wsUrl, globalThis.location?.href ?? "http://localhost");
  if (url.protocol === "http:") url.protocol = "ws:";
  if (url.protocol === "https:") url.protocol = "wss:";
  url.pathname = url.pathname.replace(/\/ws\/?$/, "") + "/collab";
  url.search = "";
  return url.toString();
}

export interface WSProviderProps {
  children: ReactNode;
  /** WebSocket server URL (e.g. "ws://localhost:8080/ws") */
  wsUrl: string;
  /** Platform-created auth store instance */
  authStore: UseBoundStore<StoreApi<AuthState>>;
  /** Identifies the WS client to the server (sent as query params on the upgrade URL). */
  identity?: ClientIdentity;
  /** Optional callback for showing toast messages (platform-specific, e.g. sonner) */
  onToast?: (message: string, type?: "info" | "error") => void;
}

export function WSProvider({
  children,
  wsUrl,
  authStore,
  identity,
  onToast,
}: WSProviderProps) {
  const user = authStore((s) => s.user);
  // Reactive read of the current workspace slug (URL-driven singleton in
  // packages/core/platform/workspace-storage.ts). When the workspace switches,
  // the useEffect below tears down the old WS connection and opens a new one
  // bound to the new workspace slug. SSR snapshot is `null` because this
  // provider only renders client-side under CoreProvider.
  const wsSlug = useSyncExternalStore(
    subscribeToCurrentSlug,
    getCurrentSlug,
    () => null,
  );
  const wsId = useSyncExternalStore(subscribeToCurrentSlug, getCurrentWsId, () => null);
  const [wsClient, setWsClient] = useState<WSClient | null>(null);

  useEffect(() => () => useGitHubConnectionStore.getState().reset(), [user?.id]);

  // Depend on identity primitives instead of the object reference so a parent
  // re-render that passes a new `{ platform, version, os }` literal does not
  // tear down and reconnect the WS when nothing about the identity actually
  // changed.
  const identityPlatform = identity?.platform;
  const identityVersion = identity?.version;
  const identityOS = identity?.os;

  useEffect(() => {
    if (!user || !wsSlug) return;

    const ws = new WSClient(wsUrl, {
      logger: createLogger("ws"),
      identity:
        identityPlatform || identityVersion || identityOS
          ? {
              platform: identityPlatform,
              version: identityVersion,
              os: identityOS,
            }
          : undefined,
    });
    ws.setAuth(wsSlug);
    setWsClient(ws);
    ws.connect();

    return () => {
      ws.disconnect();
      setWsClient(null);
    };
  }, [
    user,
    wsSlug,
    wsUrl,
    identityPlatform,
    identityVersion,
    identityOS,
  ]);

  const stores: RealtimeSyncStores = { authStore };

  // Centralized WS -> store sync (uses state so it re-subscribes when WS changes)
  useRealtimeSync(wsClient, stores, onToast);

  // Keeps this workspace's local search index attached and caught up.
  useLocalSearchIndexSync(wsClient, user?.id ?? null, wsId, wsSlug);

  const subscribe = useCallback(
    (event: WSEventType, handler: EventHandler) => {
      if (!wsClient) return () => {};
      return wsClient.on(event, handler);
    },
    [wsClient],
  );

  const onReconnectCb = useCallback(
    (callback: () => void) => {
      if (!wsClient) return () => {};
      return wsClient.onReconnect(callback);
    },
    [wsClient],
  );

  const subscribeStatus = useCallback(
    (listener: () => void) => {
      if (!wsClient) return () => {};
      return wsClient.onStatusChange(listener);
    },
    [wsClient],
  );

  const getStatus = useCallback(
    (): WSStatus => wsClient?.getStatus() ?? "offline",
    [wsClient],
  );

  const collabUrl = useMemo(() => collabUrlFromWsUrl(wsUrl), [wsUrl]);

  return (
    <WSContext.Provider
      value={{ collabUrl, subscribe, onReconnect: onReconnectCb, subscribeStatus, getStatus }}
    >
      {children}
    </WSContext.Provider>
  );
}

export function useWS() {
  const ctx = use(WSContext);
  if (!ctx) throw new Error("useWS must be used within WSProvider");
  return ctx;
}
