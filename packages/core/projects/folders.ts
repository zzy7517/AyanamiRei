import { useQueries, useQuery } from "@tanstack/react-query";
import type { Project, ProjectResource } from "../types";
import type { TerminalPlace } from "../terminal/host";
import { isProjectArchived } from "./archive";
import { projectListOptions } from "./queries";
import { projectResourcesOptions } from "./resource-queries";

/** A project's folder, with the project it belongs to: on this computer, or on an SSH host. */
export interface ProjectFolder {
  project: Project;
  resource: ProjectResource;
  place: TerminalPlace;
  path: string;
}

export interface ProjectFolders {
  /**
   * New on every call: read what is needed from it, but do not make an effect,
   * or what identifies a session, depend on the array itself.
   */
  folders: ProjectFolder[];
  /** True until the project list and every resource list asked for have answered. */
  loading: boolean;
}

/** Where a resource's folder is, for a resource that is a folder. */
export function projectFolderPlace(resource: ProjectResource): { place: TerminalPlace; path: string } | null {
  const ref = resource.resource_ref;
  if (resource.resource_type === "local_directory") {
    const path = (ref as { local_path?: unknown }).local_path;
    if (typeof path === "string") return { place: { kind: "local" }, path };
  }
  if (resource.resource_type === "ssh_directory") {
    const { host, path } = ref as { host?: unknown; path?: unknown };
    if (typeof host === "string" && typeof path === "string") {
      return { place: { kind: "ssh", host }, path };
    }
  }
  return null;
}

/**
 * Whether a typed host and path name a folder the server accepts: a host that
 * is one word, as `ssh` takes it, and an absolute path. Whether the folder is
 * there is learned when a terminal opens in it.
 */
export function sshFolderReady(host: string, path: string): boolean {
  const trimmedHost = host.trim();
  return trimmedHost !== "" && !/\s/.test(trimmedHost) && path.trim().startsWith("/");
}

/** The folder's path as a person writes it: `host:path` on an SSH host. */
export function projectFolderLabel(folder: Pick<ProjectFolder, "place" | "path">): string {
  return folder.place.kind === "ssh" ? `${folder.place.host}:${folder.path}` : folder.path;
}

/**
 * The folders the workspace's projects registered, each with its project, in
 * order of project title. Archived projects are left out, as from any other
 * place that offers a project as a choice, and only projects that have
 * resources at all are asked for theirs.
 */
export function useProjectFolders(wsId: string): ProjectFolders {
  const projects = useQuery(projectListOptions(wsId));
  const withResources = (projects.data ?? [])
    .filter((project) => !isProjectArchived(project) && project.resource_count > 0)
    .sort((a, b) => a.title.localeCompare(b.title));
  const resources = useQueries({
    queries: withResources.map((project) => projectResourcesOptions(wsId, project.id)),
  });
  return {
    folders: withResources.flatMap((project, i) =>
      (resources[i]?.data ?? []).flatMap((resource) => {
        const folder = projectFolderPlace(resource);
        return folder ? [{ project, resource, ...folder }] : [];
      }),
    ),
    loading: projects.isPending || resources.some((list) => list.isPending),
  };
}
