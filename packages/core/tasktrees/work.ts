import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** How often the list of tree work is read again: a finished task's trees
 *  become due with time, and nothing else announces that. */
export const TASK_TREE_WORK_POLL_INTERVAL_MS = 5 * 60_000;

/** The root of every list of tree work, whichever workspace it is for. */
export const TASK_TREES_QUERY_KEY = ["task-trees"] as const;

export const taskTreeKeys = {
  all: (wsId: string) => [...TASK_TREES_QUERY_KEY, wsId] as const,
  work: (wsId: string) => [...taskTreeKeys.all(wsId), "work"] as const,
};

/**
 * The trees of one workspace that the app has to build or remove, as the
 * server lists them. The request names its workspace: the app sees to every
 * workspace it is in, whichever one the window shows. This app's own reports
 * change the list, and so does a scope saved from a page: both invalidate it.
 */
export function taskTreeWorkOptions(wsId: string) {
  return queryOptions({
    queryKey: taskTreeKeys.work(wsId),
    queryFn: () => api.getTaskTreeWork(wsId),
    refetchInterval: TASK_TREE_WORK_POLL_INTERVAL_MS,
    refetchOnWindowFocus: true,
  });
}
