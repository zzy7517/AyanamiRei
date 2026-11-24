import { useEffect, useLayoutEffect, useMemo, useRef } from "react";
import { CoreProvider } from "@multica/core/platform";
import { pickLocale, type SupportedLocale } from "@multica/core/i18n";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceList } from "@multica/core/workspace";
import { useHasOnboarded } from "@multica/core/paths";
import { setCurrentWorkspace } from "@multica/core/platform";
import { ThemeProvider } from "@multica/ui/components/common/theme-provider";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { Toaster } from "@multica/ui/components/ui/sonner";
import { DesktopShell } from "./components/desktop-layout";
import { UpdateNotification } from "./components/update-notification";
import { NativeContextMenu } from "./components/native-context-menu";
import { IssueWindow } from "./components/issue-window";
import { DesktopDevToolbar } from "./components/desktop-dev-toolbar";
import {
  DESKTOP_EMERGENCY_COPY,
  resolveDesktopEmergencyLocale,
} from "./components/emergency-copy";
import { useTabStore } from "./stores/tab-store";
import { useWindowOverlayStore } from "./stores/window-overlay-store";
import { useOpenSettingsShortcut } from "./hooks/use-open-settings-shortcut";
import { useTabSelectionShortcut } from "./hooks/use-tab-selection-shortcut";
import { useDaemonIPCBridge } from "./platform/daemon-ipc-bridge";
import { startDaemonOnLaunch } from "./platform/daemon-start-sync";
import { createDesktopLocaleAdapter } from "./platform/i18n-adapter";
import { captureEvent } from "@multica/core/analytics";
import { AppearanceSync } from "@multica/views/common/appearance-sync";
import { RESOURCES } from "@multica/views/locales";
import { DesktopClientUsageReporter } from "./platform/client-usage-reporter";
import { DiagnosticRouteReporter } from "./platform/diagnostic-route-reporter";
import { flushFreezeBreadcrumb } from "./freeze-flush";
import { WorkspaceWindowBridge } from "./platform/workspace-window-bridge";
import { IssueDeepLinkBridge } from "./platform/issue-deep-link-bridge";
import {
  launchWorkspaceSlug,
  otherWindowWorkspaces,
  resolveWindowWorkspace,
} from "./platform/workspace-windows";

// BCP-47 region tags for the <html lang> attribute. index.html ships a static lang="en";
// we sync it to the resolved locale at boot so screen readers announce the
// right language AND the Japanese-scoped CJK font override in globals.css
// (`html[lang|="ja"]`) can take effect.
const HTML_LANG: Record<SupportedLocale, string> = {
  en: "en",
  "zh-Hans": "zh-CN",
  "zh-Hant": "zh-TW",
  ko: "ko-KR",
  ja: "ja-JP",
  fr: "fr-FR",
  es: "es",
  ru: "ru",
};


/**
 * Cmd/Ctrl+W: close the active tab. When the last real tab is closed
 * (or no tabs/workspace exist), close the window.
 *
 * Mounted at the App root so every renderer state — including loading,
 * onboarding, and runtime-config errors — has a working Cmd+W
 * handler. Without this, states outside the tab shell would swallow the
 * shortcut and do nothing.
 */
function useCmdWCloseTab() {
  useEffect(() => {
    return window.desktopAPI.onCloseActiveTab(() => {
      if (window.desktopAPI.windowContext?.kind === "issue") {
        window.desktopAPI.closeWindow();
        return;
      }
      const store = useTabStore.getState();
      const { activeWorkspaceSlug, byWorkspace } = store;
      if (!activeWorkspaceSlug) {
        // No workspace — nothing to close, dismiss the window.
        window.desktopAPI.closeWindow();
        return;
      }
      const group = byWorkspace[activeWorkspaceSlug];
      if (!group || group.tabs.length <= 1) {
        // Last tab (or no tabs) — close the window.
        window.desktopAPI.closeWindow();
        return;
      }
      // Multiple tabs — close the active one.
      store.closeActiveTab();
    });
  }, []);
}

