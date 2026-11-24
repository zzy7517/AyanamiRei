"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ArrowRight,
  Copy,
  FolderGit2,
  RotateCcw,
  SquareTerminal,
  Terminal,
  Trash2,
  TriangleAlert,
} from "@multica/ui/icons";
import {
  useIssueProjectScope,
  useRebuildIssueProjectTree,
  useReplaceIssueProjectScope,
} from "@multica/core/issues";
import { TreeFailure, useDeleteIssueProjectTree } from "@multica/core/tasktrees";
import { clientErrorMessage } from "@multica/core/api";
import { EMPTY_ISSUE_PROJECT_SCOPE } from "@multica/core/api/schemas";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  isProjectScopeValid,
  projectListOptions,
  useProjectFolders,
  type ProjectScopePair,
} from "@multica/core/projects";
import { useWorkspacePaths, type WorkspacePaths } from "@multica/core/paths";
import type { IssueProjectTreeState, ProjectScopeItem } from "@multica/core/types";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import { IconButton } from "@multica/ui/components/common/icon-button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { copyText } from "@multica/ui/lib/clipboard";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";
import { ProjectScopeEditor } from "../../projects/components/project-scope-editor";
import { IssueSidebarSectionHeader } from "./issue-sidebar-section";

/**
 * The server's words for a refusal it wrote for the user, or `fallback`. The
 * reason a tree was kept is such words too, written where the tree stands.
 */
function refusal(err: unknown, fallback: string): string {
  return clientErrorMessage(err) ?? (err instanceof TreeFailure ? err.message : fallback);
}

/** Where a row's text and actions start: under the project's name, past its icon. */
const ROW_INDENT = "pl-[22px]";

/**
 * The trees of a task, one per "project + branch" pair: whether each is
 * built, where it is on its machine, and what can be done in and with it. A
 * task without pairs takes them here.
 *
 * The list is the server's: nothing here changes before the server has
 * accepted a change, and it is read again after every one.
 */
export function IssueTreesSection({ issueId }: { issueId: string }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const scope = useIssueProjectScope(wsId, issueId);
  const [open, setOpen] = useState(true);
  const [deleting, setDeleting] = useState<{ treeId: string; project: string; branch: string } | null>(null);
  const items = scope.data.items;
  // A scope that could not be read, because the request failed or the answer
  // was malformed, must not offer the editor: its save replaces the whole list,
  // pairs included, that merely could not be shown.
  const unreadable = scope.isError || scope.data === EMPTY_ISSUE_PROJECT_SCOPE;
  // The project picker reads the same list, so it is usually cached already.
  const { data: projects } = useQuery(projectListOptions(wsId));
  const projectTitle = (id: string) => projects?.find((project) => project.id === id)?.title ?? id;

  return (
    <div>
      <IssueSidebarSectionHeader
        title={t(($) => $.trees.section_title)}
        open={open}
        onOpenChange={setOpen}
        count={items.length}
      />
      {open && (
        <div className="px-2" aria-busy={scope.isLoading || undefined}>
          {scope.isLoading ? (
            <div className="space-y-2 px-2 py-1.5">
              <Skeleton className="h-4 w-3/5" />
              <Skeleton className="h-3.5 w-2/5" />
            </div>
          ) : unreadable ? (
            <button
              type="button"
              onClick={() => void scope.refetch()}
              className="flex min-h-8 w-full items-center rounded-md px-2 py-1.5 text-left text-label text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
            >
              {t(($) => $.detail.section_load_failed_retry)}
            </button>
          ) : items.length === 0 ? (
            <FirstPairs issueId={issueId} />
          ) : (
            <ul className="space-y-1">
              {items.map((item) => (
                <TreeRow
                  key={item.tree?.id ?? `${item.project_id}:${item.base_branch}`}
                  issueId={issueId}
                  item={item}
                  project={projectTitle(item.project_id)}
                  projectId={item.project_id}
                  onDelete={() =>
                    setDeleting({
                      treeId: item.tree!.id,
                      project: projectTitle(item.project_id),
                      branch: item.tree?.task_branch || item.base_branch,
                    })
                  }
                />
              ))}
            </ul>
          )}
        </div>
      )}
      {deleting && <DeleteTreeDialog issueId={issueId} tree={deleting} onClose={() => setDeleting(null)} />}
    </div>
  );
}

