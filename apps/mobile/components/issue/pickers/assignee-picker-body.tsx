/**
 * Pure picker body for issue assignee — single-select over the agents that
 * have a runtime, plus an "Unassigned" option. See status-picker-body.tsx
 * for the split rationale.
 *
 * Mirrors web `packages/views/issues/components/pickers/assignee-picker.tsx`
 * (mobile skips frequency-sort; alphabetical instead).
 *
 * Header + search bar are owned by the iOS native nav header registered in
 * `app/(app)/[workspace]/_layout.tsx` (assignee Stack.Screen sets
 * `headerShown: true` + `title`); the route file wires
 * `headerSearchBarOptions.onChangeText` to a local `query` state and passes
 * it in as the `query` prop. This body is just a FlatList — no chrome.
 */
import { useMemo } from "react";
import { FlatList, Pressable, View } from "react-native";
import { useQuery } from "@tanstack/react-query";
import { Ionicons } from "@expo/vector-icons";
import { useColorScheme } from "nativewind";
import type { Agent, IssueAssigneeType } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { ActorAvatar } from "@/components/ui/actor-avatar";
import { agentListOptions } from "@/data/queries/agents";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useScrollToTopOnChange } from "@/lib/use-scroll-to-top-on-change";
import { THEME } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { isAgentRuntimeBound } from "@/lib/is-agent-runtime-bound";
import { useT } from "@/lib/i18n";

const AVATAR_SIZE = 36;

export type AssigneeValue = {
  type: IssueAssigneeType;
  id: string;
} | null;

interface Props {
  value: AssigneeValue;
  query: string;
  onChange: (next: AssigneeValue) => void;
}

type Row = { kind: "unassigned" } | { kind: "agent"; agent: Agent };

function isRowSelected(value: AssigneeValue, row: Row): boolean {
  if (row.kind === "unassigned") return value === null;
  return value !== null && value.id === row.agent.id;
}

export function AssigneePickerBody({ value, query, onChange }: Props) {
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { t } = useT("issues");
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const listRef = useScrollToTopOnChange(query);
  const { colorScheme } = useColorScheme();
  // Tint color for the checkmark accessory. Project uses a monochrome
  // shadcn palette where `primary` is the canonical tint (near-black light /
  // near-white dark); matches Apple HIG's "tintColor" semantics for
  // selection accessories.
  const checkColor =
    colorScheme === "dark" ? THEME.dark.primary : THEME.light.primary;

  const rows = useMemo<Row[]>(() => {
    const q = query.trim().toLowerCase();
    const matchName = (name: string) => !q || name.toLowerCase().includes(q);

    const agentRows: Row[] = [...agents]
      .filter((a) => matchName(a.name))
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((a) => ({ kind: "agent" as const, agent: a }));

    // Pin the currently-selected agent right below Unassigned and remove it
    // from the list so it doesn't render twice. Apple HIG doesn't require
    // this — it's a product UX choice that speeds up the common "see who's
    // assigned + reassign nearby" path. Skipped when query is active because
    // search-result order should reflect matches, not state.
    const selectedRow = agentRows.find((r) => isRowSelected(value, r));
    return [
      { kind: "unassigned" },
      ...(selectedRow ? [selectedRow] : []),
      ...agentRows.filter((r) => !isRowSelected(value, r)),
    ];
  }, [agents, query, value]);

  const isSelected = (row: Row) => isRowSelected(value, row);

  const select = (row: Row) => {
    onChange(row.kind === "unassigned" ? null : { type: "agent", id: row.agent.id });
  };

  // FlatList is returned as the route's direct child so RNSScreenContentWrapper
  // can find it as a direct subview and apply the iOS formSheet header offset.
  // See react-native-screens#3634 — wrapping in a parent <View> hides the list
  // from the native search and the rows render at y=0, overlapping the header.
  return (
    <FlatList
      ref={listRef}
      data={rows}
      className="flex-1"
      keyboardShouldPersistTaps="handled"
      automaticallyAdjustKeyboardInsets
      contentInsetAdjustmentBehavior="automatic"
      keyExtractor={(row) => {
        return row.kind === "unassigned" ? "unassigned" : `a:${row.agent.id}`;
      }}
      renderItem={({ item }) => {
        const needsRuntime =
          item.kind === "agent" && !isAgentRuntimeBound(item.agent);
        return (
          <Pressable
          disabled={needsRuntime}
          onPress={() => select(item)}
          className={cn(
            "flex-row items-center gap-3 px-4 py-3 active:bg-secondary",
            needsRuntime && "opacity-50",
          )}
        >
          {item.kind === "unassigned" ? (
            <View
              className="rounded-full border border-dashed border-muted-foreground/40 items-center justify-center"
              style={{ width: AVATAR_SIZE, height: AVATAR_SIZE }}
            >
              <Text className="text-sm text-muted-foreground">∅</Text>
            </View>
          ) : (
            <ActorAvatar type="agent" id={item.agent.id} size={AVATAR_SIZE} />
          )}
          <Text className="flex-1 text-base text-foreground">
            {item.kind === "unassigned" ? t("picker.unassigned") : item.agent.name}
          </Text>
          {/* Right-aligned secondary label. Mirrors Apple's
              UITableViewCellStyleValue1 / UIListContentConfiguration.valueCell
              pattern used throughout iOS Settings — type tag in lighter font on
              the same row. */}
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
