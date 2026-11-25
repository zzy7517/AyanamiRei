import type { ReactElement } from "react";
import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const route = vi.hoisted(() => ({ props: null as null | Record<string, unknown> }));

vi.mock("react-router-dom", () => ({ useParams: () => ({ id: "issue-1", name: undefined }) }));
vi.mock("@multica/views/issues/components", () => ({
  IssueDetailRoute: (props: Record<string, unknown>) => {
    route.props = props;
    return null;
  },
  IssueTreesSection: () => null,
}));

import { IssueDetailPage } from "./issue-detail-page";
import { IssueTreesSection } from "@multica/views/issues/components";

const renderPage = () => render(<IssueDetailPage onDelete={() => {}} />);

describe("IssueDetailPage", () => {
  it("passes the route's issue and delete action through", () => {
    renderPage();
    expect(route.props).toMatchObject({ routeId: "issue-1", deliverable: undefined });
    expect(route.props!.onDelete).toBeTypeOf("function");
  });

  it("passes the task's trees through", () => {
    renderPage();
    const trees = route.props!.trees as (issueId: string) => ReactElement;
    const section = trees("issue-1");
    expect(section.type).toBe(vi.mocked(IssueTreesSection));
    expect(section.props).toEqual({ issueId: "issue-1" });
  });

  it("keeps the same slot across renders of the page", () => {
    const { rerender } = renderPage();
    const first = route.props!.trees;
    rerender(<IssueDetailPage onDelete={() => {}} />);
    expect(route.props!.trees).toBe(first);
  });
});
