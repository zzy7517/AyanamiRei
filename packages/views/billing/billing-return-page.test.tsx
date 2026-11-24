import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enBilling from "../locales/en/billing.json";

const mockReplace = vi.hoisted(() => vi.fn());
const searchRef = vi.hoisted(() => ({ current: new URLSearchParams() }));
const authRef = vi.hoisted(() => ({
  current: { user: { id: "user-1" } as { id: string } | null, isLoading: false },
}));
const mockRefetchWorkspaces = vi.hoisted(() => vi.fn());
const workspacesRef = vi.hoisted(() => ({
  current: {
    data: [
      { id: "11111111-1111-1111-1111-111111111111", slug: "acme" },
      { id: "22222222-2222-2222-2222-222222222222", slug: "globex" },
    ] as { id: string; slug: string }[] | undefined,
    isPending: false,
    isError: false,
    refetch: mockRefetchWorkspaces,
  },
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => workspacesRef.current,
}));

vi.mock("@multica/core/workspace/queries", () => ({
  workspaceListOptions: () => ({ queryKey: ["workspaces"], queryFn: vi.fn() }),
}));

vi.mock("@multica/core/auth", () => {
  const useAuthStore = Object.assign(
    (selector?: (state: typeof authRef.current) => unknown) =>
      selector ? selector(authRef.current) : authRef.current,
    { getState: () => authRef.current },
  );
  return { useAuthStore };
});

vi.mock("../navigation", () => ({
  useNavigation: () => ({
    replace: mockReplace,
    searchParams: searchRef.current,
    hash: "",
    pathname: "/billing/return",
  }),
}));

import { BillingReturnPage } from "./billing-return-page";

function Wrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={{ en: { billing: enBilling } }}>
      {children}
    </I18nProvider>
  );
}

function renderReturn(search: string) {
  searchRef.current = new URLSearchParams(search);
  return render(<BillingReturnPage />, { wrapper: Wrapper });
}

/**
 * The one claim this page must never make is that anything was saved. `cancel`
 * means the user backed out, `portal` may have been opened and closed, and even
 * `success` only means Stripe finished — the subscription is not recorded until
 * the webhook lands.
 */
function expectNoOutcomeClaim() {
  const text = screen.getByRole("alert").textContent ?? "";
  expect(text).not.toMatch(/saved|success|upgraded|activated|subscribed/i);
}

describe("BillingReturnPage", () => {
  beforeEach(() => {
    authRef.current = { user: { id: "user-1" }, isLoading: false };
    workspacesRef.current = {
      data: [
        { id: "11111111-1111-1111-1111-111111111111", slug: "acme" },
        { id: "22222222-2222-2222-2222-222222222222", slug: "globex" },
      ],
      isPending: false,
      isError: false,
      refetch: mockRefetchWorkspaces,
    };
  });

  afterEach(() => {
    mockReplace.mockReset();
    mockRefetchWorkspaces.mockReset();
  });

  it("resolves the workspace id cloud authorized into its slug", async () => {
    renderReturn("workspace_id=22222222-2222-2222-2222-222222222222&result=success&session_id=cs_test_123");

    await waitFor(() =>
      expect(mockReplace).toHaveBeenCalledWith(
        "/globex/settings?tab=billing&result=success",
      ),
    );
  });

  it("forwards cancel and portal results and drops the Stripe session id", async () => {
    renderReturn("workspace_id=11111111-1111-1111-1111-111111111111&result=cancel&session_id=cs_test_9");
    await waitFor(() =>
      expect(mockReplace).toHaveBeenCalledWith(
        "/acme/settings?tab=billing&result=cancel",
      ),
    );

    mockReplace.mockReset();
    renderReturn("workspace_id=11111111-1111-1111-1111-111111111111&result=portal");
    await waitFor(() =>
      expect(mockReplace).toHaveBeenCalledWith(
        "/acme/settings?tab=billing&result=portal",
      ),
    );
  });

  it("forwards an add-on return with its Checkout Session so Billing can confirm it", async () => {
    renderReturn("workspace_id=11111111-1111-1111-1111-111111111111&result=autopilot_addon&session_id=cs_test_42");
    await waitFor(() =>
      expect(mockReplace).toHaveBeenCalledWith(
        "/acme/settings?tab=billing&result=autopilot_addon&session_id=cs_test_42",
      ),
    );

    // Anything but a Stripe session ID shape is dropped.
    mockReplace.mockReset();
    renderReturn("workspace_id=11111111-1111-1111-1111-111111111111&result=autopilot_addon&session_id=../x&y");
    await waitFor(() =>
      expect(mockReplace).toHaveBeenCalledWith(
        "/acme/settings?tab=billing&result=autopilot_addon",
      ),
    );

    mockReplace.mockReset();
    renderReturn("workspace_id=11111111-1111-1111-1111-111111111111&result=autopilot_addon_cancel&session_id=cs_test_42");
    await waitFor(() =>
      expect(mockReplace).toHaveBeenCalledWith(
        "/acme/settings?tab=billing&result=autopilot_addon_cancel",
      ),
    );
  });

  // Only `workspace_id` and `result` are read, and the destination is rebuilt
  // from paths — so a hand-crafted return link cannot redirect off-app.
  it("ignores an unrecognized result instead of forwarding it", async () => {
    renderReturn("workspace_id=11111111-1111-1111-1111-111111111111&result=https://evil.example/steal");

    await waitFor(() =>
      expect(mockReplace).toHaveBeenCalledWith("/acme/settings?tab=billing"),
    );
  });

  // Paying and then landing somewhere unexplained is worse than a short
  // message, so an unresolvable workspace is explained rather than redirected.
  it("explains an unresolvable workspace instead of silently redirecting", async () => {
    renderReturn("workspace_id=33333333-3333-3333-3333-333333333333&result=success");

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Could not find that workspace",
      ),
    );
    // The page cannot know the outcome, so it must not claim one.
    expectNoOutcomeClaim();
    expect(mockReplace).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Go to Multica" }));
    expect(mockReplace).toHaveBeenCalledWith("/");
  });

  it("explains a missing workspace id the same way", async () => {
    renderReturn("result=success");

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Could not find that workspace",
      ),
    );
    expectNoOutcomeClaim();
    expect(mockReplace).not.toHaveBeenCalled();
  });

  // The local install has no login page. A return that arrives before the
  // user record does still opens the workspace billing page, and Stripe's
  // session id stays off that URL.
  it("opens workspace billing when the user record is not loaded yet", async () => {
    authRef.current = { user: null, isLoading: false };
    renderReturn(
      "workspace_id=11111111-1111-1111-1111-111111111111&result=success&session_id=cs_test_1",
    );

    await waitFor(() => {
      expect(mockReplace).toHaveBeenCalledWith(
        "/acme/settings?tab=billing&result=success",
      );
    });
    expect(String(mockReplace.mock.calls)).not.toContain("/login");
    expect(String(mockReplace.mock.calls)).not.toContain("cs_test_1");
  });

  it("offers a retry instead of spinning forever when the workspace list fails", async () => {
    workspacesRef.current = {
      data: undefined,
      isPending: false,
      isError: true,
      refetch: mockRefetchWorkspaces,
    };
    renderReturn("workspace_id=11111111-1111-1111-1111-111111111111&result=success");

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Could not open workspace billing",
    );
    expectNoOutcomeClaim();
    expect(mockReplace).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(mockRefetchWorkspaces).toHaveBeenCalledTimes(1);
  });

  it("waits for auth and the workspace list before deciding", async () => {
    authRef.current = { user: null, isLoading: true };
    renderReturn("workspace_id=11111111-1111-1111-1111-111111111111&result=success");
    expect(mockReplace).not.toHaveBeenCalled();

    authRef.current = { user: { id: "user-1" }, isLoading: false };
    workspacesRef.current = {
      data: undefined,
      isPending: true,
      isError: false,
      refetch: mockRefetchWorkspaces,
    };
    renderReturn("workspace_id=11111111-1111-1111-1111-111111111111&result=success");
    expect(mockReplace).not.toHaveBeenCalled();
  });

  it("shows a polite status while redirecting", () => {
    renderReturn("workspace_id=11111111-1111-1111-1111-111111111111&result=success");

    expect(screen.getByRole("status")).toHaveTextContent(
      "Returning you to workspace billing",
    );
  });
});

