/**
 * Workspace-level mention picker route — formSheet, opened from any
 * composer that has an `@` button (currently the issue-comment composer
 * and the chat composer).
 *
 * `?mode=` controls which sections render:
 *   - "comment" (default) — Agents + Issues.
 *   - "chat" — Issues only. Chat is user ↔ single agent, so an agent
 *     mention there is noise. Issues remain useful as "reference this
 *     ticket for the agent's context".
 *
 * Lives at workspace level (not nested under issue/[id]) because the chat
 * tab has no per-session route to nest under; making it workspace-level
 * keeps a single route file serving both contexts.
 */
import { useLocalSearchParams } from "expo-router";
import { MentionPickerBody } from "@/components/issue/pickers/mention-picker-body";
import { useNativeSearchBar } from "@/lib/use-native-search-bar";
import { useT } from "@/lib/i18n";

type Mode = "comment" | "chat";

export default function MentionPickerRoute() {
  const { mode: rawMode } = useLocalSearchParams<{ mode?: string }>();
  const { t } = useT("issues");
  const mode: Mode = rawMode === "chat" ? "chat" : "comment";
  const placeholder =
    mode === "chat"
      ? t("picker.reference_issue")
      : t("picker.search_agents_issues");
  const query = useNativeSearchBar(placeholder, { autoFocus: true });
  return <MentionPickerBody mode={mode} query={query} />;
}
