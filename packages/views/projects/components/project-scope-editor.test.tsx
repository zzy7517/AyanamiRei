import { useState, type ComponentProps, type ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import { PROJECT_SCOPE_MAX_ITEMS, type ProjectScopePair } from "@multica/core/projects";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import enProjects from "../../locales/en/projects.json";
import { ProjectScopeEditor } from "./project-scope-editor";

const projects = [
  { id: "p1", title: "Launch Command Center", icon: null },
  { id: "p2", title: "Mobile Web", icon: null },
];

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: projects }),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/projects/queries", () => ({
  projectListOptions: () => ({ queryKey: ["projects"] }),
}));
vi.mock("./project-icon", () => ({ ProjectIcon: () => null }));

const pair = (project_id: string, branch: string): ProjectScopePair => ({ project_id, branch });

type EditorProps = ComponentProps<typeof ProjectScopeEditor>;

// The editor is controlled, so a flow that edits needs a caller that adopts.
function Stateful({ initial = [], ...props }: Omit<EditorProps, "value" | "onChange"> & { initial?: ProjectScopePair[] }) {
  const [value, setValue] = useState(initial);
  return <ProjectScopeEditor {...props} value={value} onChange={setValue} />;
}

const resources = { en: { common: enCommon, projects: enProjects, issues: enIssues } };
const wrap = (ui: ReactNode) => <I18nProvider locale="en" resources={resources}>{ui}</I18nProvider>;
const rows = () => screen.getAllByRole("listitem");
const row = (index: number) => within(rows()[index]!);
const rowNode = (index: number) => rows()[index]!;
const branchInputs = () => screen.getAllByRole("textbox", { name: "Branch" });
const branches = () => branchInputs().map((input) => (input as HTMLInputElement).value);
const projectOf = (index: number) => within(rows()[index]!).getAllByRole("button")[0]!;

afterEach(() => cleanup());

