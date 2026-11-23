import type { ReactNode } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { StorageAdapter } from "@multica/core/types";
import { defaultStorage } from "@multica/core/platform";

// The storage App hands CoreProvider: the one place the session credential is
// read from, so what it holds is what the app presents.
const wiring = vi.hoisted(() => ({
  storage: undefined as StorageAdapter | undefined,
}));

const state = vi.hoisted(() => ({
  auth: {
    isLoading: false,
    retryAuthentication: vi.fn(),
    status: "authenticated",
    user: { id: "user-1" } as { id: string } | null,
    expired: false,
  },
}));

const tabStore = vi.hoisted(() => ({
  activeWorkspaceSlug: "acme" as string | null,
  byWorkspace: {},
  closeActiveTab: vi.fn(),
  reset: vi.fn(),
  switchWorkspace: vi.fn(),
  validateWorkspaceSlugs: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ setQueryData: vi.fn() }),
}));

vi.mock("@multica/core/platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/platform")>();
  return {
    ...actual,
    CoreProvider: ({
      children,
      storage,
    }: {
      children: ReactNode;
      storage?: StorageAdapter;
    }) => {
      wiring.storage = storage;
      return <>{children}</>;
    },
    setCurrentWorkspace: vi.fn(),
  };
});

vi.mock("@multica/core/i18n", () => ({
  pickLocale: () => "en",
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (auth: typeof state.auth) => unknown) =>
    selector(state.auth),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  workspaceKeys: { list: () => ["workspace-list"] },
}));

vi.mock("@multica/core/workspace", () => ({
  useWorkspaceList: () => ({
    isFetching: false,
    ready: true,
    refetch: vi.fn(),
    unavailable: false,
    workspaces: [{ id: "ws-1", slug: "acme" }],
  }),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listMyInvitations: vi.fn(),
    listWorkspaces: vi.fn(),
  },
}));

vi.mock("@multica/core/paths", () => ({
  useHasOnboarded: () => true,
}));

