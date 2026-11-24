"use client";

import { useEffect, useMemo } from "react";
import { ApiClient } from "../api/client";
import { installFreezeWatchdog } from "../diagnostics/freeze-watchdog";
import { setApiInstance, setSchemaLogger } from "../api";
import { createAuthStore, registerAuthStore } from "../auth";
import { createChatStore, registerChatStore } from "../chat";
import {
  I18nProvider,
  LocaleAdapterProvider,
  UserLocaleSync,
} from "../i18n/react";
import { WSProvider } from "../realtime";
import { QueryProvider } from "../provider";
import { createLogger } from "../logger";
import { defaultStorage } from "./storage";
import { SystemUserInitializer } from "./system-user-initializer";
import type { CoreProviderProps, ClientIdentity } from "./types";
import type { StorageAdapter } from "../types/storage";
import { ClientUsageReporter } from "../client-usage";
import {
  configureShortcutPlatform,
  configureShortcutRuntime,
} from "../shortcuts/platform";

// Module-level singletons — created once at first render, never recreated.
// Vite HMR preserves module-level state, so these survive hot reloads.
let initialized = false;
let authStore: ReturnType<typeof createAuthStore>;
let chatStore: ReturnType<typeof createChatStore>;
interface InitCoreOptions {
  apiBaseUrl: string;
  storage: StorageAdapter;
  identity?: ClientIdentity;
}

function initCore({
  apiBaseUrl,
  storage,
  identity,
}: InitCoreOptions) {
  if (initialized) return;

  configureShortcutPlatform(
    identity?.os === "macos" ||
      identity?.os === "windows" ||
      identity?.os === "linux" ||
      identity?.os === "unknown"
      ? identity.os
      : null,
  );
  // Authoritative override; before this runs (module-eval store hydration)
  // detectShortcutRuntime() reads the preload globals and already agrees.
  configureShortcutRuntime(
    identity?.platform === "desktop" ? "desktop" : null,
  );

  const api = new ApiClient(apiBaseUrl, {
    logger: createLogger("api"),
    identity,
  });
  setApiInstance(api);
  setSchemaLogger(createLogger("api-schema"));

  // Workspace identity is URL-driven: the [workspaceSlug] layout resolves
  // the slug and calls setCurrentWorkspace(slug, wsId) on mount. The api
  // client reads the slug from that singleton for the X-Workspace-Slug
  // header. No boot-time hydration from storage is required.

  authStore = createAuthStore({
    api,
    storage,
  });
  registerAuthStore(authStore);

  chatStore = createChatStore({ storage });
  registerChatStore(chatStore);
  initialized = true;
}

export function CoreProvider({
  children,
  apiBaseUrl = "",
  wsUrl = "ws://localhost:8080/ws",
  storage = defaultStorage,
  identity,
  locale,
  resources,
  localeAdapter,
  syncUserLocale = true,
}: CoreProviderProps) {
  // Initialize singletons on first render only. Dependencies are read-once:
  // apiBaseUrl, storage, and callbacks are set at app boot and never change at runtime.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useMemo(
    () =>
      initCore({
        apiBaseUrl,
        storage,
        identity,
      }),
    [],
  );

  // Client-only freeze watchdog — shared by web and desktop. No-op on the
  // server and idempotent, so mounting it here covers both apps in one place.
  useEffect(() => {
    installFreezeWatchdog();
  }, []);

  // I18nProvider wraps everything else: server and client must use the same
  // (locale, resources) to avoid hydration mismatch. Language switching goes
  // through window.location.reload(), never client-side changeLanguage.
  const tree = (
    <QueryProvider>
      <SystemUserInitializer identity={identity}>
        {/* Desktop's reporter owns both activity and runtime state so it must
            be the only writer for that installation. */}
        {identity?.platform !== "desktop" && (
          <ClientUsageReporter storage={storage} identity={identity} />
        )}
        <WSProvider
          wsUrl={wsUrl}
          authStore={authStore}
          identity={identity}
        >
          {children}
        </WSProvider>
      </SystemUserInitializer>
    </QueryProvider>
  );

  // UserLocaleSync requires a LocaleAdapter to persist; only mount it when
  // the host app provides one (web layout + desktop App both do).
  const withAdapter = localeAdapter ? (
    <LocaleAdapterProvider adapter={localeAdapter}>
      {syncUserLocale && <UserLocaleSync />}
      {tree}
    </LocaleAdapterProvider>
  ) : (
    tree
  );

  return (
    <I18nProvider locale={locale} resources={resources}>
      {withAdapter}
    </I18nProvider>
  );
}
