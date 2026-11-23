import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { NavigationProvider } from "../navigation/context";
import type { NavigationAdapter } from "../navigation/types";

vi.mock("../issues/hooks", () => ({
  useResolveIssueIdentifier: () => null,
}));

vi.mock("../i18n", async () => {
  const editor = (await import("../locales/en/editor.json")).default;
  return {
    useT: () => ({
      t: (select: (bundle: typeof editor) => string) => select(editor),
    }),
    useTimeAgo: () => "just now",
  };
});

vi.mock("@multica/core/api", () => ({
  api: { getAttachmentTextContent: vi.fn() },
  PreviewTooLargeError: class extends Error {},
  PreviewUnsupportedError: class extends Error {},
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/acme/issues/${id}`,
    projectDetail: (id: string) => `/acme/projects/${id}`,
  }),
  useWorkspaceSlug: () => "acme",
}));

vi.mock("../issues/components/issue-mention-card", () => ({
  IssueMentionCard: ({ issueId }: { issueId: string }) => <span>{issueId}</span>,
}));

vi.mock("../projects/components/project-chip", () => ({
  useChipProject: () => undefined,
  ProjectChip: ({ projectId }: { projectId: string }) => <span>{projectId}</span>,
}));

vi.mock("../editor/link-hover-card", () => ({
  useLinkHover: () => ({}),
  LinkHoverCard: () => null,
}));

import { RichContent } from "./rich-content";

const MEMBER_ID = "8f14e45f-ceea-4d0e-a1a2-9b1c0d3e4f5a";
const CONTENT =
  `Hi [@Ada](mention://member/${MEMBER_ID}) and [@all](mention://all/all).`;

const adapter: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (p) => p,
};

describe("links to members and to @all", () => {
  it("render as plain text without a link", () => {
    const { container } = render(
      <NavigationProvider value={adapter}>
        <RichContent content={CONTENT} />
      </NavigationProvider>,
    );

    expect(screen.getByText(`Hi @Ada and @all.`)).toBeInTheDocument();
    expect(container.querySelector("a")).toBeNull();
    expect(container.querySelector(".mention")).toBeNull();
  });

  it("render a legacy shortcode as plain text without a link", () => {
    const { container } = render(
      <NavigationProvider value={adapter}>
        <RichContent content={`Hi [@ id="${MEMBER_ID}" label="Ada"].`} />
      </NavigationProvider>,
    );

    expect(screen.getByText("Hi @Ada.")).toBeInTheDocument();
    expect(container.querySelector("a")).toBeNull();
  });
});
