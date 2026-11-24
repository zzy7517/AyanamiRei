import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  user: null as { id: string; onboarded_at: string | null } | null,
  isLoading: true,
  workspaces: [] as { id: string; slug: string }[],
  ready: true,
  replace: vi.fn(),
  push: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: state.replace, push: state.push }),
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (
    selector: (auth: {
      user: typeof state.user;
      isLoading: boolean;
    }) => unknown,
  ) => selector({ user: state.user, isLoading: state.isLoading }),
}));

vi.mock("@multica/core/paths", () => ({
  useHasOnboarded: () => state.user?.onboarded_at != null,
  resolvePostAuthDestination: (workspaces: { slug: string }[]) =>
    `/${workspaces[0]?.slug ?? "none"}/issues`,
  paths: {
    root: () => "/",
    workspace: (slug: string) => ({
      issues: () => `/${slug}/issues`,
      chatSession: () => "/",
      issueDetail: () => "/",
    }),
  },
}));

vi.mock("@multica/core/workspace", () => ({
  useWorkspaceList: () => ({ workspaces: state.workspaces, ready: state.ready }),
}));

vi.mock("@multica/views/onboarding", () => ({
  OnboardingFlow: () => <div data-testid="onboarding-flow" />,
  CliInstallInstructions: () => <div data-testid="cli-install-instructions" />,
}));

import OnboardingPage from "./page";

function renderPage() {
  const queryClient = new QueryClient();
  return render(
    <QueryClientProvider client={queryClient}>
      <OnboardingPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  state.user = null;
  state.isLoading = true;
  state.workspaces = [];
  state.ready = true;
  vi.clearAllMocks();
});

// Regression: the flow used to mount on the first render, before the user
// record had loaded, and threw "OnboardingFlow requires an authenticated
// user" on every direct visit.
describe("onboarding page", () => {
  it("renders nothing while the user record is loading", () => {
    renderPage();
    expect(screen.queryByTestId("onboarding-flow")).toBeNull();
    expect(state.replace).not.toHaveBeenCalled();
  });

  it("renders nothing when there is no user record", () => {
    state.isLoading = false;
    renderPage();
    expect(screen.queryByTestId("onboarding-flow")).toBeNull();
  });

  it("renders the flow once the user record has loaded", () => {
    state.isLoading = false;
    state.user = { id: "user-1", onboarded_at: null };
    renderPage();
    expect(screen.getByTestId("onboarding-flow")).toBeTruthy();
    expect(state.replace).not.toHaveBeenCalled();
  });

  it("leaves for the workspace instead of rendering the flow when the user is already onboarded", () => {
    state.isLoading = false;
    state.user = { id: "user-1", onboarded_at: "2026-01-01T00:00:00Z" };
    state.workspaces = [{ id: "ws-1", slug: "dev" }];
    renderPage();
    expect(screen.queryByTestId("onboarding-flow")).toBeNull();
    expect(state.replace).toHaveBeenCalledWith("/dev/issues");
  });
});
