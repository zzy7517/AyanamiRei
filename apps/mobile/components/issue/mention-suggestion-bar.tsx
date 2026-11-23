/**
 * Above-input suggestion bar for @mentions.
 *
 * Two modes — `comment` (the default, used in issue comment composer
 * and new-issue body) and `chat` (used in chat composer).
 *
 * `comment` sections:
 *   1. Agents — sorted alphabetically
 *
 * `chat` sections (chat is user ↔ single agent — an `@agent` there is
 * noise; `@` here means "reference a resource for the agent"):
 *   1. Recent — issues the user opened most recently (from the in-memory
 *      viewed-issues store), max 5
 *   2. Created by the viewer — deduped against Recent, max 10
 *
 * Closed issues (status `done` / `cancelled`) are dimmed but selectable,
 * matching web's behaviour (mention-suggestion.tsx).
 */
import { useMemo } from "react";
import { FlatList, Pressable, View } from "react-native";
import { useQueries, useQuery } from "@tanstack/react-query";
import type { Agent, Issue } from "@multica/core/types";
import { canAssignAgentToIssue } from "@multica/core/permissions/rules";
import { Text } from "@/components/ui/text";
import { ActorAvatar } from "@/components/ui/actor-avatar";
import { StatusIcon } from "@/components/ui/status-icon";
import {
  CLOSED_CATEGORIES,
  issueBehavesAsAny,
  issueColumnCategory,
} from "@/lib/issue-status";
import { useIssueStatuses } from "@/lib/use-issue-statuses";
import { useT } from "@/lib/i18n";
import { memberListOptions } from "@/data/queries/members";
import { agentListOptions } from "@/data/queries/agents";
import { issueDetailOptions } from "@/data/queries/issues";
import { myIssueListOptions } from "@/data/queries/my-list";
import { useAuthStore } from "@/data/auth-store";
import { useWorkspaceStore } from "@/data/workspace-store";
import {
  selectViewedIssueIds,
  useViewedIssuesStore,
} from "@/data/viewed-issues-store";
import type { MentionMarker } from "@/lib/mention-serialize";
import { cn } from "@/lib/utils";
import { isAgentRuntimeBound } from "@/lib/is-agent-runtime-bound";

type Mode = "comment" | "chat";

type Row =
  | { kind: "section"; label: string }
  | { kind: "agent"; agent: Agent }
  | { kind: "issue"; issue: Issue }
  | { kind: "empty" };

interface Props {
  visible: boolean;
  query: string;
  onSelect: (mention: MentionMarker) => void;
  /** Default `"comment"` to preserve existing comment-composer and
   *  new-issue behaviour. `"chat"` switches the bar to issue mode. */
  mode?: Mode;
}

const RECENT_LIMIT = 5;
const CREATED_LIMIT = 10;

