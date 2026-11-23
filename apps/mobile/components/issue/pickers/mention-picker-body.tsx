/**
 * Pure picker body for the comment composer's @mention chips.
 *
 * Mirrors `LabelPickerBody`: multi-select with tap-to-toggle, sheet stays
 * open across toggles, user dismisses via grabber drag-down or Back. The
 * composer's chip row reflects the store live (sheet is presented over
 * the composer; the row is partly visible behind the sheet).
 *
 * Sections (alphabetical within each):
 *   1. Agents
 *   2. Issues (server-side `api.searchIssues`, debounced 200ms; empty
 *      query → no issues section, matching web's mention-suggestion.tsx)
 *
 * Mobile is the iOS-native equivalent of shadcn's `CommandDialog` — search
 * input from the native UISearchController (registered by the parent
 * route via `useNativeSearchBar`), groups via uppercase section labels,
 * empty state inline.
 */
import { useEffect, useMemo, useState } from "react";
import { FlatList, Pressable, View } from "react-native";
import { useQuery } from "@tanstack/react-query";
import { Ionicons } from "@expo/vector-icons";
import { useColorScheme } from "nativewind";
import type { Agent, Issue } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { ActorAvatar } from "@/components/ui/actor-avatar";
import { StatusIcon } from "@/components/ui/status-icon";
import { issueColumnCategory } from "@/lib/issue-status";
import { useIssueStatuses } from "@/lib/use-issue-statuses";
import { agentListOptions } from "@/data/queries/agents";
import { api } from "@/data/api";
import { useWorkspaceStore } from "@/data/workspace-store";
import {
  useMentionDraftStore,
  type MentionChipDraft,
  type MentionTargetType,
} from "@/data/stores/mention-draft-store";
import { useScrollToTopOnChange } from "@/lib/use-scroll-to-top-on-change";
import { THEME } from "@/lib/theme";
import { isAgentRuntimeBound } from "@/lib/is-agent-runtime-bound";
import { commentMentionCandidates } from "@/lib/comment-mention-candidates";
import { cn } from "@/lib/utils";
import { useT } from "@/lib/i18n";

const AVATAR_SIZE = 36;

type Row =
  | { kind: "section"; label: string }
  | { kind: "agent"; agent: Agent }
  | { kind: "issue"; issue: Issue };

interface Props {
  query: string;
  /** "comment" (default) renders Agents + Issues. "chat" leaves only the
   *  Issues section: chat is user ↔ single agent, so an agent mention
   *  there is noise. An issue stays useful as "reference this ticket for
   *  context". */
  mode?: "comment" | "chat";
}

