// @vitest-environment jsdom
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import { EMPTY_ISSUE_PROJECT_SCOPE } from "@multica/core/api/schemas";
import type { IssueProjectScope, IssueProjectTree, ProjectScopeItem } from "@multica/core/types";
import { getTestResources } from "../../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { IssueTreesSection } from "./issue-trees-section";

// The API is the only thing standing between the section and the server: the
// queries and mutations it uses are the real ones, so what they do after a
// write (and whether they still do it once the page is gone) is what runs here.

const mocks = vi.hoisted(() => ({
  getIssueProjectScope: vi.fn(),
  replaceIssueProjectScope: vi.fn(),
  deleteIssueProjectTree: vi.fn(),
  rebuildIssueProjectTree: vi.fn(),
  listProjects: vi.fn(),
  copyText: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@multica/core/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...real,
    // On the real client's prototype, so that a method nobody mocked here
    // fails for the usual reason instead of not existing.
    api: Object.assign(Object.create(real.api), {
      getIssueProjectScope: mocks.getIssueProjectScope,
      replaceIssueProjectScope: mocks.replaceIssueProjectScope,
      deleteIssueProjectTree: mocks.deleteIssueProjectTree,
      rebuildIssueProjectTree: mocks.rebuildIssueProjectTree,
      listProjects: mocks.listProjects,
    }),
  };
});
vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/ui/lib/clipboard", () => ({ copyText: mocks.copyText }));
vi.mock("sonner", () => ({ toast: { success: mocks.toastSuccess, error: mocks.toastError } }));
// The picker has its own tests. Here it only has to name a project.
vi.mock("../../projects/components/project-picker", () => ({
  ProjectPicker: ({ onUpdate }: { onUpdate: (updates: { project_id?: string }) => void }) => (
    <button type="button" onClick={() => onUpdate({ project_id: "proj-1" })}>
      Pick project
    </button>
  ),
}));

const navigation: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/issues/MUL-1",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (path) => path,
};

function renderSection() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider resources={getTestResources("en")} locale="en">
        <NavigationProvider value={navigation}>
          <WorkspaceSlugProvider slug="acme">
            <IssueTreesSection issueId="issue-1" />
          </WorkspaceSlugProvider>
        </NavigationProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

function tree(overrides: Partial<IssueProjectTree>): IssueProjectTree {
  return {
    id: "tree-1",
    state: "ready",
    path: "",
    task_branch: "",
    error: "",
    created_at: "2026-11-22T10:00:00Z",
    updated_at: "2026-11-22T10:00:00Z",
    ...overrides,
  };
}

function item(overrides: Partial<ProjectScopeItem>): ProjectScopeItem {
  return { project_id: "proj-1", base_branch: "main", position: 0, tree: null, ...overrides };
}

const BUILT = item({
  position: 0,
  tree: tree({ id: "tree-1", state: "ready", path: "/srv/trees/issue-1/proj-1", task_branch: "task/MUL-1" }),
});
const FAILED = item({
  project_id: "proj-2",
  base_branch: "develop",
  position: 1,
  tree: tree({ id: "tree-2", state: "failed", error: "no such branch: develop" }),
});
const DELETED = item({
  position: 2,
  tree: tree({ id: "tree-3", state: "deleted", task_branch: "task/MUL-1" }),
});
const BUILDING = item({ position: 3, tree: tree({ id: "tree-4", state: "pending" }) });
const SCOPE: IssueProjectScope = { items: [BUILT, FAILED, DELETED, BUILDING] };

const rows = () => screen.findAllByRole("listitem");
/** A row of a list that `rows()` has found whole. */
const nth = async (index: number) => (await rows())[index]!;

/** Leaves the promise returned by `mock` to the test, which settles it in its own time. */
function hold(mock: { mockImplementation: (impl: () => Promise<unknown>) => unknown }) {
  let settle!: { resolve: () => void; reject: (err: unknown) => void };
  const promise = new Promise<void>((resolve, reject) => {
    settle = { resolve, reject };
  });
  mock.mockImplementation(() => promise);
  return settle;
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.listProjects.mockResolvedValue({
    projects: [
      { id: "proj-1", title: "Billing" },
      { id: "proj-2", title: "Mobile" },
    ],
  });
  mocks.copyText.mockResolvedValue(true);
});

