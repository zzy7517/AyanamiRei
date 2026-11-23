import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

const mockUpdateWorkspace = vi.hoisted(() => vi.fn());
const workspaceRef = vi.hoisted(() => ({
  current: { id: "workspace-1", settings: {} as Record<string, unknown> },
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: [] }),
  useQueryClient: () => ({ setQueryData: vi.fn() }),
  queryOptions: <T,>(opts: T) => opts,
}));
vi.mock("@multica/core/paths", () => ({ useCurrentWorkspace: () => workspaceRef.current }));
vi.mock("@multica/core/api", () => ({ api: { updateWorkspace: mockUpdateWorkspace } }));
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));

import { TaskTreeRetentionRow } from "./task-tree-retention-row";

const TEST_RESOURCES = { en: { common: enCommon, settings: enSettings } };

function Wrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

const field = () =>
  screen.getByRole("spinbutton", { name: enSettings.code.task_tree_retention.label });

beforeEach(() => {
  vi.clearAllMocks();
  workspaceRef.current = { id: "workspace-1", settings: {} };
  // The server's answer is what the workspace holds from then on.
  mockUpdateWorkspace.mockImplementation(
    async (_id: string, payload: { settings: Record<string, unknown> }) => {
      workspaceRef.current = { ...workspaceRef.current, settings: payload.settings };
      return workspaceRef.current;
    },
  );
});

describe("TaskTreeRetentionRow", () => {
  it("shows 5 days until the workspace sets how long it keeps worktrees", () => {
    render(<TaskTreeRetentionRow canManage />, { wrapper: Wrapper });
    expect(field()).toHaveValue(5);

    cleanup();
    workspaceRef.current.settings = { task_tree_retention_days: 30 };
    render(<TaskTreeRetentionRow canManage />, { wrapper: Wrapper });
    expect(field()).toHaveValue(30);
  });

  it("saves a whole number of days with the other workspace settings", async () => {
    const user = userEvent.setup();
    workspaceRef.current.settings = { github_enabled: false };
    render(<TaskTreeRetentionRow canManage />, { wrapper: Wrapper });

    await user.clear(field());
    await user.type(field(), "12");
    await user.tab();

    await waitFor(() =>
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        settings: { github_enabled: false, task_tree_retention_days: 12 },
      }),
    );
    expect(field()).toHaveValue(12);
  });

  it.each([["0"], ["2.5"], [""]])(
    "puts the saved value back for %j and saves nothing",
    async (typed) => {
      const user = userEvent.setup();
      workspaceRef.current.settings = { task_tree_retention_days: 30 };
      render(<TaskTreeRetentionRow canManage />, { wrapper: Wrapper });

      await user.clear(field());
      if (typed) await user.type(field(), typed);
      await user.tab();

      expect(field()).toHaveValue(30);
      expect(mockUpdateWorkspace).not.toHaveBeenCalled();
    },
  );

  it("saves nothing while the number is the one already saved", async () => {
    const user = userEvent.setup();
    render(<TaskTreeRetentionRow canManage />, { wrapper: Wrapper });

    await user.clear(field());
    await user.type(field(), "5");
    await user.tab();

    expect(field()).toHaveValue(5);
    expect(mockUpdateWorkspace).not.toHaveBeenCalled();
  });

  it("puts the saved value back when the save fails", async () => {
    const user = userEvent.setup();
    const { toast } = await import("sonner");
    mockUpdateWorkspace.mockRejectedValue(new Error("offline"));
    render(<TaskTreeRetentionRow canManage />, { wrapper: Wrapper });

    await user.clear(field());
    await user.type(field(), "12");
    await user.tab();

    await waitFor(() => expect(field()).toHaveValue(5));
    expect(toast.error).toHaveBeenCalledWith("offline");
  });

  it("is read-only for members who cannot manage the workspace", () => {
    render(<TaskTreeRetentionRow canManage={false} />, { wrapper: Wrapper });
    expect(field()).toBeDisabled();
  });
});