/** A task's first pairs. The editor checks each by the server's own rule. */
function FirstPairs({ issueId }: { issueId: string }) {
  const { t } = useT("issues");
  const { t: tProjects } = useT("projects");
  const wsId = useWorkspaceId();
  const replace = useReplaceIssueProjectScope(wsId, issueId);
  const [pairs, setPairs] = useState<ProjectScopePair[]>([]);

  const save = async () => {
    // A refusal stays by the button; `replace.isError` renders its reason.
    await replace
      .mutateAsync({ items: pairs.map(({ project_id, branch }) => ({ project_id, base_branch: branch })) })
      .catch(() => undefined);
  };

  return (
    <div role="group" aria-label={tProjects(($) => $.scope.section_title)} className="px-2 py-1.5">
      <ProjectScopeEditor value={pairs} onChange={setPairs} busy={replace.isPending} />
      <div className="mt-2 flex items-center justify-end gap-2">
        {replace.isError && (
          <p role="alert" className="min-w-0 wrap-anywhere text-caption text-destructive">
            {refusal(replace.error, t(($) => $.trees.save_failed))}
          </p>
        )}
        <Button
          size="xs"
          disabled={pairs.length === 0 || !isProjectScopeValid(pairs) || replace.isPending}
          aria-busy={replace.isPending || undefined}
          onClick={() => void save()}
        >
          {tProjects(($) => $.scope.save)}
        </Button>
      </div>
    </div>
  );
}

function TreeRow({
  issueId,
  item,
  project,
  projectId,
  onDelete,
}: {
  issueId: string;
  item: ProjectScopeItem;
  /** The project's title, or its id until the list of projects has loaded. */
  project: string;
  projectId: string;
  onDelete: () => void;
}) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const wsId = useWorkspaceId();
  const { folders } = useProjectFolders(wsId);
  const tree = item.tree;
  const state = tree?.state ?? null;
  // A tree is where its project's folder is. Until the folders are read it
  // reads as on this computer, which is what a shell needs to be told anyway.
  const place = folders.find(({ project }) => project.id === projectId)?.place;
  const host = place?.kind === "ssh" ? place.host : undefined;

  return (
    <li className="rounded-md px-2 py-1.5 transition-colors hover:bg-accent/50">
      <div className="flex min-w-0 items-center gap-2">
        <FolderGit2 aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="min-w-0 flex-1 truncate text-label font-medium" title={project}>
          {project}
        </span>
        {state && <TreeState state={state} />}
      </div>
      <div className={`mt-1 flex min-w-0 items-center gap-1 ${ROW_INDENT}`}>
        <span className="min-w-0 truncate font-mono text-caption">
          <span className="text-muted-foreground">{item.base_branch}</span>
          {tree?.task_branch && (
            <>
              <ArrowRight aria-hidden="true" className="inline size-3 text-faint-foreground" />
              {tree.task_branch}
            </>
          )}
        </span>
        {tree?.task_branch && (
          <CopyButton
            value={tree.task_branch}
            label={t(($) => $.trees.copy_task_branch)}
          />
        )}
      </div>
      {state === "ready" && tree?.path && (
        <div className={`mt-0.5 flex min-w-0 items-center gap-1 ${ROW_INDENT}`}>
          <code className="min-w-0 truncate font-mono text-caption text-muted-foreground" title={tree.path}>
            {tree.path}
          </code>
          <CopyButton value={tree.path} label={t(($) => $.trees.copy_path)} />
        </div>
      )}
      {state === "failed" && tree?.error && (
        <p className={`mt-1 wrap-anywhere whitespace-pre-wrap text-caption text-destructive ${ROW_INDENT}`}>{tree.error}</p>
      )}
      {/* Nothing can be done with a tree while it is being built. */}
      {tree && tree.state !== "pending" && (
        <TreeActions
          issueId={issueId}
          tree={tree}
          state={tree.state}
          host={host}
          paths={paths}
          onDelete={onDelete}
        />
      )}
    </li>
  );
}

function TreeState({ state }: { state: IssueProjectTreeState }) {
  const { t } = useT("issues");
  switch (state) {
    case "ready":
      return (
        <span className="inline-flex shrink-0 items-center gap-1 text-caption text-success">
          <span aria-hidden="true" className="size-1.5 rounded-full bg-success" />
          {t(($) => $.trees.state.ready)}
        </span>
      );
    case "failed":
      return (
        <span className="inline-flex shrink-0 items-center gap-1 text-caption text-destructive">
          <TriangleAlert aria-hidden="true" className="size-3" />
          {t(($) => $.trees.state.failed)}
        </span>
      );
    case "deleted":
      return (
        <span className="inline-flex shrink-0 items-center gap-1 text-caption text-muted-foreground">
          <Trash2 aria-hidden="true" className="size-3" />
          {t(($) => $.trees.state.deleted)}
        </span>
      );
    case "pending":
      return (
        <span className="inline-flex shrink-0 items-center gap-1 text-caption text-muted-foreground">
          <Spinner aria-hidden="true" className="size-3" />
          {t(($) => $.trees.state.pending)}
        </span>
      );
  }
}

