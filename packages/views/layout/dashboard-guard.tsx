"use client";

import type { ReactNode } from "react";
import { useDashboardGuard } from "./use-dashboard-guard";

interface DashboardGuardProps {
  children: ReactNode;
  /** Rendered when auth or workspace is loading */
  loadingFallback?: ReactNode;
}

/**
 * Shared guard for dashboard layouts.
 *
 * Waits until the URL slug resolves to a workspace, then renders children.
 * A missing user record does not blank the dashboard.
 *
 * WorkspaceIdProvider has been removed — useWorkspaceId() now derives from
 * the URL slug via useCurrentWorkspace(). The guard still gates on workspace
 * being resolved so downstream components can safely call useWorkspaceId().
 */
export function DashboardGuard({
  children,
  loadingFallback = null,
}: DashboardGuardProps) {
  const { workspace } = useDashboardGuard();

  if (!workspace) return <>{loadingFallback}</>;

  return <>{children}</>;
}
