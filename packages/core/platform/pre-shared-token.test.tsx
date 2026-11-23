/**
 * @vitest-environment jsdom
 */
import { render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import { ApiClient } from "../api/client";
import { WSClient } from "../api/ws-client";
import { createAuthStore, registerAuthStore, useAuthStore } from "../auth";
import { configStore } from "../config";
import { __clearDraftCleanupRegistryForTest } from "../drafts/cleanup-registry";
import type { StorageAdapter, User } from "../types";
import { AuthInitializer } from "./auth-initializer";
import { withPreSharedToken } from "./pre-shared-token";

vi.mock("../analytics", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../analytics")>();
  return {
    ...actual,
    captureSignupSource: vi.fn(),
    identify: vi.fn(),
    initAnalytics: vi.fn(),
    resetAnalytics: vi.fn(),
  };
});

const TOKEN = "open-sesame-not-a-jwt";
const me = { id: "user-1", email: "owner@example.com" } as User;

function makeStorage(initial: Record<string, string> = {}): StorageAdapter {
  const values = { ...initial };
  return {
    getItem: (key) => values[key] ?? null,
    setItem: (key, value) => {
      values[key] = value;
    },
    removeItem: (key) => {
      delete values[key];
    },
    keys: () => Object.keys(values),
  };
}

// Only the upgrade and the first frame matter here: the token is delivered as
// the first message, never in the URL.
class FakeWebSocket {
  static lastInstance: FakeWebSocket | null = null;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  readyState = 0;
  sent: string[] = [];
  constructor() {
    FakeWebSocket.lastInstance = this;
  }
  send(frame: string) {
    this.sent.push(frame);
  }
  close() {}
}

function respond(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** Answers `/api/me` with `meResponse` and everything else benignly. */
function stubServer(meResponse: () => Response) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("/api/me")) return Promise.resolve(meResponse());
    if (url.endsWith("/api/config")) return Promise.resolve(respond({}));
    return Promise.resolve(respond([]));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function authorizationOf(call: unknown[] | undefined): string | undefined {
  const headers = (call?.[1] as RequestInit | undefined)?.headers;
  return (headers as Record<string, string> | undefined)?.Authorization;
}

/** The credential reader CoreProvider hands the API and WebSocket clients. */
function readCredential(storage: StorageAdapter): () => string | null {
  return () => storage.getItem("multica_token");
}

/**
 * Boots the way CoreProvider does in token mode: one API client that reads the
 * credential through storage, and whose 401 ends the session in the auth store.
 * The child stands in for the app's choice between the workspace and the login
 * page.
 */
function boot(base: StorageAdapter) {
  const storage = withPreSharedToken(base, TOKEN);
  const api = new ApiClient("https://api.example.test", {
    getToken: readCredential(storage),
    onUnauthorized: () => useAuthStore.getState().sessionExpired(),
  });
  setApiInstance(api);
  registerAuthStore(createAuthStore({ api, storage }));
  api.setToken(storage.getItem("multica_token"));
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity } },
  });

  function Screen() {
    const status = useAuthStore((state) => state.status);
    return (
      <div data-testid="screen">
        {status === "authenticated"
          ? "workspace"
          : status === "unauthenticated"
            ? "login"
            : "loading"}
      </div>
    );
  }

  const screen = render(
    <QueryClientProvider client={queryClient}>
      <AuthInitializer storage={storage} identity={{ platform: "desktop" }}>
        <Screen />
      </AuthInitializer>
    </QueryClientProvider>,
  );
  return { storage, api, screen };
}