describe("IssueTreesSection", () => {
  it("lists one row per pair: its project, branches and state, and its path once built", async () => {
    mocks.getIssueProjectScope.mockResolvedValue(SCOPE);
    renderSection();

    const built = await nth(0);
    const failed = await nth(1);
    const deleted = await nth(2);
    const building = await nth(3);
    expect(screen.getByRole("button", { name: /^Worktrees/ })).toHaveTextContent("4");
    expect(built).toHaveTextContent("Billing");
    expect(within(built).getByText("main")).toBeInTheDocument();
    expect(within(built).getByText("task/MUL-1")).toBeInTheDocument();
    expect(built).toHaveTextContent("Ready");
    expect(within(built).getByText("/srv/trees/issue-1/proj-1")).toBeInTheDocument();
    expect(failed).toHaveTextContent("Mobile");
    expect(failed).toHaveTextContent("Failed");
    expect(failed).toHaveTextContent("no such branch: develop");
    expect(deleted).toHaveTextContent("Deleted");
    expect(deleted).toHaveTextContent("task/MUL-1");
    expect(building).toHaveTextContent("Building");
    expect(building).not.toHaveTextContent("task/");
  });

  it("names a project by its id until the list of projects is in", async () => {
    mocks.getIssueProjectScope.mockResolvedValue({ items: [BUILT] });
    mocks.listProjects.mockReturnValue(new Promise(() => {}));
    renderSection();

    expect(await rows()).toHaveLength(1);
    expect(screen.getByRole("listitem")).toHaveTextContent("proj-1");
  });

  it("opens an agent terminal and a shell in a built tree, and offers to delete it", async () => {
    mocks.getIssueProjectScope.mockResolvedValue({ items: [BUILT] });
    renderSection();

    const built = await nth(0);
    expect(within(built).getByRole("link", { name: "Agent terminal" })).toHaveAttribute(
      "href",
      "/acme/terminal?issue=issue-1&tree=tree-1",
    );
    expect(within(built).getByRole("link", { name: "Shell" })).toHaveAttribute(
      "href",
      "/acme/terminal?cwd=%2Fsrv%2Ftrees%2Fissue-1%2Fproj-1",
    );
    expect(within(built).getByRole("button", { name: "Delete worktree" })).toBeInTheDocument();
    expect(within(built).queryByRole("button", { name: "Rebuild" })).toBeNull();
  });

  it("offers a tree that failed or was deleted to be built again, and nothing else", async () => {
    mocks.getIssueProjectScope.mockResolvedValue({ items: [FAILED, DELETED] });
    renderSection();

    const failed = await nth(0);
    const deleted = await nth(1);
    for (const row of [failed, deleted]) {
      expect(within(row).getByRole("button", { name: "Rebuild" })).toBeInTheDocument();
      expect(within(row).queryByRole("link")).toBeNull();
      expect(within(row).queryByRole("button", { name: "Delete worktree" })).toBeNull();
    }
    // Its branch outlives a deleted tree; the path on the machine does not.
    expect(deleted).not.toHaveTextContent("/srv/");
  });

  it("offers nothing to do with a tree while it is being built", async () => {
    mocks.getIssueProjectScope.mockResolvedValue({ items: [BUILDING] });
    renderSection();

    const building = await nth(0);
    expect(within(building).queryByRole("button")).toBeNull();
    expect(within(building).queryByRole("link")).toBeNull();
  });

  it("copies the task branch and the path", async () => {
    mocks.getIssueProjectScope.mockResolvedValue({ items: [BUILT] });
    renderSection();
    const built = await nth(0);

    fireEvent.click(within(built).getByRole("button", { name: "Copy task branch" }));
    await waitFor(() => expect(mocks.copyText).toHaveBeenCalledWith("task/MUL-1"));
    expect(mocks.toastSuccess).toHaveBeenLastCalledWith("Copied");

    fireEvent.click(within(built).getByRole("button", { name: "Copy path" }));
    await waitFor(() => expect(mocks.copyText).toHaveBeenLastCalledWith("/srv/trees/issue-1/proj-1"));
  });

  it("says so when the clipboard has nothing of it", async () => {
    mocks.getIssueProjectScope.mockResolvedValue({ items: [BUILT] });
    mocks.copyText.mockResolvedValue(false);
    renderSection();
    const built = await nth(0);

    fireEvent.click(within(built).getByRole("button", { name: "Copy path" }));

    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledWith("Couldn't copy"));
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
  });

  describe("deleting a tree", () => {
    it("asks first, and deletes only once told to", async () => {
      mocks.getIssueProjectScope.mockResolvedValue({ items: [BUILT] });
      renderSection();
      const built = await nth(0);

      fireEvent.click(within(built).getByRole("button", { name: "Delete worktree" }));

      const dialog = await screen.findByRole("alertdialog");
      expect(dialog).toHaveAccessibleName("Delete worktree");
      expect(dialog).toHaveTextContent("The worktree is removed from the machine.");
      expect(dialog).toHaveTextContent("Billing");
      expect(dialog).toHaveTextContent("task/MUL-1");
      expect(mocks.deleteIssueProjectTree).not.toHaveBeenCalled();
    });

    it("closes without a request when cancelled", async () => {
      mocks.getIssueProjectScope.mockResolvedValue({ items: [BUILT] });
      renderSection();
      const built = await nth(0);
      fireEvent.click(within(built).getByRole("button", { name: "Delete worktree" }));
      const dialog = await screen.findByRole("alertdialog");

      fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

      await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
      expect(mocks.deleteIssueProjectTree).not.toHaveBeenCalled();
      expect(screen.getByRole("listitem")).toHaveTextContent("Ready");
    });

    it("keeps the tree on screen and the question open until the server has deleted it, then reads the list again", async () => {
      mocks.getIssueProjectScope.mockResolvedValue({ items: [BUILT] });
      renderSection();
      const built = await nth(0);
      const server = hold(mocks.deleteIssueProjectTree);

      fireEvent.click(within(built).getByRole("button", { name: "Delete worktree" }));
      const dialog = await screen.findByRole("alertdialog");
      mocks.getIssueProjectScope.mockClear();
      fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

      await waitFor(() => expect(mocks.deleteIssueProjectTree).toHaveBeenCalledWith("issue-1", "tree-1"));
      // Behind the question, which hides the page from assistive technology,
      // the tree is still the one shown.
      expect(screen.getByRole("listitem", { hidden: true })).toHaveTextContent("Ready");
      expect(screen.getByRole("alertdialog")).toBeInTheDocument();
      expect(within(dialog).getByRole("button", { name: "Cancel" })).toBeDisabled();
      expect(mocks.getIssueProjectScope).not.toHaveBeenCalled();

      mocks.getIssueProjectScope.mockResolvedValue({ items: [{ ...BUILT, tree: tree({ id: "tree-1", state: "deleted" }) }] });
      await act(async () => server.resolve());

      await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
      expect(await screen.findByRole("button", { name: "Rebuild" })).toBeInTheDocument();
      expect(screen.queryByRole("link", { name: "Agent terminal" })).toBeNull();
    });

    it("says why the server refused, and keeps the tree", async () => {
      mocks.getIssueProjectScope.mockResolvedValue({ items: [BUILT] });
      renderSection();
      const built = await nth(0);
      const server = hold(mocks.deleteIssueProjectTree);
      fireEvent.click(within(built).getByRole("button", { name: "Delete worktree" }));
      const dialog = await screen.findByRole("alertdialog");
      fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

      await act(async () => server.reject(new Error("Failed to fetch")));

      expect(within(dialog).getByRole("alert")).toHaveTextContent("Couldn't delete the worktree");
      expect(within(dialog).getByRole("button", { name: "Cancel" })).toBeEnabled();

      // The question can be left, and the tree is still there to be used.
      fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
      await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
      expect(screen.getByRole("listitem")).toHaveTextContent("Ready");
      expect(screen.getByRole("link", { name: "Agent terminal" })).toBeInTheDocument();
      expect(mocks.deleteIssueProjectTree).toHaveBeenCalledTimes(1);
    });
  });

  describe("building a tree again", () => {
    it.each([
      { state: "failed", pair: FAILED },
      { state: "deleted", pair: DELETED },
    ])("asks the server to build a $state tree again, then reads the list again", async ({ pair }) => {
      mocks.getIssueProjectScope.mockResolvedValue({ items: [pair] });
      renderSection();
      const row = await nth(0);
      const treeId = pair.tree!.id;

      fireEvent.click(within(row).getByRole("button", { name: "Rebuild" }));

      await waitFor(() => expect(mocks.rebuildIssueProjectTree).toHaveBeenCalledWith("issue-1", treeId));
      await waitFor(() => expect(mocks.getIssueProjectScope).toHaveBeenCalledTimes(2));
      // Nothing here decides what the tree is now.
      expect(screen.queryByRole("link", { name: "Agent terminal" })).toBeNull();
    });

    it("says why the server refused", async () => {
      mocks.getIssueProjectScope.mockResolvedValue({ items: [FAILED] });
      renderSection();
      const row = await nth(0);
      const { ApiError } = await import("@multica/core/api");
      mocks.rebuildIssueProjectTree.mockRejectedValue(
        new ApiError("The machine is not connected.", 409, "Conflict", { code: "runtime_offline" }),
      );

      fireEvent.click(within(row).getByRole("button", { name: "Rebuild" }));

      await waitFor(() => expect(mocks.toastError).toHaveBeenCalledWith("The machine is not connected."));
      expect(within(row).getByRole("button", { name: "Rebuild" })).toBeEnabled();
    });

    it("says when a refusal carries no reason", async () => {
      mocks.getIssueProjectScope.mockResolvedValue({ items: [FAILED] });
      renderSection();
      const row = await nth(0);
      mocks.rebuildIssueProjectTree.mockRejectedValue(new Error("Failed to fetch"));

      fireEvent.click(within(row).getByRole("button", { name: "Rebuild" }));

      await waitFor(() => expect(mocks.toastError).toHaveBeenCalledWith("Couldn't rebuild the worktree"));
    });
  });

  describe("a task without pairs", () => {
    it("takes them from the editor, and saves nothing until a pair is complete", async () => {
      mocks.getIssueProjectScope.mockResolvedValue({ items: [] });
      renderSection();

      const save = await screen.findByRole("button", { name: "Save" });
      expect(save).toBeDisabled();
      expect(screen.getByRole("group")).toHaveAccessibleName("Projects + branches");

      fireEvent.click(screen.getByRole("button", { name: "Add project" }));
      expect(save).toBeDisabled();
      fireEvent.click(screen.getByRole("button", { name: "Pick project" }));
      expect(screen.getByText("Enter a branch.")).toBeInTheDocument();
      expect(save).toBeDisabled();
      fireEvent.change(screen.getByRole("textbox", { name: "Branch" }), { target: { value: "main" } });

      expect(save).toBeEnabled();
      expect(mocks.replaceIssueProjectScope).not.toHaveBeenCalled();
    });

    it("saves them as the task's pairs and lists the trees they become", async () => {
      mocks.getIssueProjectScope.mockResolvedValue({ items: [] });
      renderSection();
      fireEvent.click(await screen.findByRole("button", { name: "Add project" }));
      fireEvent.click(screen.getByRole("button", { name: "Pick project" }));
      fireEvent.change(screen.getByRole("textbox", { name: "Branch" }), { target: { value: "main" } });
      const saved: IssueProjectScope = { items: [BUILDING] };
      mocks.replaceIssueProjectScope.mockResolvedValue(saved);

      fireEvent.click(screen.getByRole("button", { name: "Save" }));

      await waitFor(() =>
        expect(mocks.replaceIssueProjectScope).toHaveBeenCalledWith("issue-1", {
          items: [{ project_id: "proj-1", base_branch: "main" }],
        }),
      );
      expect(await screen.findByRole("listitem")).toHaveTextContent("Building");
      expect(screen.queryByRole("button", { name: "Add project" })).toBeNull();
    });

    it("keeps the pairs in the editor and says why the server refused them", async () => {
      mocks.getIssueProjectScope.mockResolvedValue({ items: [] });
      renderSection();
      fireEvent.click(await screen.findByRole("button", { name: "Add project" }));
      fireEvent.click(screen.getByRole("button", { name: "Pick project" }));
      fireEvent.change(screen.getByRole("textbox", { name: "Branch" }), { target: { value: "main" } });
      const { ApiError } = await import("@multica/core/api");
      mocks.replaceIssueProjectScope.mockRejectedValue(
        new ApiError("Choose a branch that exists.", 400, "Bad Request"),
      );

      fireEvent.click(screen.getByRole("button", { name: "Save" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("Choose a branch that exists.");
      expect(screen.getByRole("textbox", { name: "Branch" })).toHaveValue("main");
      expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    });
  });

  describe("a list that could not be read", () => {
    it.each([
      { why: "the request failed", answer: () => Promise.reject(new Error("Failed to fetch")) },
      { why: "the answer was malformed", answer: () => Promise.resolve(EMPTY_ISSUE_PROJECT_SCOPE) },
    ])("offers to read it again, and not the editor, while $why", async ({ answer }) => {
      mocks.getIssueProjectScope.mockImplementation(answer);
      renderSection();

      const retry = await screen.findByRole("button", { name: "Could not load. Retry" });
      // Saving from the editor replaces the whole list, pairs that could not
      // be shown included.
      expect(screen.queryByRole("button", { name: "Add project" })).toBeNull();

      mocks.getIssueProjectScope.mockResolvedValue({ items: [BUILT] });
      fireEvent.click(retry);

      expect(mocks.getIssueProjectScope).toHaveBeenCalledTimes(2);
      expect(await screen.findByRole("listitem")).toHaveTextContent("Ready");
    });
  });

  it("has nothing to act on while the list is loading", async () => {
    mocks.getIssueProjectScope.mockReturnValue(new Promise(() => {}));
    renderSection();

    expect(await screen.findByRole("button", { name: /^Worktrees/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Retry|Add project|Save/ })).toBeNull();
  });
});
