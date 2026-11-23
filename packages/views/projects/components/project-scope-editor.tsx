"use client";

import { useId, useState, type FocusEvent } from "react";
import { ArrowDown, ArrowUp, Plus, X } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  PROJECT_SCOPE_MAX_ITEMS,
  projectScopePairError,
  type ProjectScopePair,
  type ProjectScopePairError,
} from "@multica/core/projects";
import { cn } from "@multica/ui/lib/utils";
import { PICKER_TRIGGER_CLASS } from "../../issues/components/pickers/property-picker";
import { useT } from "../../i18n";
import { ProjectPicker } from "./project-picker";

type Translate = ReturnType<typeof useT<"projects">>["t"];

const COLUMNS = "grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]";

/**
 * The "project + branch" pairs a task works on, in their order.
 *
 * Controlled: it holds no pairs of its own and makes no request. A pair is
 * checked here by the rule the server applies, so a list that cannot be saved
 * says so before it is sent. A row that was just added and not filled in says
 * nothing; the list is still not valid until it is completed or removed.
 */
export function ProjectScopeEditor({
  value,
  onChange,
  disabled = false,
  busy = false,
}: {
  value: ProjectScopePair[];
  onChange: (next: ProjectScopePair[]) => void;
  /** Read-only: no pair can be added, changed, removed or moved. */
  disabled?: boolean;
  /** A save of these pairs is in flight. */
  busy?: boolean;
}) {
  const { t } = useT("projects");
  const baseId = useId();
  // The row being edited: the one that holds focus, or has its project menu
  // open (the menu renders outside the row, so focus alone cannot tell).
  const [active, setActive] = useState<number | null>(null);
  const [openPicker, setOpenPicker] = useState<number | null>(null);
  const locked = disabled || busy;
  const up = t(($) => $.scope.move_up);
  const down = t(($) => $.scope.move_down);
  const removeLabel = t(($) => $.scope.remove);
  const atLimit = value.length >= PROJECT_SCOPE_MAX_ITEMS;

  const set = (index: number, patch: Partial<ProjectScopePair>) =>
    onChange(value.map((pair, i) => (i === index ? { ...pair, ...patch } : pair)));

  const add = () => {
    const index = value.length;
    onChange([...value, { project_id: "", branch: "" }]);
    // A new pair starts with its project, so the menu opens on the new row.
    setActive(index);
    setOpenPicker(index);
  };

  const remove = (index: number) => {
    onChange(value.filter((_, i) => i !== index));
    setActive(null);
    setOpenPicker(null);
  };

  const move = (index: number, to: number) => {
    const next = [...value];
    next[index] = next[to]!;
    next[to] = value[index]!;
    onChange(next);
    // The row moved, and what marked it moved with it.
    setActive(to);
    setOpenPicker((current) => (current === index ? to : current));
  };

  const trackFocus = (index: number) => ({
    onFocus: () => setActive(index),
    onBlur: (event: FocusEvent<HTMLLIElement>) => {
      // Focus passing between two controls of the row leaves the row active.
      if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
        setActive((current) => (current === index ? null : current));
      }
    },
  });

  return (
    <div className="space-y-2" aria-busy={busy || undefined}>
      <div className={cn(COLUMNS, "grid items-center gap-2 px-1")}>
        <span className="px-2 text-caption font-medium text-muted-foreground">
          {t(($) => $.scope.project_label)}
        </span>
        <span className="px-2 text-caption font-medium text-muted-foreground">
          {t(($) => $.scope.branch_label)}
        </span>
        <div className="flex items-center justify-end gap-2">
          {atLimit ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.scope.limit, { max: PROJECT_SCOPE_MAX_ITEMS })}
            </p>
          ) : null}
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={locked || atLimit}
            onClick={add}
          >
            <Plus aria-hidden="true" className="size-3.5" />
            {t(($) => $.scope.add)}
          </Button>
        </div>
      </div>
      {value.length > 0 ? (
        <ul className="space-y-1">
          {value.map((pair, index) => {
            const error =
              pair.project_id === "" && pair.branch === ""
                ? null
                : projectScopePairError(value, index);
            const errorId = `${baseId}-${index}-error`;
            const branchFaulty = error !== null && error !== "project_required";
            const marked = active === index || openPicker === index;
            return (
              <li
                key={index}
                {...trackFocus(index)}
                // A busy row, and the row being edited, keep their tint under
                // the pointer: neither state may vanish on hover.
                className={cn(
                  COLUMNS,
                  "grid items-center gap-2 rounded-md px-1 py-1 transition-colors",
                  busy
                    ? "bg-muted hover:bg-muted"
                    : marked
                      ? "bg-surface-selected hover:bg-surface-selected"
                      : "hover:bg-surface-hover",
                )}
              >
                <div className="min-w-0">
                  <ProjectPicker
                    projectId={pair.project_id}
                    onUpdate={(updates) =>
                      set(index, { project_id: updates.project_id ?? "" })
                    }
                    disabled={locked}
                    open={openPicker === index}
                    onOpenChange={(open) => setOpenPicker(open ? index : null)}
                    triggerRender={
                      <button
                        type="button"
                        disabled={locked}
                        aria-invalid={error === "project_required"}
                        aria-describedby={error === "project_required" ? errorId : undefined}
                        className={cn(PICKER_TRIGGER_CLASS, "w-full justify-start")}
                      />
                    }
                  />
                </div>
                <Input
                  value={pair.branch}
                  onChange={(event) => set(index, { branch: event.target.value })}
                  disabled={locked}
                  aria-label={t(($) => $.scope.branch_label)}
                  aria-invalid={branchFaulty}
                  aria-describedby={branchFaulty ? errorId : undefined}
                  placeholder={t(($) => $.resources.ref_placeholder)}
                  spellCheck={false}
                  autoCapitalize="none"
                  autoCorrect="off"
                  className="font-mono"
                />
                {/* A list holds up to 50 rows. Named buttons with a native
                    title rather than tooltip roots: three per row would mount
                    150 of them for a full list. */}
                <div className="flex items-center">
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    aria-label={up}
                    title={up}
                    disabled={locked || index === 0}
                    onClick={() => move(index, index - 1)}
                  >
                    <ArrowUp />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    aria-label={down}
                    title={down}
                    disabled={locked || index === value.length - 1}
                    onClick={() => move(index, index + 1)}
                  >
                    <ArrowDown />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    aria-label={removeLabel}
                    title={removeLabel}
                    disabled={locked}
                    onClick={() => remove(index)}
                  >
                    <X />
                  </Button>
                </div>
                {error ? (
                  <p
                    id={errorId}
                    className="col-span-3 px-2 pb-1 text-micro text-destructive"
                  >
                    {pairMessage(error, t)}
                  </p>
                ) : null}
              </li>
            );
          })}
        </ul>
      ) : null}
    </div>
  );
}

function pairMessage(error: ProjectScopePairError, t: Translate): string {
  switch (error) {
    case "project_required":
      return t(($) => $.scope.project_required);
    case "branch_required":
      return t(($) => $.scope.branch_required);
    case "branch_invalid_characters":
      return t(($) => $.resources.ref_error_characters);
    case "branch_invalid_format":
      return t(($) => $.resources.ref_error_format);
    case "branch_too_long":
      return t(($) => $.resources.ref_error_too_long);
    case "duplicate":
      return t(($) => $.scope.duplicate);
  }
}