function IssueWindowContent() {
  const isLoading = useAuthStore((state) => state.isLoading);
  const context = window.desktopAPI.windowContext ?? { kind: "main" as const };

  if (context.kind !== "issue") return null;
  if (isLoading) {
    return (
      <div className="flex h-screen items-center justify-center">
        <MulticaIcon mode="think" size={40} />
      </div>
    );
  }

  return (
    <IssueWindow context={context} />
  );
}

function AppContent() {
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);
  const runtimeConfig = window.desktopAPI.runtimeConfig.ok
    ? window.desktopAPI.runtimeConfig.config
    : null;

  // Tell the main process which backend URL we talk to, so daemon-manager
  // can pick the matching CLI profile (server_url from ~/.multica config).
  useEffect(() => {
    if (!runtimeConfig) return;
    window.daemonAPI.setTargetApiUrl(runtimeConfig.apiUrl);
  }, [runtimeConfig]);

  // Start the daemon once the user record is known. The ordering inside
  // startDaemonOnLaunch is load-bearing — see that module.
  useEffect(() => {
    if (!user || !runtimeConfig) return;
    startDaemonOnLaunch(window.daemonAPI, runtimeConfig.apiUrl).catch((err) => {
      console.error("Failed to start the daemon", err);
    });
  }, [user, runtimeConfig]);

  // When a session that started with zero workspaces creates the first one,
  // restart the daemon so it picks the workspace up immediately rather than
  // waiting for the next workspace-sync tick.
  const {
    workspaces,
    ready: workspaceListReady,
  } = useWorkspaceList();
  const wsCount = workspaces.length;
  const hasOnboarded = useHasOnboarded();

  // Bridge local daemon IPC status into the runtimes cache so this user's
  // own daemon flips to offline/online sub-second instead of waiting on the
  // server's 75s sweeper. Resolves wsId from the active tab so workspace
  // switches automatically rebind the subscription.
  const activeWorkspaceSlug = useTabStore((s) => s.activeWorkspaceSlug);
  const activeWsId = activeWorkspaceSlug
    ? workspaces.find((w) => w.slug === activeWorkspaceSlug)?.id
    : undefined;
  useDaemonIPCBridge(activeWsId);

  // Pre-workspace overlay routing for desktop. Mirrors the web layout
  // hard gate via overlays (desktop has no URL bar, so we open the
  // onboarding overlay instead of router.replace):
  //   onboarded + has workspace      → no overlay, dashboard
  //   un-onboarded (any wsCount)     → /onboarding overlay
  //   onboarded + no workspace       → /workspaces/new overlay
  //
  // When the user record is known, `onboarded_at != null` is the path into
  // the dashboard. CreateWorkspace does not mark onboarded; only Step 3's
  // CompleteOnboarding flips the flag. A user who somehow has a workspace
  // but no onboarded mark must be sent back to /onboarding — we also clear
  // the active workspace so the dashboard doesn't render under the overlay
  // with stale workspace context.
  useEffect(() => {
    if (isLoading || !workspaceListReady) return;
    const { overlay, open } = useWindowOverlayStore.getState();
    if (overlay) return;
    // No user record yet: enter the workspace list. Onboarding needs the
    // user record, and there is no login surface to send them to.
    if (!user) {
      if (wsCount === 0) open({ type: "new-workspace" });
      return;
    }
    if (hasOnboarded && wsCount > 0) return;
    if (!hasOnboarded) {
      // Stale workspace context (if any) would leak X-Workspace-Slug
      // headers into onboarding-time API calls. Clear it before opening
      // the overlay.
      setCurrentWorkspace(null, null);
      open({ type: "onboarding" });
      return;
    }
    open({ type: "new-workspace" });
  }, [user, isLoading, workspaceListReady, wsCount, hasOnboarded]);


  // Validate persisted tab state against the current user's workspace list,
  // and pick an active workspace if none is set. Runs in useLayoutEffect
  // (synchronously after render, before paint) rather than the render
  // phase — the original render-phase pattern triggered React's
  // "Cannot update a component while rendering a different component"
  // warning because `switchWorkspace` is a Zustand setState that the
  // TabBar is subscribed to. useLayoutEffect flushes both renders before
  // the user sees anything, so there's no visible flicker.
  //
  // Gate on authoritative data: pending and initial errors expose no data,
  // while a failed background refetch retains the last successful list.
  useLayoutEffect(() => {
    if (!workspaceListReady) return;
    const slugs = workspaces.map((w) => w.slug);
    useTabStore.getState().validateWorkspaceSlugs(new Set(slugs));
    const { activeWorkspaceSlug, switchWorkspace } = useTabStore.getState();
    if (slugs.length === 0) return;
    const target = resolveWindowWorkspace(
      slugs,
      activeWorkspaceSlug,
      launchWorkspaceSlug(),
      otherWindowWorkspaces(),
    );
    if (target && target !== activeWorkspaceSlug) switchWorkspace(target);
  }, [workspaces, workspaceListReady]);

  // null = undecided (user record or list hasn't settled yet)
  // true  = session started with zero workspaces; next transition to >=1 triggers restart
  // false = session started with >=1 workspace, OR we've already restarted; skip
  const sessionStartedEmptyRef = useRef<boolean | null>(null);
  useEffect(() => {
    if (isLoading || !workspaceListReady) return;
    if (sessionStartedEmptyRef.current === null) {
      sessionStartedEmptyRef.current = wsCount === 0;
      return;
    }
    if (sessionStartedEmptyRef.current && wsCount >= 1) {
      void window.daemonAPI.restart();
      sessionStartedEmptyRef.current = false;
    }
  }, [isLoading, workspaceListReady, wsCount]);

  if (isLoading) {
    return (
      <div className="flex h-screen items-center justify-center">
        <MulticaIcon mode="think" size={40} />
      </div>
    );
  }

  return <DesktopShell />;
}