/** What can be done with the tree in its state. Nothing while it is being built. */
function TreeActions({
  issueId,
  tree,
  state,
  host,
  paths,
  onDelete,
}: {
  issueId: string;
  tree: NonNullable<ProjectScopeItem["tree"]>;
  state: IssueProjectTreeState;
  /** The SSH host the tree is on, when it is not on this computer. */
  host: string | undefined;
  paths: WorkspacePaths;
  onDelete: () => void;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const rebuild = useRebuildIssueProjectTree(wsId, issueId);
  // Only a built tree is a directory: terminals open in it, and deleting it removes it.
  const built = state === "ready";

  const rebuildTree = async () => {
    // A refusal names its reason; the tree's next state is read with the scope.
    await rebuild.mutateAsync(tree.id).catch((err: unknown) => {
      toast.error(refusal(err, t(($) => $.trees.rebuild_failed)));
    });
  };

  return (
    <div className={`mt-1.5 flex items-center gap-1 ${ROW_INDENT}`}>
      {built ? (
        <>
          <AppLink
            href={paths.terminalAgent(issueId, tree.id)}
            className={buttonVariants({ variant: "outline", size: "xs" })}
          >
            <SquareTerminal />
            {t(($) => $.trees.open_agent_terminal)}
          </AppLink>
          {tree.path && (
            <AppLink
              href={paths.terminalIn(tree.path, { host })}
              className={buttonVariants({ variant: "outline", size: "xs" })}
            >
              <Terminal />
              {t(($) => $.trees.open_shell)}
            </AppLink>
          )}
          <IconButton variant="ghost" size="icon-xs" label={t(($) => $.trees.delete)} onClick={onDelete}>
            <Trash2 />
          </IconButton>
        </>
      ) : (
        <Button
          variant="outline"
          size="xs"
          disabled={rebuild.isPending}
          aria-busy={rebuild.isPending || undefined}
          onClick={() => void rebuildTree()}
        >
          <RotateCcw />
          {t(($) => $.trees.rebuild)}
        </Button>
      )}
    </div>
  );
}

function CopyButton({ value, label }: { value: string; label: string }) {
  const { t } = useT("issues");
  return (
    <IconButton
      variant="ghost"
      size="icon-xs"
      label={label}
      className="shrink-0 text-muted-foreground"
      onClick={() => {
        void copyText(value).then((ok) => {
          if (ok) toast.success(t(($) => $.trees.copied));
          else toast.error(t(($) => $.trees.copy_failed));
        });
      }}
    >
      <Copy />
    </IconButton>
  );
}

function DeleteTreeDialog({
  issueId,
  tree,
  onClose,
}: {
  issueId: string;
  tree: { treeId: string; project: string; branch: string };
  onClose: () => void;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const remove = useDeleteIssueProjectTree(wsId, issueId);
  const [failed, setFailed] = useState<string | null>(null);

  const confirm = async () => {
    setFailed(null);
    // The row keeps its tree until the server has deleted it.
    await remove.mutateAsync(tree.treeId).then(
      () => onClose(),
      (err: unknown) => setFailed(refusal(err, t(($) => $.trees.delete_failed))),
    );
  };

  return (
    <AlertDialog open onOpenChange={(open) => !open && !remove.isPending && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(($) => $.trees.delete)}</AlertDialogTitle>
          <AlertDialogDescription>{t(($) => $.trees.delete_description)}</AlertDialogDescription>
        </AlertDialogHeader>
        <p className="wrap-anywhere text-label font-medium">
          {tree.project}
          <span className="ml-2 font-mono font-normal text-muted-foreground">{tree.branch}</span>
        </p>
        {failed && (
          <p role="alert" className="wrap-anywhere text-caption text-destructive">
            {failed}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={remove.isPending}>{t(($) => $.trees.cancel)}</AlertDialogCancel>
          <Button
            variant="destructive"
            disabled={remove.isPending}
            aria-busy={remove.isPending || undefined}
            onClick={() => void confirm()}
          >
            {t(($) => $.trees.delete_confirm)}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
