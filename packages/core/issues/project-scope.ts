import {
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
import { EMPTY_ISSUE_PROJECT_SCOPE } from "../api/schemas";
import type {
  IssueProjectScope,
  ReplaceIssueProjectScopeRequest,
} from "../types";

export const issueProjectScopeKeys = {
  all: (wsId: string) => ["issues", wsId, "project-scope"] as const,
  own: (wsId: string, issueId: string) =>
    [...issueProjectScopeKeys.all(wsId), "own", issueId] as const,
};

/** How often a scope with a tree waiting to be built is read again. */
export const ISSUE_TREE_POLL_INTERVAL_MS = 3_000;

const hasPendingTree = (scope: IssueProjectScope) =>
  scope.items.some((item) => item.tree?.state === "pending");

export function issueProjectScopeOptions(wsId: string, issueId: string) {
  return queryOptions({
    queryKey: issueProjectScopeKeys.own(wsId, issueId),
    queryFn: () => api.getIssueProjectScope(issueId),
    // The daemon builds a tree on its own schedule and no event reports when
    // it is done, so the scope is read again until no tree is waiting.
    refetchInterval: (query) =>
      query.state.data && hasPendingTree(query.state.data) ? ISSUE_TREE_POLL_INTERVAL_MS : false,
  });
}

/** The pairs a task works on. Until they load, and after a malformed
 *  response, the scope reads as empty. */
export function useIssueProjectScope(
  wsId: string,
  issueId: string | null | undefined,
) {
  const query = useQuery({
    ...issueProjectScopeOptions(wsId, issueId ?? ""),
    enabled: issueId != null,
  });
  return { ...query, data: query.data ?? EMPTY_ISSUE_PROJECT_SCOPE };
}

/** The pairs that replace a task's. `issue_id` names a task other than the
 *  hook's: one created a moment ago, whose id did not exist when the hook
 *  mounted. */
export interface ReplaceIssueProjectScopeInput extends ReplaceIssueProjectScopeRequest {
  issue_id?: string;
}

/** Replaces a task's pairs wholesale, in their order. The cache follows from
 *  the response. Removing a pair whose tree is ready is refused by the server,
 *  as a thrown error. */
export function useReplaceIssueProjectScope(wsId: string, issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ issue_id = issueId, items }: ReplaceIssueProjectScopeInput) =>
      api.replaceIssueProjectScope(issue_id, { items }),
    onSuccess: (scope, { issue_id = issueId }) => {
      const key = issueProjectScopeKeys.own(wsId, issue_id);
      // A malformed response to a write that went through says nothing about
      // the stored pairs: they are refetched, not read as none.
      if (scope) qc.setQueryData(key, scope);
      else void qc.invalidateQueries({ queryKey: key });
    },
  });
}

/** Marks a task's tree deleted. The daemon removes it from the disk after the
 *  server accepted, so nothing is taken out of the cache first. */
export function useDeleteIssueProjectTree(wsId: string, issueId: string) {
  return useIssueProjectTreeWrite(wsId, issueId, (treeId) =>
    api.deleteIssueProjectTree(issueId, treeId),
  );
}

/** Asks for a deleted or failed tree to be built again. */
export function useRebuildIssueProjectTree(wsId: string, issueId: string) {
  return useIssueProjectTreeWrite(wsId, issueId, (treeId) =>
    api.rebuildIssueProjectTree(issueId, treeId),
  );
}

function useIssueProjectTreeWrite(
  wsId: string,
  issueId: string,
  write: (treeId: string) => Promise<unknown>,
) {
  const qc = useQueryClient();
  const readAgain = () =>
    qc.invalidateQueries({ queryKey: issueProjectScopeKeys.own(wsId, issueId) });
  return useMutation({
    mutationFn: write,
    // The answer describes one tree. The panel shows every tree as they are
    // now, which only a fresh read can say.
    onSuccess: readAgain,
    // A refusal means the tree is not in the state the panel showed (the
    // server says "read it again"), or that it names no tree to list anymore.
    onError: readAgain,
  });
}