beforeEach(() => {
  configStore.setState({
    daemonConfigStatus: "loading",
    daemonSetupTarget: "unknown",
    daemonServerUrl: "",
    daemonAppUrl: "",
  });
  __clearDraftCleanupRegistryForTest();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("withPreSharedToken", () => {
  it("presents the token as the session credential without storing it", () => {
    const base = makeStorage({ other: "kept" });
    const storage = withPreSharedToken(base, TOKEN);

    expect(storage.getItem("multica_token")).toBe(TOKEN);
    expect(storage.state()).toBe("presented");
    expect(storage.keys?.()).toEqual(["other", "multica_token"]);
    // Everything else is the base's own, and the base never sees the token.
    expect(storage.getItem("other")).toBe("kept");
    expect(base.getItem("multica_token")).toBeNull();
    expect(base.keys?.()).toEqual(["other"]);
  });

  it("stops presenting the token once the session is dropped", () => {
    const base = makeStorage();
    const storage = withPreSharedToken(base, TOKEN);

    storage.removeItem("multica_token");

    expect(storage.state()).toBe("dropped");
    expect(storage.getItem("multica_token")).toBeNull();
    expect(storage.keys?.()).toEqual([]);
  });

  it("hands the session to a credential the app stores itself", () => {
    const base = makeStorage();
    const storage = withPreSharedToken(base, TOKEN);

    storage.setItem("multica_token", "a-session-jwt");
    expect(storage.state()).toBe("replaced");
    // Ending that later session is not about the token.
    storage.removeItem("multica_token");
    expect(storage.state()).toBe("replaced");
    expect(storage.getItem("multica_token")).toBeNull();
  });

  it("stays without a key listing when the storage it wraps has none", () => {
    const base: StorageAdapter = {
      getItem: () => null,
      setItem: () => {},
      removeItem: () => {},
    };
    const storage = withPreSharedToken(base, TOKEN);
    expect(storage.keys).toBeUndefined();
    expect(storage.getItem("multica_token")).toBe(TOKEN);
  });
});

describe("a client wired with a pre-shared token", () => {
  it("sends the token as the credential of API and WebSocket requests", async () => {
    const fetchMock = stubServer(() => respond(me));
    vi.stubGlobal("WebSocket", FakeWebSocket as unknown as typeof WebSocket);
    const storage = withPreSharedToken(makeStorage(), TOKEN);
    const api = new ApiClient("https://api.example.test", {
      getToken: readCredential(storage),
    });
    const ws = new WSClient("wss://api.example.test/ws", {
      getToken: readCredential(storage),
    });

    await api.getMe();
    expect(authorizationOf(fetchMock.mock.calls[0])).toBe(`Bearer ${TOKEN}`);

    ws.setAuth(storage.getItem("multica_token"), "acme");
    ws.connect();
    FakeWebSocket.lastInstance!.onopen!();
    expect(JSON.parse(FakeWebSocket.lastInstance!.sent[0]!)).toEqual({
      type: "auth",
      payload: { token: TOKEN },
    });
  });

  it("opens past the login page when the server accepts it", async () => {
    const fetchMock = stubServer(() => respond(me));
    const base = makeStorage();
    const { storage, screen } = boot(base);

    await waitFor(() => {
      expect(useAuthStore.getState().status).toBe("authenticated");
    });
    expect(authorizationOf(fetchMock.mock.calls[0])).toBe(`Bearer ${TOKEN}`);
    expect(useAuthStore.getState().user?.id).toBe("user-1");
    expect(screen.getByTestId("screen").textContent).toBe("workspace");
    // Accepted, so still the credential in use — and still only in memory.
    expect(storage.state()).toBe("presented");
    expect(base.getItem("multica_token")).toBeNull();
  });

  it("falls back to the login page when the server rejects it, leaving nothing to replay", async () => {
    const fetchMock = stubServer(() =>
      respond({ error: "invalid token" }, 401),
    );
    const base = makeStorage();
    const { storage, api, screen } = boot(base);

    await waitFor(() => {
      expect(useAuthStore.getState().status).toBe("unauthenticated");
    });
    const auth = useAuthStore.getState();
    expect(auth.user).toBeNull();
    // The server ended a session it was asked to accept; that is what the
    // login page's notice is keyed on.
    expect(auth.expired).toBe(true);
    expect(screen.getByTestId("screen").textContent).toBe("login");

    // Dropped, not replaced: the app that wires this can tell the token was
    // the credential refused, and it is gone from every read.
    expect(storage.state()).toBe("dropped");
    expect(storage.getItem("multica_token")).toBeNull();
    expect(base.getItem("multica_token")).toBeNull();
    await api.listWorkspaces().catch(() => {});
    expect(authorizationOf(fetchMock.mock.calls.at(-1))).toBeUndefined();
  });
});
