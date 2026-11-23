import { cleanup, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../test/i18n";
import { IssuesPage } from "../issues/components/issues-page";

const state = vi.hoisted(() => ({ refreshing: false, user: { id: "user-1" } as { id: string } | null }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (select: (s: typeof state) => unknown) => select(state),
}));
vi.mock("@multica/core/issues/stores/issues-scope-store", () => ({
  useIssuesScope: () => "all",
}));
vi.mock("../issues/surface/issue-surface", () => ({
  IssueSurface: ({ renderHeader }: { renderHeader: (context: object) => React.ReactNode }) => (
    <>{renderHeader({ controller: { isRefreshing: state.refreshing, surfaceIssues: [] } })}</>
  ),
}));
vi.mock("../chat/use-start-chat-with-draft", () => ({
  useStartChatWithDraft: () => null,
}));
vi.mock("../issue-views/view-header", () => ({
  IssueViewHeader: ({ controller, base }: { controller: { isRefreshing: boolean }; base: { kind: string } }) => (
    <header>
      <h1>{base.kind}</h1>
      {controller.isRefreshing && <span role="status" />}
    </header>
  ),
}));

describe("issue page refresh feedback", () => {
  beforeEach(() => {
    state.refreshing = false;
    state.user = { id: "user-1" };
  });
  afterEach(() => {
    cleanup();
  });

  it("hands the surface controller to the view header", () => {
    const { rerender } = renderWithI18n(<IssuesPage />);
    const header = screen.getByRole("banner");
    expect(within(header).getByRole("heading")).toHaveTextContent("workspace");
    expect(within(header).queryByRole("status")).not.toBeInTheDocument();

    state.refreshing = true;
    rerender(<IssuesPage />);
    expect(within(header).getByRole("status")).toBeInTheDocument();
    expect(screen.getAllByRole("banner")).toHaveLength(1);
  });
});
