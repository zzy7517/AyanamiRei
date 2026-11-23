import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { IssueView } from "../api/schemas";

/** The (scope_type, scope_id) container a surface's views live in. */
export interface IssueViewScope {
  scope_type: "workspace" | "project";
  scope_id?: string | null;
}

export const issueViewKeys = {
  all: (wsId: string) => ["issue-views", wsId] as const,
  list: (wsId: string, scope: IssueViewScope) =>
    [...issueViewKeys.all(wsId), scope.scope_type, scope.scope_id ?? null] as const,
  detail: (wsId: string, id: string) =>
    [...issueViewKeys.all(wsId), "detail", id] as const,
  everywhere: (wsId: string) => [...issueViewKeys.all(wsId), "everywhere"] as const,
};

/** Every view the caller can open, across workspace and project scopes. */
export function issueViewEverywhereOptions(wsId: string) {
  return queryOptions({
    queryKey: issueViewKeys.everywhere(wsId),
    queryFn: async () => {
      let views: IssueView[];
      try {
        views = await api.listIssueViews({ scope_type: "all" });
      } catch {
        // Backends without the cross-scope list reject scope_type=all.
        views = await api.listIssueViews({ scope_type: "workspace" });
      }
      // A view scoped to a surface this client does not have cannot be opened.
      return views.filter((view) => view.scope_type !== "my");
    },
    enabled: !!wsId,
  });
}

export function issueViewListOptions(wsId: string, scope: IssueViewScope) {
  return queryOptions({
    queryKey: issueViewKeys.list(wsId, scope),
    queryFn: () => api.listIssueViews(scope),
    enabled: !!wsId,
  });
}

/** One view by id — the sidebar pin rows resolve their label/target here. */
export function issueViewDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: issueViewKeys.detail(wsId, id),
    queryFn: () => api.getIssueView(id),
    enabled: !!wsId && !!id,
  });
}

/**
 * Client-side mirror of the server's canManageIssueView: the owner, or a
 * workspace owner/admin for views shared to the workspace. Drives which
 * affordances render — the server re-checks on every write.
 */
export function canManageIssueView(
  view: { owner_id: string; visibility: string },
  userId: string | null,
  role: string | null | undefined,
): boolean {
  if (!userId) return false;
  if (view.owner_id === userId) return true;
  return view.visibility === "workspace" && (role === "owner" || role === "admin");
}
