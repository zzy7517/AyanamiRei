/**
 * The agent rows of the comment @mention picker, filtered by the search
 * query and sorted by name.
 * Mirrors `packages/views/editor/extensions/mention-suggestion.tsx`.
 */
import type { Agent } from "@multica/core/types";

export function commentMentionCandidates({
  query,
  agents,
}: {
  query: string;
  agents: readonly Agent[];
}): Agent[] {
  const q = query.trim().toLowerCase();
  const matchName = (name: string) => !q || name.toLowerCase().includes(q);
  const byName = (a: { name: string }, b: { name: string }) =>
    a.name.localeCompare(b.name);
  return agents.filter((a) => matchName(a.name)).sort(byName);
}