export function MentionPickerBody({ query, mode = "comment" }: Props) {
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { t } = useT("issues");
  // Rows are icon-only here too — colour is what names a custom status.
  const catalog = useIssueStatuses();
  const { data: agents = [] } = useQuery({
    ...agentListOptions(wsId),
    enabled: !!wsId,
  });
  const listRef = useScrollToTopOnChange(query);
  const { colorScheme } = useColorScheme();
  const checkColor =
    colorScheme === "dark" ? THEME.dark.primary : THEME.light.primary;

  const selected = useMentionDraftStore((s) => s.mentions);
  const toggle = useMentionDraftStore((s) => s.toggle);

  // Server-side issue search (mirrors web's mention-suggestion.tsx). Empty
  // query → no fetch + no issues section. Debounced 200ms; in-flight
  // cancelled on every keystroke via AbortController.
  const [searchResult, setSearchResult] = useState<{ query: string; issues: Issue[] }>({
    query: "",
    issues: [],
  });
  const issueResults = useMemo(
    () => searchResult.query === query.trim() ? searchResult.issues : [],
    [searchResult, query],
  );
  useEffect(() => {
    const trimmed = query.trim();
    if (!trimmed) return;
    const ac = new AbortController();
    const timer = setTimeout(() => {
      void api
        .searchIssues(
          { q: trimmed, limit: 8, include_closed: false },
          { signal: ac.signal },
        )
        .then((res) => {
          if (!ac.signal.aborted) setSearchResult({ query: trimmed, issues: res.issues });
        })
        .catch(() => {
          if (!ac.signal.aborted) setSearchResult({ query: trimmed, issues: [] });
        });
    }, 200);
    return () => {
      ac.abort();
      clearTimeout(timer);
    };
  }, [query]);

  const isSelectedKey = (type: MentionTargetType, id: string) =>
    selected.some((m) => m.type === type && m.id === id);

  const isSelected = (row: Row): boolean => {
    if (row.kind === "section") return false;
    if (row.kind === "agent") return isSelectedKey("agent", row.agent.id);
    return isSelectedKey("issue", row.issue.id);
  };

  const rows = useMemo<Row[]>(() => {
    const out: Row[] = [];

    // The agent section only renders in comment mode. Chat is single-agent;
    // an @agent mention there is noise. The Issues section IS useful in
    // chat ("reference ticket for context"), so it stays for both modes.
    if (mode === "comment") {
      const agentRows = commentMentionCandidates({ query, agents });
      if (agentRows.length > 0) {
        out.push(
          { kind: "section", label: t("picker.agents") },
          ...agentRows.map((a): Row => ({ kind: "agent", agent: a })),
        );
      }
    }

    if (issueResults.length > 0) {
      out.push({ kind: "section", label: t("picker.issues") });
      for (const i of issueResults) {
        out.push({ kind: "issue", issue: i });
      }
    }
    return out;
  }, [mode, agents, issueResults, query, t]);

  const pick = (row: Row) => {
    let chip: MentionChipDraft | null = null;
    if (row.kind === "agent")
      chip = { type: "agent", id: row.agent.id, name: row.agent.name };
    else if (row.kind === "issue")
      chip = {
        type: "issue",
        id: row.issue.id,
        name: row.issue.identifier,
      };
    if (chip) toggle(chip);
  };

  // FlatList returned as the route's direct child so RNSScreenContentWrapper
  // can find it as a direct subview and apply the iOS formSheet header
  // offset. See react-native-screens#3634.
  return (
    <FlatList
      ref={listRef}
      data={rows}
      className="flex-1"
      keyboardShouldPersistTaps="handled"
      automaticallyAdjustKeyboardInsets
      contentInsetAdjustmentBehavior="automatic"
      keyExtractor={(row, idx) => {
        if (row.kind === "section") return `section:${row.label}:${idx}`;
        if (row.kind === "agent") return `a:${row.agent.id}`;
        return `i:${row.issue.id}`;
      }}
      renderItem={({ item }) => {
        if (item.kind === "section") {
          return (
            <View className="px-4 pt-4 pb-1">
              <Text className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                {item.label}
              </Text>
            </View>
          );
        }
        const needsRuntime =
          item.kind === "agent" && !isAgentRuntimeBound(item.agent);
        return (
          <Pressable
            disabled={needsRuntime}
            onPress={() => pick(item)}
            className={cn(
              "flex-row items-center gap-3 px-4 py-3 active:bg-secondary",
              needsRuntime && "opacity-50",
            )}
          >
            {item.kind === "agent" ? (
              <ActorAvatar type="agent" id={item.agent.id} size={AVATAR_SIZE} />
            ) : (
              <View
                className="items-center justify-center"
                style={{ width: AVATAR_SIZE, height: AVATAR_SIZE }}
              >
                <StatusIcon
                  status={item.issue.status}
                  category={issueColumnCategory(item.issue)}
                  icon={catalog.iconOf(item.issue.status)} color={catalog.colorOf(item.issue.status)}
                  size={22}
                />
              </View>
            )}
            {item.kind === "issue" ? (
              <View className="flex-1 flex-row items-center gap-2">
                <Text className="text-sm font-medium text-muted-foreground">
                  {item.issue.identifier}
                </Text>
                <Text
                  className="flex-1 text-base text-foreground"
                  numberOfLines={1}
                >
                  {item.issue.title}
                </Text>
              </View>
            ) : (
              <Text className="flex-1 text-base text-foreground">
                {item.agent.name}
              </Text>
            )}
            {item.kind === "agent" ? (
              <Text className="text-sm text-muted-foreground">
                {isAgentRuntimeBound(item.agent)
                  ? t("picker.agent")
                  : t("picker.needs_runtime")}
              </Text>
            ) : null}
            {isSelected(item) ? (
              <Ionicons name="checkmark" size={20} color={checkColor} />
            ) : null}
          </Pressable>
        );
      }}
      ListEmptyComponent={
        <View className="px-3 py-8 items-center">
          <Text className="text-sm text-muted-foreground">
            {t("picker.no_matches")}
          </Text>
        </View>
      }
    />
  );
}
