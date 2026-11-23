import type { ReactElement } from "react";
import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { configStore } from "@multica/core/config";

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
  beforeEach(() => {
    route.props = null;
    configStore.getState().setFeatureFlags({});
  });

  it("passes the route's issue and delete action through", () => {
    renderPage();
    expect(route.props).toMatchObject({ routeId: "issue-1", deliverable: undefined });
    expect(route.props!.onDelete).toBeTypeOf("function");
  });

  it("offers no trees while the task sessions flag is off", () => {
    renderPage();
    expect(route.props!.trees).toBeUndefined();
  });

  it("passes the task's trees behind the flag", () => {
    configStore.getState().setFeatureFlags({ task_sessions_v1: true });
    renderPage();
    const trees = route.props!.trees as (issueId: string) => ReactElement;
    const section = trees("issue-1");
    expect(section.type).toBe(vi.mocked(IssueTreesSection));
    expect(section.props).toEqual({ issueId: "issue-1" });
  });

  it("keeps the same slot across renders of the page", () => {
    configStore.getState().setFeatureFlags({ task_sessions_v1: true });
    const { rerender } = renderPage();
    const first = route.props!.trees;
    rerender(<IssueDetailPage onDelete={() => {}} />);
    expect(route.props!.trees).toBe(first);
  });
});