export function MentionSuggestionBar({
  visible,
  query,
  onSelect,
  mode = "comment",
}: Props) {
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const isChat = mode === "chat";
  // Rows are icon-only, so colour is the only thing that can carry a custom
  // status's identity here. (MUL-6243)
  const catalog = useIssueStatuses();
  const { t } = useT("issues");

  // Comment-mode data — disabled in chat mode to avoid wasted fetches. The
  // member list only supplies the viewer's role for the agent filter.
  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: !isChat && !!wsId,
  });
  const { data: agents = [] } = useQuery({
    ...agentListOptions(wsId),
    enabled: !isChat && !!wsId,
  });

  // Chat-mode data.
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const viewedIds = useViewedIssuesStore(selectViewedIssueIds(wsId));
  const recentIds = useMemo(
    () => viewedIds.slice(0, RECENT_LIMIT),
    [viewedIds],
  );
  const recentQueries = useQueries({
    queries: recentIds.map((id) => ({
      ...issueDetailOptions(wsId, id),
      enabled: isChat && !!wsId,
    })),
  });
  const recentIssues = useMemo<Issue[]>(
    () =>
      recentQueries
        .map((q) => q.data)
        .filter((i): i is Issue => !!i),
    [recentQueries],
  );

  const createdFilter = useMemo(
    () => ({ creator_id: userId ?? "" }),
    [userId],
  );
  const { data: createdIssues = [] } = useQuery({
    ...myIssueListOptions(wsId, createdFilter),
    enabled: isChat && !!wsId && !!userId,
  });

  const rows = useMemo<Row[]>(() => {
    const q = query.trim().toLowerCase();

    if (isChat) {
      const issueMatches = (i: Issue) =>
        !q ||
        i.identifier.toLowerCase().includes(q) ||
        i.title.toLowerCase().includes(q);

      const matchedRecent = recentIssues.filter(issueMatches);
      const recentIdSet = new Set(matchedRecent.map((i) => i.id));
      const matchedCreated = createdIssues
        .filter((i) => !recentIdSet.has(i.id) && issueMatches(i))
        .slice(0, CREATED_LIMIT);

      const out: Row[] = [];
      if (matchedRecent.length > 0) {
        out.push({ kind: "section", label: t("suggestions.recent") });
        for (const i of matchedRecent) out.push({ kind: "issue", issue: i });
      }
      if (matchedCreated.length > 0) {
        out.push({ kind: "section", label: t("suggestions.created_by_me") });
        for (const i of matchedCreated) out.push({ kind: "issue", issue: i });
      }
      if (out.length === 0) out.push({ kind: "empty" });
      return out;
    }

    // Comment mode.
    const myRole =
      members.find((m) => m.user_id === userId)?.role ?? null;
    // Agents: filter archived + drop ones the current user can't assign —
    // mirrors web (packages/views/editor/extensions/mention-suggestion.tsx).
    // A private agent shown in the suggestion list would create a mention the
    // assignee can never act on; web hides them, mobile must too.
    const matchedAgents = [...agents]
      .filter(
        (a) =>
          !a.archived_at &&
          isAgentRuntimeBound(a) &&
          (!q || a.name.toLowerCase().includes(q)) &&
          canAssignAgentToIssue(a, { userId, role: myRole }).allowed,
      )
      .sort((a, b) => a.name.localeCompare(b.name));

    const out: Row[] = [];
    if (matchedAgents.length > 0) {
      out.push({ kind: "section", label: t("picker.agents") });
      for (const a of matchedAgents) out.push({ kind: "agent", agent: a });
    }
    if (out.length === 0) out.push({ kind: "empty" });
    return out;
  }, [isChat, query, recentIssues, createdIssues, members, agents, userId, t]);

  if (!visible) return null;

  return (
    // Plain View — wrapping in reanimated `Animated.View` with `className`
    // silently drops every NativeWind utility (no `cssInterop` registered in
    // this repo for Animated.View), and `FadeInDown` left the bar at
    // `opacity:0` when mounted inside `KeyboardStickyView`. Entrance
    // animation is desirable but needs the chat-composer pattern (bare
    // Animated.View outer + className on inner View) — single follow-up.
    <View
      className="bg-background border-b border-border"
      style={{ maxHeight: 220 }}
    >
      <FlatList
        data={rows}
        keyboardShouldPersistTaps="handled"
        keyExtractor={(row, i) =>
          row.kind === "section"
            ? `row:section:${row.label}`
            : row.kind === "agent"
              ? `row:a:${row.agent.id}`
              : row.kind === "issue"
                ? `row:i:${row.issue.id}`
                : `row:empty:${i}`
        }
        renderItem={({ item, index }) => {
          if (item.kind === "section") {
            return (
              <View
                className={cn(
                  "px-3 pt-2 pb-1",
                  index > 0 && "border-t border-border/60 mt-1",
                )}
              >
                <Text className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-medium">
                  {item.label}
                </Text>
              </View>
            );
          }
          if (item.kind === "empty") {
            return (
              <View className="px-3 py-3">
                <Text className="text-xs text-muted-foreground">
                  {t("picker.no_matches")}
                </Text>
              </View>
            );
          }
          if (item.kind === "agent") {
            const runtimeBound = isAgentRuntimeBound(item.agent);
            return (
              <Pressable
                disabled={!runtimeBound}
                onPress={() =>
                  onSelect({
                    type: "agent",
                    id: item.agent.id,
                    name: item.agent.name,
                  })
                }
                className={cn(
                  "flex-row items-center gap-3 px-3 py-2 active:bg-secondary",
                  !runtimeBound && "opacity-50",
                )}
              >
                <ActorAvatar type="agent" id={item.agent.id} size={28} showPresence />
                <Text className="flex-1 text-sm text-foreground">
                  {item.agent.name}
                </Text>
                <Badge
                  label={runtimeBound
                    ? t("picker.agent")
                    : t("picker.needs_runtime")}
                  tone={runtimeBound ? "brand" : "outline"}
                />
              </Pressable>
            );
          }
          // issue
          // By CATEGORY, not by key: a custom status in the completed category is
          // done, and `status === "done"` silently disagrees — the row would
          // render at full opacity as though the work were still open.
          // (MUL-6243)
          const closed = issueBehavesAsAny(item.issue, CLOSED_CATEGORIES);
          return (
            <Pressable
              onPress={() =>
                onSelect({
                  type: "issue",
                  id: item.issue.id,
                  name: item.issue.identifier,
                })
              }
              className={cn(
                "flex-row items-center gap-3 px-3 py-2 active:bg-secondary",
                closed && "opacity-60",
              )}
            >
              <View className="size-7 items-center justify-center">
                <StatusIcon
                  status={item.issue.status}
                  category={issueColumnCategory(item.issue)}
                  icon={catalog.iconOf(item.issue.status)} color={catalog.colorOf(item.issue.status)}
                  size={16}
                />
              </View>
              <Text className="text-sm font-medium text-foreground">
                {item.issue.identifier}
              </Text>
              <Text
                className="flex-1 text-sm text-muted-foreground"
                numberOfLines={1}
              >
                {item.issue.title}
              </Text>
            </Pressable>
          );
        }}
      />
    </View>
  );
}

function Badge({
  label,
  tone,
}: {
  label: string;
  tone: "brand" | "outline";
}) {
  return (
    <View
      className={cn(
        "px-1.5 py-0.5 rounded-xs",
        tone === "brand" ? "bg-brand/10" : "border border-border",
      )}
    >
      <Text
        className={cn(
          "text-[10px] uppercase tracking-wide",
          tone === "brand" ? "text-brand" : "text-muted-foreground",
        )}
      >
        {label}
      </Text>
    </View>
  );
}
