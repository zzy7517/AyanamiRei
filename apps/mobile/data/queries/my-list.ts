/**
 * The viewer's own list — the issues they created — behind the chat mention
 * bar's issue suggestions.
 *
 * Mobile owns these query keys and its QueryClient. useMyListRealtime
 * patches or invalidates the local list; each client receives its own
 * server events.
 */
import { queryOptions } from "@tanstack/react-query";
import { api } from "@/data/api";
import { issueKeys, type MyIssueListFilter } from "./issue-keys";

export const myIssueListOptions = (
  wsId: string | null,
  filter: MyIssueListFilter,
) =>
  queryOptions({
    queryKey: issueKeys.myList(wsId, filter),
    queryFn: async ({ signal }) => {
      const res = await api.listIssues(filter, { signal });
      return res.issues;
    },
    enabled: !!wsId,
  });
