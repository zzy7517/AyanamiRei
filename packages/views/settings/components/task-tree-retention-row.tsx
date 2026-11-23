"use client";

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Input } from "@multica/ui/components/ui/input";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import type { Workspace } from "@multica/core/types";
import {
  parseTaskTreeRetentionDays,
  TASK_TREE_RETENTION_DAYS_KEY,
  TASK_TREE_RETENTION_DAYS_MAX,
  taskTreeRetentionDays,
} from "@multica/core/workspace";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { useT } from "../../i18n";
import { SettingsRow } from "./settings-layout";

/**
 * How many days a completed task keeps its worktrees in this workspace. The
 * edit is saved when the field is left: a whole number of days is stored
 * with the other workspace settings, anything else puts the saved value
 * back. The server removes worktrees on this number.
 */
export function TaskTreeRetentionRow({ canManage }: { canManage: boolean }) {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const qc = useQueryClient();
  const saved = taskTreeRetentionDays(workspace);
  // What was typed, from the first keystroke until the edit is over.
  const [draft, setDraft] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  async function commit(raw: string) {
    const days = parseTaskTreeRetentionDays(raw);
    if (days === null || days === saved || !workspace) {
      setDraft(null);
      return;
    }
    setSaving(true);
    try {
      const updated = await api.updateWorkspace(workspace.id, {
        settings: {
          ...((workspace.settings as Record<string, unknown>) ?? {}),
          [TASK_TREE_RETENTION_DAYS_KEY]: days,
        },
      });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.auto_save.failed));
    } finally {
      // The saved value shows again: the new one once it arrived, the old
      // one when the save failed.
      setSaving(false);
      setDraft(null);
    }
  }

  return (
    <SettingsRow
      anchor="task-tree-retention"
      label={t(($) => $.code.task_tree_retention.label)}
      description={t(($) => $.code.task_tree_retention.description)}
    >
      <div className="flex items-center gap-1.5">
        <Input
          type="number"
          aria-label={t(($) => $.code.task_tree_retention.label)}
          className="h-8 w-24 text-right tabular-nums"
          min={1}
          max={TASK_TREE_RETENTION_DAYS_MAX}
          step={1}
          value={draft ?? String(saved)}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={(event) => void commit(event.target.value)}
          disabled={!canManage || saving}
          aria-busy={saving || undefined}
        />
        <span className="text-label text-muted-foreground">
          {t(($) => $.code.task_tree_retention.unit)}
        </span>
      </div>
    </SettingsRow>
  );
}