function BlockingRuntimeConfigError({
  locale,
  message,
}: {
  locale: SupportedLocale;
  message: string;
}) {
  const copy = DESKTOP_EMERGENCY_COPY[resolveDesktopEmergencyLocale(locale)];
  return (
    <div className="flex h-screen items-center justify-center bg-background p-8 text-foreground">
      <div className="max-w-xl rounded-lg border bg-card p-6 shadow-sm">
        <h1 className="text-title font-semibold">{copy.configTitle}</h1>
        <p className="mt-3 text-body text-muted-foreground">
          {copy.configDescriptionBefore}
          <code>~/.multica/desktop.json</code>
          {copy.configDescriptionAfter}
        </p>
        <pre className="mt-4 whitespace-pre-wrap rounded-md bg-muted p-3 text-caption text-muted-foreground">
          {message}
        </pre>
      </div>
    </div>
  );
}

export default function App() {
  const { version, os } = window.desktopAPI.appInfo;
  const systemLocale = window.desktopAPI.systemLocale;
  const runtimeConfigResult = window.desktopAPI.runtimeConfig;
  // The fallback keeps renderer HMR safe while a main/preload rebuild is
  // restarting Electron; packaged builds always expose windowContext.
  const windowContext =
    window.desktopAPI.windowContext ?? { kind: "main" as const };
  useCmdWCloseTab();
  // Mounted at the App root for the same reason as Cmd+W: the chord has to
  // work in every renderer state, not only inside the tab shell.
  useOpenSettingsShortcut();
  // Fixed browser-style tab selection is also owned by main so it remains
  // available while focus sits inside editors, inputs, menus, or dialogs.
  useTabSelectionShortcut();

  // Flush a freeze/crash breadcrumb the main process parked from a previous
  // session. A true hang or process death can't report itself when it happens
  // (the renderer is blocked or gone), so the main process persists it and we
  // emit it here on the next boot. The in-thread, recoverable freeze tier is
  // handled separately by the shared watchdog in CoreProvider.
  useEffect(
    () =>
      flushFreezeBreadcrumb({
        getLastFreeze: () => window.desktopAPI.getLastFreeze(),
        ackFreeze: (ts) => window.desktopAPI.ackFreeze(ts),
        capture: captureEvent,
      }),
    [],
  );

  // Stable identity reference so downstream effects (WS reconnect) don't
  // tear down on every parent render.
  const identity = useMemo(
    () => ({ platform: "desktop", version, os }),
    [version, os],
  );
  // Locale resolution happens once at app boot. Switching language goes
  // through window.location.reload() to avoid hydration mismatch.
  const localeAdapter = useMemo(
    () => createDesktopLocaleAdapter(systemLocale),
    [systemLocale],
  );
  const locale = useMemo(() => pickLocale(localeAdapter), [localeAdapter]);
  const resources = useMemo(
    () => ({ [locale]: RESOURCES[locale] }),
    [locale],
  );

  // Keep <html lang> in sync with the resolved locale (index.html hardcodes
  // "en"). Drives the lang-scoped Japanese CJK font override and a11y.
  // useLayoutEffect (not useEffect) so lang is committed before the first
  // paint — otherwise Japanese users would see one frame of Kanji rendered
  // with the Chinese-first fallback stack before the override kicks in.
  useLayoutEffect(() => {
    document.documentElement.lang = HTML_LANG[locale];
    window.desktopAPI.setLocale?.(locale);
  }, [locale]);

  // React to OS-level language changes detected by main on focus regain.
  // Only act when the user is following the system signal (no explicit
  // Settings choice) — otherwise their preference wins. Cross-device sync
  // for the explicit-choice case is handled inside CoreProvider.
  useEffect(() => {
    return window.desktopAPI.onSystemLocaleChanged((nextSystemLocale) => {
      if (localeAdapter.getUserChoice()) return;
      const next = pickLocale({
        ...localeAdapter,
        getSystemPreferences: () =>
          nextSystemLocale ? [nextSystemLocale] : [],
      });
      if (next === locale) return;
      localeAdapter.persist(next);
      window.location.reload();
    });
  }, [localeAdapter, locale]);

  return (
    <ThemeProvider>
      <AppearanceSync />
      {runtimeConfigResult.ok ? (
        <CoreProvider
          apiBaseUrl={runtimeConfigResult.config.apiUrl}
          wsUrl={runtimeConfigResult.config.wsUrl}
          identity={identity}
          locale={locale}
          resources={resources}
          localeAdapter={localeAdapter}
        >
          <NativeContextMenu />
          {windowContext.kind === "main" && <DiagnosticRouteReporter />}
          {windowContext.kind === "main" && <WorkspaceWindowBridge />}
          {windowContext.kind === "main" && <IssueDeepLinkBridge />}
          {windowContext.kind === "main" && (
            <DesktopClientUsageReporter
              apiUrl={runtimeConfigResult.config.apiUrl}
            />
          )}
          {windowContext.kind === "issue" ? (
            <IssueWindowContent />
          ) : (
            <AppContent />
          )}
          {/* Opt-in dev status bar; see "Dev Toolbar" in CONTRIBUTING.md. */}
          {import.meta.env.DEV &&
            import.meta.env.VITE_DEV_TOOLBAR &&
            windowContext.kind === "main" && (
              <DesktopDevToolbar apiUrl={runtimeConfigResult.config.apiUrl} />
            )}
        </CoreProvider>
      ) : (
        <BlockingRuntimeConfigError
          locale={locale}
          message={runtimeConfigResult.error.message}
        />
      )}
      <Toaster />
      {windowContext.kind === "main" && <UpdateNotification />}
    </ThemeProvider>
  );
}
