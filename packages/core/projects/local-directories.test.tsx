/** @vitest-environment jsdom */
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { ListProjectResourcesResponse, ListProjectsResponse, Project, ProjectResource } from "../types";
import { useProjectLocalDirectories } from "./local-directories";
import { projectKeys } from "./queries";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function project(id: string, title: string, over: Partial<Project> = {}): Project {
  return {
    id,
    workspace_id: "ws1",
    title,
    description: null,
    icon: null,
    status: "planned",
    priority: "none",
    lead_type: null,
    lead_id: null,
    start_date: null,
    due_date: null,
    created_at: "2026-11-24T00:00:00Z",
    updated_at: "2026-11-24T00:00:00Z",
    issue_count: 0,
    done_count: 0,
    resource_count: 0,
    ...over,
  };
}

function folder(id: string, projectId: string, path: string): ProjectResource {
  return {
    id,
    project_id: projectId,
    workspace_id: "ws1",
    resource_type: "local_directory",
    resource_ref: { local_path: path, daemon_id: "daemon-1" },
    label: null,
    position: 0,
    created_at: "2026-11-24T00:00:00Z",
    created_by: null,
  };
}

const zeta = project("zeta", "Zeta", { resource_count: 1 });
const alpha = project("alpha", "Alpha", { resource_count: 2 });
const archived = project("gamma", "Gamma", { resource_count: 1, archived_at: "2026-11-01T00:00:00Z" });
const empty = project("delta", "Delta");
const inZeta = folder("fz", "zeta", "/srv/zeta");
const inAlpha = folder("fa", "alpha", "C:\\code\\alpha");
const alphaRepo: ProjectResource = {
  ...folder("r1", "alpha", ""),
  resource_type: "github_repo",
  resource_ref: { owner: "multica-ai", repo: "alpha" },
};

describe("useProjectLocalDirectories", () => {
  let qc: QueryClient;
  let listProjects: ReturnType<typeof vi.fn>;
  let listResources: ReturnType<typeof vi.fn>;

  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );

  beforeEach(() => {
    qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    });
    listProjects = vi.fn();
    listResources = vi.fn();
    setApiInstance({ listProjects, listProjectResources: listResources } as unknown as ApiClient);
  });

  afterEach(() => {
    qc.clear();
    vi.restoreAllMocks();
  });

  it("lists the local folders of the projects that are not archived, by project title", async () => {
    listProjects.mockResolvedValue({ projects: [zeta, archived, alpha, empty], total: 4 });
    listResources.mockImplementation((projectId: string) =>
      Promise.resolve({
        resources: projectId === "alpha" ? [alphaRepo, inAlpha] : [inZeta],
        total: projectId === "alpha" ? 2 : 1,
      }),
    );
    const { result } = renderHook(() => useProjectLocalDirectories("ws1"), { wrapper });

    await waitFor(() => expect(result.current.folders).toHaveLength(2));
    // A folder's path is the daemon's, passed on as it was registered.
    expect(result.current.folders).toEqual([
      { project: alpha, resource: inAlpha },
      { project: zeta, resource: inZeta },
    ]);
    expect(result.current.loading).toBe(false);
    expect(listProjects).toHaveBeenCalledWith({ include_archived: true });
    // Only projects with resources are asked, and not the archived one.
    expect(listResources.mock.calls.map(([projectId]) => projectId).sort()).toEqual(["alpha", "zeta"]);
    // The cached list keeps the order the server sent.
    const cached = qc.getQueryData<ListProjectsResponse>(projectKeys.list("ws1"));
    expect(cached?.projects.map((p) => p.id)).toEqual(["zeta", "gamma", "alpha", "delta"]);
  });

  it("is loading until the lists have answered, and leaves out a project whose resources cannot be read", async () => {
    listProjects.mockResolvedValue({ projects: [alpha, zeta], total: 2 });
    const zetaList = deferred<ListProjectResourcesResponse>();
    listResources.mockImplementation((projectId: string) =>
      projectId === "zeta" ? zetaList.promise : Promise.reject(new Error("500")),
    );
    const { result } = renderHook(() => useProjectLocalDirectories("ws1"), { wrapper });

    expect(result.current).toEqual({ folders: [], loading: true });
    await waitFor(() => expect(listResources).toHaveBeenCalledTimes(2));
    expect(result.current).toEqual({ folders: [], loading: true });

    await act(async () => {
      zetaList.resolve({ resources: [inZeta], total: 1 });
      await zetaList.promise;
    });
    await waitFor(() =>
      expect(result.current).toEqual({ folders: [{ project: zeta, resource: inZeta }], loading: false }),
    );
  });
});
