import { useQueries, useQuery } from "@tanstack/react-query";
import type { LocalDirectoryResourceRef, Project, ProjectResource } from "../types";
import { isProjectArchived } from "./archive";
import { projectListOptions } from "./queries";
import { projectResourcesOptions } from "./resource-queries";

type LocalDirectoryResource = ProjectResource & { resource_ref: LocalDirectoryResourceRef };

function isLocalDirectory(resource: ProjectResource): resource is LocalDirectoryResource {
  return resource.resource_type === "local_directory";
}

/** A local folder a project registered, with the project it belongs to. */
export interface ProjectLocalDirectory {
  project: Project;
  resource: LocalDirectoryResource;
}

export interface ProjectLocalDirectories {
  /**
   * New on every call: read what is needed from it, but do not make an effect,
   * or what identifies a session, depend on the array itself.
   */
  folders: ProjectLocalDirectory[];
  /** True until the project list and every resource list asked for have answered. */
  loading: boolean;
}

/**
 * The local folders the workspace's projects registered, each with its
 * project, in order of project title. Archived projects are left out, as from
 * any other place that offers a project as a choice, and only projects that
 * have resources at all are asked for theirs.
 */
export function useProjectLocalDirectories(wsId: string): ProjectLocalDirectories {
  const projects = useQuery(projectListOptions(wsId));
  const withResources = (projects.data ?? [])
    .filter((project) => !isProjectArchived(project) && project.resource_count > 0)
    .sort((a, b) => a.title.localeCompare(b.title));
  const resources = useQueries({
    queries: withResources.map((project) => projectResourcesOptions(wsId, project.id)),
  });
  return {
    folders: withResources.flatMap((project, i) =>
      (resources[i]?.data ?? []).filter(isLocalDirectory).map((resource) => ({ project, resource })),
    ),
    loading: projects.isPending || resources.some((list) => list.isPending),
  };
}
