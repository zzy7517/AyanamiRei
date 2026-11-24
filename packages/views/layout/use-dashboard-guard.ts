"use client";

import { useEffect } from "react";
import { useNavigationStore } from "@multica/core/navigation";
import { useAuthStore } from "@multica/core/auth";
import {
  paths,
  resolvePostAuthDestination,
  useCurrentWorkspace,
  useHasOnboarded,
} from "@multica/core/paths";
import { useWorkspaceList } from "@multica/core/workspace";
import { useRecentIssuesStore } from "@multica/core/issues/stores";
import { useRecentVisitsStore } from "@multica/core/command-bar";
import { useNavigation } from "../navigation";

/**
 * Workspace gate for the dashboard.
 *
 * Redirect logic:
 *  - Workspace list not yet loaded → wait
 *  - URL slug resolves → stay
 *  - User record still loading and the slug does not resolve → wait
 *  - User record is known and the slug does not resolve →
 *    `resolvePostAuthDestination(list, hasOnboarded)`
 *  - No user record and the slug does not resolve → the first workspace,
 *    or /workspaces/new when the list is empty
 *
 * There is no login route. A missing user is not a signed-out screen.
 *
 * We read the workspace list query state directly (rather than relying on
 * useCurrentWorkspace's null return) so we can distinguish "list loading"
 * from "slug not found". Otherwise users could see a transient redirect
 * before their workspace list arrives.
 */
export function useDashboardGuard() {
  const { pathname, replace } = useNavigation();
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);
  const workspace = useCurrentWorkspace();
  const hasOnboarded = useHasOnboarded();
  const { workspaces, ready: workspaceListReady } = useWorkspaceList();

  useEffect(() => {
    if (!workspaceListReady) return;
    if (workspace) return;
    // Onboarding depends on the user record. Don't pick a destination
    // until that load finishes, or a still-loading user would skip it.
    if (isLoading) return;
    const first = workspaces[0];
    replace(
      user
        ? resolvePostAuthDestination(workspaces, hasOnboarded)
        : first
          ? paths.workspace(first.slug).issues()
          : paths.newWorkspace(),
    );
  }, [user, isLoading, workspaceListReady, workspace, workspaces, hasOnboarded, replace]);

  useEffect(() => {
    useNavigationStore.getState().onPathChange(pathname);
  }, [pathname]);

  // Drop recent-issues buckets for workspaces the user no longer belongs to.
  // Runs once the workspace list resolves, and again whenever membership
  // changes (workspace deleted, user kicked out).
  useEffect(() => {
    if (!workspaceListReady) return;
    const activeWsIds = workspaces.map((w) => w.id);
    useRecentIssuesStore.getState().pruneWorkspaces(activeWsIds);
    useRecentVisitsStore.getState().pruneWorkspaces(activeWsIds);
  }, [workspaceListReady, workspaces]);

  return { user, isLoading, workspace };
}
