import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  user: null as { id: string; onboarded_at: string | null } | null,
  isLoading: true,
  push: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: state.push }),
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
  paths: {
    root: () => "/",
    workspace: (slug: string) => ({
      issues: () => `/${slug}/issues`,
      chatSession: () => "/",
      issueDetail: () => "/",
    }),
  },
}));

vi.mock("@multica/core/workspace/queries", () => ({
  workspaceListOptions: () => ({
    queryKey: ["workspace-list"],
    queryFn: async () => [],
  }),
}));

vi.mock("@multica/views/onboarding", () => ({
  OnboardingFlow: ({ mode }: { mode?: string }) => (
    <div data-testid="onboarding-flow" data-mode={mode} />
  ),
  CliInstallInstructions: () => <div data-testid="cli-install-instructions" />,
}));

import Page from "./page";

function renderPage() {
  const queryClient = new QueryClient();
  return render(
    <QueryClientProvider client={queryClient}>
      <Page />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  state.user = null;
  state.isLoading = true;
  vi.clearAllMocks();
});

// Regression: the flow used to mount on the first render, before the user
// record had loaded, and threw "OnboardingFlow requires an authenticated
// user" on every direct visit.
describe("new workspace page", () => {
  it("renders nothing while the user record is loading", () => {
    renderPage();
    expect(screen.queryByTestId("onboarding-flow")).toBeNull();
  });

  it("renders nothing when there is no user record", () => {
    state.isLoading = false;
    renderPage();
    expect(screen.queryByTestId("onboarding-flow")).toBeNull();
  });

  it("renders the flow at the workspace step once the user record has loaded", () => {
    state.isLoading = false;
    state.user = { id: "user-1", onboarded_at: "2026-01-01T00:00:00Z" };
    renderPage();
    expect(screen.getByTestId("onboarding-flow").dataset.mode).toBe("new_workspace");
  });
});