vi.mock("@multica/core/analytics", () => ({ captureEvent: vi.fn() }));
vi.mock("@multica/ui/components/common/theme-provider", () => ({
  ThemeProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("@multica/ui/components/common/multica-icon", () => ({
  MulticaIcon: () => <div data-testid="app-loading" />,
}));
vi.mock("@multica/ui/components/ui/sonner", () => ({ Toaster: () => null }));
vi.mock("@multica/views/locales", () => ({ RESOURCES: { en: {} } }));

vi.mock("./pages/login", () => ({
  DesktopLoginPage: ({
    preSharedTokenRejected = false,
  }: {
    preSharedTokenRejected?: boolean;
  }) => (
    <div
      data-testid="login-page"
      data-token-rejected={preSharedTokenRejected ? "true" : "false"}
    />
  ),
}));
vi.mock("./pages/auth-recovery", () => ({
  DesktopAuthRecoveryPage: () => <div data-testid="auth-recovery" />,
}));
vi.mock("./components/desktop-layout", () => ({
  DesktopShell: () => <div data-testid="desktop-shell" />,
}));
vi.mock("./components/update-notification", () => ({
  UpdateNotification: () => null,
}));
vi.mock("./components/issue-window", () => ({
  IssueWindow: () => <div data-testid="issue-window" />,
}));

vi.mock("./stores/tab-store", () => ({
  useTabStore: Object.assign(
    (selector: (value: typeof tabStore) => unknown) => selector(tabStore),
    { getState: () => tabStore },
  ),
}));
vi.mock("./stores/window-overlay-store", () => ({
  useWindowOverlayStore: {
    getState: () => ({ close: vi.fn(), open: vi.fn(), overlay: null }),
  },
}));

vi.mock("./hooks/use-open-settings-shortcut", () => ({
  useOpenSettingsShortcut: () => {},
}));
vi.mock("./hooks/use-tab-selection-shortcut", () => ({
  useTabSelectionShortcut: () => {},
}));
vi.mock("./platform/daemon-ipc-bridge", () => ({
  useDaemonIPCBridge: () => {},
}));
const { syncDaemonOnLogin } = vi.hoisted(() => ({
  syncDaemonOnLogin: vi.fn(),
}));
vi.mock("./platform/daemon-login-sync", () => ({ syncDaemonOnLogin }));
vi.mock("./platform/i18n-adapter", () => ({
  createDesktopLocaleAdapter: () => ({
    getSystemPreferences: () => ["en"],
    getUserChoice: () => null,
    persist: vi.fn(),
  }),
}));
vi.mock("./platform/client-usage-reporter", () => ({
  DesktopClientUsageReporter: () => null,
}));
vi.mock("./platform/diagnostic-route-reporter", () => ({
  DiagnosticRouteReporter: () => null,
}));
vi.mock("./freeze-flush", () => ({ flushFreezeBreadcrumb: vi.fn() }));
vi.mock("./platform/auth-session-bridge", () => ({
  DesktopAuthSessionBridge: () => null,
}));
vi.mock("./platform/session-teardown", () => ({
  tearDownOnLogout: vi.fn(),
  tearDownOnSessionExpiry: vi.fn(),
}));

const { default: App } = await import("./App");

const TOKEN = "open-sesame-not-a-jwt";

function signedInAs(authToken?: string) {
  Object.assign(window, {
    daemonAPI: {
      clearToken: vi.fn(),
      restart: vi.fn(),
      setTargetApiUrl: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn(),
    },
    desktopAPI: {
      ackFreeze: vi.fn(),
      appInfo: { os: "macos", version: "0.5.1" },
      closeWindow: vi.fn(),
      getLastFreeze: vi.fn(),
      onAuthToken: () => () => {},
      onCloseActiveTab: () => () => {},
      onContextMenu: () => () => {},
      onInviteOpen: () => () => {},
      onIssueOpen: () => () => {},
      onSystemLocaleChanged: () => () => {},
      reportAuthSession: vi.fn(),
      runtimeConfig: {
        config: { apiUrl: "http://localhost", wsUrl: "ws://localhost", authToken },
        ok: true,
      },
      systemLocale: "en",
      windowContext: { kind: "main" },
    },
  });
}

/** Ends the session the way the auth store does, then lets the app render. */
function endSession(storage: StorageAdapter, { expired }: { expired: boolean }) {
  act(() => {
    storage.removeItem("multica_token");
    state.auth.status = "unauthenticated";
    state.auth.user = null;
    state.auth.expired = expired;
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  state.auth.isLoading = false;
  state.auth.status = "authenticated";
  state.auth.user = { id: "user-1" };
  state.auth.expired = false;
});

describe("App with a pre-shared token in desktop.json", () => {
  it("presents it as the session credential without storing it, and gives it to the daemon sync", async () => {
    signedInAs(TOKEN);

    render(<App />);

    const storage = wiring.storage!;
    expect(storage.getItem("multica_token")).toBe(TOKEN);
    expect(localStorage.getItem("multica_token")).toBeNull();
    expect(screen.getByTestId("desktop-shell")).toBeInTheDocument();
    await waitForDaemonSync();
    expect(syncDaemonOnLogin).toHaveBeenCalledWith(
      window.daemonAPI,
      "http://localhost",
      TOKEN,
      "user-1",
    );
  });

  it("reads the credential from localStorage as before when the file has no token", async () => {
    signedInAs();
    localStorage.setItem("multica_token", "jwt-abc");

    render(<App />);

    // No overlay: CoreProvider gets the same localStorage-backed storage as
    // when App passed none.
    expect(wiring.storage).toBe(defaultStorage);
    await waitForDaemonSync();
    expect(syncDaemonOnLogin).toHaveBeenCalledWith(
      window.daemonAPI,
      "http://localhost",
      "jwt-abc",
      "user-1",
    );
  });

  it("tells the login page the token was refused when the server rejects it", () => {
    signedInAs(TOKEN);

    const app = render(<App />);
    const storage = wiring.storage!;
    endSession(storage, { expired: true });
    app.rerender(<App />);

    expect(screen.queryByTestId("desktop-shell")).toBeNull();
    expect(screen.getByTestId("login-page")).toHaveAttribute(
      "data-token-rejected",
      "true",
    );
  });

  it("does not blame the token when the user signed out, or a later sign-in ended", () => {
    signedInAs(TOKEN);

    const app = render(<App />);
    const storage = wiring.storage!;
    endSession(storage, { expired: false });
    app.rerender(<App />);
    expect(screen.getByTestId("login-page")).toHaveAttribute(
      "data-token-rejected",
      "false",
    );

    // The user signs in with a code; that session is what ends later.
    act(() => storage.setItem("multica_token", "a-session-jwt"));
    endSession(storage, { expired: true });
    app.rerender(<App />);
    expect(screen.getByTestId("login-page")).toHaveAttribute(
      "data-token-rejected",
      "false",
    );
  });
});

function waitForDaemonSync() {
  return waitFor(() => expect(syncDaemonOnLogin).toHaveBeenCalled());
}