describe("BillingReturnPage outcome-neutral failure copy", () => {
  beforeEach(() => {
    authRef.current = { user: { id: "user-1" }, isLoading: false };
    workspacesRef.current = {
      data: [{ id: "11111111-1111-1111-1111-111111111111", slug: "acme" }],
      isPending: false,
      isError: false,
      refetch: mockRefetchWorkspaces,
    };
  });

  afterEach(() => {
    mockReplace.mockReset();
    mockRefetchWorkspaces.mockReset();
  });

  // A canceled Checkout changed nothing, so the failure screen must not imply a
  // billing change happened.
  it.each([
    ["cancel", "result=cancel"],
    ["portal", "result=portal"],
    ["missing result", ""],
    ["invalid result", "result=not-a-result"],
  ])("claims no outcome for %s", async (_label, query) => {
    const search = ["workspace_id=99999999-9999-9999-9999-999999999999", query]
      .filter(Boolean)
      .join("&");
    renderReturn(search);

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Could not find that workspace",
      ),
    );
    expectNoOutcomeClaim();
  });

  it("claims no outcome when the workspace list itself fails", () => {
    workspacesRef.current = {
      data: undefined,
      isPending: false,
      isError: true,
      refetch: mockRefetchWorkspaces,
    };
    renderReturn("workspace_id=11111111-1111-1111-1111-111111111111&result=success");

    expectNoOutcomeClaim();
  });
});

describe("BillingReturnPage without a user record", () => {
  beforeEach(() => {
    authRef.current = { user: null, isLoading: false };
    workspacesRef.current = {
      data: [
        { id: "11111111-1111-1111-1111-111111111111", slug: "acme" },
      ],
      isPending: false,
      isError: false,
      refetch: mockRefetchWorkspaces,
    };
  });

  afterEach(() => {
    mockReplace.mockReset();
  });

  it("opens billing and drops unrelated return parameters", async () => {
    renderReturn(
      "workspace_id=11111111-1111-1111-1111-111111111111&result=success" +
        "&session_id=cs_test_secret&utm_source=stripe&anything=else",
    );

    await waitFor(() =>
      expect(mockReplace).toHaveBeenCalledWith(
        "/acme/settings?tab=billing&result=success",
      ),
    );
    const target = String(mockReplace.mock.calls);
    expect(target).not.toContain("/login");
    expect(target).not.toContain("session_id");
    expect(target).not.toContain("cs_test_secret");
    expect(target).not.toContain("utm_source");
    expect(target).not.toContain("anything");
  });

  it("explains a malformed workspace id instead of leaving for login", async () => {
    renderReturn("workspace_id=not-a-uuid&result=javascript:alert(1)&session_id=cs_x");

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Could not find that workspace",
      ),
    );
    expect(mockReplace).not.toHaveBeenCalled();
  });
});
