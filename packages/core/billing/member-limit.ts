import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import { workspaceKeys } from "../workspace/queries";

/**
 * Member limit (MUL-9488).
 *
 * A Free workspace can have a limited number of members; pending invitations
 * take a slot too. The server enforces the limit; this module only renders it.
 */
export const memberLimitKeys = {
  /** Under `members`, so every member change also refetches the limit. */
  all: (wsId: string) => [...workspaceKeys.members(wsId), "limit"] as const,
};

/** Refetched on focus: an upgrade happens outside this tab, in Stripe. */
export function memberLimitOptions(wsId: string) {
  return queryOptions({
    queryKey: memberLimitKeys.all(wsId),
    queryFn: () => api.getMemberLimit(wsId),
    enabled: wsId.length > 0,
    staleTime: 30 * 1000,
    refetchOnWindowFocus: true,
  });
}