describe("ProjectScopeEditor", () => {
  it("adds a pair: the new row opens its project menu, then takes a branch", async () => {
    const user = userEvent.setup();
    render(wrap(<Stateful />));
    expect(screen.queryAllByRole("listitem")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Add project" }));
    // A row just added says nothing until it has something to check.
    expect(rows()).toHaveLength(1);
    expect(rows()[0]).not.toHaveTextContent("Choose a project.");
    await user.click(await screen.findByRole("button", { name: /mobile web/i }));

    expect(projectOf(0)).toHaveTextContent(/mobile web/i);
    expect(rows()[0]).toHaveTextContent("Enter a branch.");
    await user.type(branchInputs()[0]!, "develop");
    expect(branches()).toEqual(["develop"]);
    expect(rows()[0]).not.toHaveTextContent("Enter a branch.");
  });

  it("removes a pair and keeps the others", async () => {
    const user = userEvent.setup();
    render(wrap(<Stateful initial={[pair("p1", "main"), pair("p2", "develop")]} />));

    await user.click(row(1).getByRole("button", { name: "Remove" }));

    expect(branches()).toEqual(["main"]);
  });

  it("moves a pair up and down, and not past either end", async () => {
    const user = userEvent.setup();
    render(
      wrap(
        <Stateful initial={[pair("p1", "main"), pair("p2", "develop"), pair("p1", "release")]} />,
      ),
    );
    expect(row(0).getByRole("button", { name: "Move up" })).toBeDisabled();

    await user.click(row(2).getByRole("button", { name: "Move up" }));
    expect(branches()).toEqual(["main", "release", "develop"]);
    expect(projectOf(1)).toHaveTextContent(/launch command center/i);

    await user.click(row(0).getByRole("button", { name: "Move down" }));
    expect(branches()).toEqual(["release", "main", "develop"]);
    expect(row(2).getByRole("button", { name: "Move down" })).toBeDisabled();
  });

  it("marks the later of two identical pairs, and accepts one project on several branches", () => {
    render(
      wrap(
        <Stateful initial={[pair("p1", "main"), pair("p1", "develop"), pair("p1", "main")]} />,
      ),
    );

    expect(rowNode(1)).not.toHaveTextContent("already in the list");
    expect(rowNode(2)).toHaveTextContent("This project and branch are already in the list.");
    expect(branchInputs()[2]).toHaveAttribute("aria-invalid", "true");
    expect(branchInputs()[2]).toHaveAccessibleDescription(
      "This project and branch are already in the list.",
    );
    expect(branchInputs()[1]).toHaveAttribute("aria-invalid", "false");
  });

  it("refuses a branch that is not a git ref, and one left empty", async () => {
    const user = userEvent.setup();
    render(wrap(<Stateful initial={[pair("p1", "main"), pair("", "main")]} />));
    const branch = branchInputs()[0]!;
    expect(projectOf(1)).toHaveAttribute("aria-invalid", "true");
    expect(rowNode(1)).toHaveTextContent("Choose a project.");

    await user.clear(branch);
    expect(rowNode(0)).toHaveTextContent("Enter a branch.");
    await user.type(branch, "main..dev");
    expect(rowNode(0)).toHaveTextContent("Not a valid branch name.");
    await user.clear(branch);
    await user.type(branch, "my branch");
    expect(rowNode(0)).toHaveTextContent("Branch names can't contain spaces");
    expect(branch).toHaveAttribute("aria-invalid", "true");
    await user.clear(branch);
    await user.type(branch, "release/2026-09");
    expect(branch).toHaveAttribute("aria-invalid", "false");
    expect(rowNode(0)).not.toHaveTextContent("valid branch name");
    expect(rowNode(0)).not.toHaveTextContent("Enter a branch.");
  });

  it("stops at the server's cap of pairs", () => {
    const full = Array.from({ length: PROJECT_SCOPE_MAX_ITEMS }, (_, i) => pair(`p${i}`, "main"));
    const { rerender } = render(wrap(<ProjectScopeEditor value={full} onChange={vi.fn()} />));
    expect(screen.getByRole("button", { name: "Add project" })).toBeDisabled();
    expect(screen.getByText("Up to 50 pairs.")).toBeInTheDocument();

    rerender(wrap(<ProjectScopeEditor value={full.slice(0, -1)} onChange={vi.fn()} />));
    expect(screen.getByRole("button", { name: "Add project" })).toBeEnabled();
    expect(screen.queryByText("Up to 50 pairs.")).not.toBeInTheDocument();
  });

  it("is controlled by its value: it reports the next list and keeps no state", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const value = [pair("p1", "main")];
    render(wrap(<ProjectScopeEditor value={value} onChange={onChange} />));

    await user.type(branchInputs()[0]!, "x");
    expect(onChange).toHaveBeenLastCalledWith([pair("p1", "mainx")]);
    // Not adopted by the caller, so not shown.
    expect(branches()).toEqual(["main"]);

    await user.click(row(0).getByRole("button", { name: "Remove" }));
    expect(onChange).toHaveBeenLastCalledWith([]);
    expect(rows()).toHaveLength(1);
    expect(value).toEqual([pair("p1", "main")]);
  });

  it("keeps a busy row, and the row being edited, marked under the pointer", async () => {
    const user = userEvent.setup();
    const pairs = [pair("p1", "main"), pair("p2", "develop")];
    const { rerender } = render(wrap(<Stateful initial={pairs} />));

    await user.click(branchInputs()[1]!);
    expect(rows()[1]).toHaveClass("bg-surface-selected", "hover:bg-surface-selected");
    expect(rows()[0]).not.toHaveClass("bg-surface-selected");

    rerender(wrap(<ProjectScopeEditor value={pairs} onChange={vi.fn()} busy />));
    for (const item of rows()) expect(item).toHaveClass("bg-muted", "hover:bg-muted");
    expect(branchInputs().every((input) => (input as HTMLInputElement).disabled)).toBe(true);
    expect(screen.getByRole("button", { name: "Add project" })).toBeDisabled();

    rerender(wrap(<ProjectScopeEditor value={pairs} onChange={vi.fn()} disabled />));
    expect(branchInputs().every((input) => (input as HTMLInputElement).disabled)).toBe(true);
    for (const index of [0, 1]) {
      expect(row(index).getByRole("button", { name: "Remove" })).toBeDisabled();
    }
  });
});
