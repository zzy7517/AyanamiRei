"use client";

/**
 * A terminal in a project's folder, or the agent's command line in a tree of a
 * task. The desktop app owns the terminal: it keeps running while the page is
 * gone, and a page that names it shows it again.
 *
 * The URL says what the page shows. `?project=<project>` opens a shell in a
 * project's folder: the default project, which is there without adding one, or
 * a folder a project registered, named by the id of that folder.
 * `?cwd=<directory>` opens one in a directory of a task, which is where the
 * task page's Shell link leads; `&host=<host>` says it is on an SSH host.
 * `?issue=<task id>&tree=<tree id>` opens an agent terminal in that tree.
 * `&t=<terminal id>` rejoins a terminal that is already open, which is how a
 * desktop tab finds its terminal again after a switch. The id is written once
 * the app names the terminal and stays after the process has ended, so
 * returning to the page does not quietly open another shell or start the agent
 * again.
 *
 * With none of these the page lists the projects a terminal can open in.
 */

import { useQuery } from "@tanstack/react-query";
import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueDetailOptions, useIssueProjectScope, useIssueShown } from "@multica/core/issues";
import { useModalStore } from "@multica/core/modals";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  projectFolderLabel,
  projectFolderPlace,
  projectListOptions,
  projectResourcesOptions,
  useProjectFolders,
  type ProjectFolder,
} from "@multica/core/projects";
import {
  TERMINAL_DEFAULT_PROJECT,
  useTerminalSession,
  useTerminalSessionState,
  type AgentTerminalTree,
  type TerminalHostTarget,
  type TerminalPlace,
  type TerminalStatus,
} from "@multica/core/terminal";
import { Button } from "@multica/ui/components/ui/button";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { ArrowLeft, FolderOpen } from "@multica/ui/icons";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { useMarkAgentReplySeenWhileViewing } from "../issues/hooks/use-mark-agent-reply-seen-while-viewing";
import { PAGE_GUTTER, PageHeader } from "../layout/page-header";
import { AppLink, useNavigation } from "../navigation";
import { Terminal, type TerminalHandle } from "./terminal";

/** The page's address with the id of the terminal it shows. */
function withTerminalId(address: string, terminalId: string): string {
  return `${address}${address.includes("?") ? "&" : "?"}t=${encodeURIComponent(terminalId)}`;
}

export function TerminalPage() {
  const { t } = useT("terminal");
  const navigation = useNavigation();
  const workspacePaths = useWorkspacePaths();
  const project = navigation.searchParams.get("project")?.trim() ?? "";
  const directory = navigation.searchParams.get("cwd")?.trim() ?? "";
  const host = navigation.searchParams.get("host")?.trim() ?? "";
  const issueId = navigation.searchParams.get("issue")?.trim() ?? "";
  const treeId = navigation.searchParams.get("tree")?.trim() ?? "";
  // A tree belongs to one task: an address with only one of the two opens nothing.
  const isAgent = Boolean(issueId && treeId);
  // An id on its own says which terminal to rejoin, but not what it is for.
  const terminalId =
    isAgent || project || directory ? navigation.searchParams.get("t") || undefined : undefined;
  // The viewer is watching this agent, so its replies are seen as they land:
  // none is left marked, and none is announced over the terminal showing it.
  const wsId = useWorkspaceId();
  const shownIssueId = isAgent ? issueId : null;
  useIssueShown(shownIssueId);
  useMarkAgentReplySeenWhileViewing(wsId, shownIssueId ?? "");

  const back = { label: t(($) => $.all_projects), to: workspacePaths.terminal() };

  if (isAgent) {
    return (
      <AgentTerminal
        issueId={issueId}
        treeId={treeId}
        terminalId={terminalId}
        address={workspacePaths.terminalAgent(issueId, treeId)}
      />
    );
  }
  if (project === TERMINAL_DEFAULT_PROJECT) {
    return (
      <TerminalFor
        target={{ kind: "defaultProject" }}
        sessionKey="default-project"
        terminalId={terminalId}
        address={workspacePaths.terminalProject(project)}
        back={back}
        title={<span className="min-w-0 truncate text-body font-medium">{t(($) => $.default_project)}</span>}
      />
    );
  }
  if (project) return <ProjectTerminal project={project} terminalId={terminalId} back={back} />;
  if (directory) {
    const place: TerminalPlace = host ? { kind: "ssh", host } : { kind: "local" };
    return (
      <TerminalFor
        target={{ kind: "shell", place, cwd: directory }}
        sessionKey={`shell:${host}:${directory}`}
        terminalId={terminalId}
        address={workspacePaths.terminalIn(directory, { host: host || undefined })}
        title={
          <span className="min-w-0 truncate font-mono text-caption text-muted-foreground">
            {host ? `${host}:${directory}` : directory}
          </span>
        }
      />
    );
  }
  return <ProjectList />;
}

/** The list a project's terminal opens from, and the way back to it. */
interface BackToList {
  label: string;
  to: string;
}

/**
 * A page for one terminal: the header says what it is for and holds its
 * controls, and the terminal fills the rest.
 */
function TerminalFrame({
  title,
  back,
  live,
  controls,
  children,
}: {
  /** What the header says about the terminal. */
  title: ReactNode;
  back?: BackToList;
  /**
   * The terminal is opening or running. The way back to the list is closed
   * then: leaving would leave the shell behind, still running, with no page
   * that leads to it.
   */
  live?: boolean;
  /** The header's buttons. */
  controls?: ReactNode;
  children?: ReactNode;
}) {
  const navigation = useNavigation();
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader className="gap-3">
        <div className="flex min-w-0 flex-1 items-center gap-2">
          {back && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="-ml-2 shrink-0 gap-1.5 text-muted-foreground"
              disabled={live}
              onClick={() => navigation.replace(back.to)}
            >
              <ArrowLeft className="h-4 w-4" />
              {back.label}
            </Button>
          )}
          {title}
        </div>
        {controls}
      </PageHeader>
      {children}
    </div>
  );
}

function TerminalFor({
  target,
  sessionKey,
  terminalId,
  address,
  back,
  title,
}: {
  target: TerminalHostTarget;
  /** What keeps this page's terminal across renders. */
  sessionKey: string;
  terminalId?: string;
  /** The page's address for the target, without the id of a terminal. */
  address: string;
  back?: BackToList;
  title: ReactNode;
}) {
  const { t } = useT("terminal");
  const navigation = useNavigation();
  const view = useRef<TerminalViewHandle>(null);
  const [live, setLive] = useState(false);

  const restart = () => {
    // The address drops the ended terminal's id, which a switch back to this
    // tab would otherwise rejoin.
    if (terminalId) navigation.replace(address);
    view.current?.restart();
  };

  return (
    <TerminalFrame
      title={title}
      back={back}
      live={live}
      controls={
        <>
          <Button type="button" size="sm" disabled={live} onClick={restart}>
            {t(($) => $.restart)}
          </Button>
          {live && (
            <Button type="button" size="sm" variant="ghost" onClick={() => view.current?.end()}>
              {t(($) => $.end_session)}
            </Button>
          )}
        </>
      }
    >
      <div className="min-h-0 flex-1 px-3 pt-1 pb-2">
        <TerminalSessionView
          ref={view}
          target={target}
          sessionKey={sessionKey}
          terminalId={terminalId}
          address={address}
          onLiveChange={setLive}
        />
      </div>
    </TerminalFrame>
  );
}

/** The page's header, while what the terminal opens is still being read. */
function TerminalLoading({ title, back }: { title: ReactNode; back?: BackToList }) {
  return (
    <TerminalFrame title={title} back={back}>
      <div className="flex min-h-0 flex-1 items-center justify-center">
        <Spinner aria-hidden />
      </div>
    </TerminalFrame>
  );
}

/** The page's header, and why there is no terminal to show under it. */
function TerminalMessage({ title, back, message }: { title: ReactNode; back?: BackToList; message: string }) {
  return (
    <TerminalFrame title={title} back={back}>
      <div className="flex min-h-0 flex-1 items-center justify-center p-6">
        <p role="alert" className="max-w-md rounded-lg border border-surface-border bg-background px-4 py-3 text-center text-body text-foreground shadow-md">
          {message}
        </p>
      </div>
    </TerminalFrame>
  );
}

function placeKey(place: TerminalPlace): string {
  return place.kind === "ssh" ? `ssh:${place.host}` : "local";
}

/**
 * The agent's command line in one tree of a task. The task, its trees and
 * where each of them is are read here; what the app is asked to start follows
 * from them.
 */
function AgentTerminal({
  issueId,
  treeId,
  terminalId,
  address,
}: {
  issueId: string;
  treeId: string;
  terminalId?: string;
  address: string;
}) {
  const { t } = useT("terminal");
  const wsId = useWorkspaceId();
  const issue = useQuery(issueDetailOptions(wsId, issueId));
  const scope = useIssueProjectScope(wsId, issueId);
  const projects = useQuery(projectListOptions(wsId));
  const { folders } = useProjectFolders(wsId);
  // The pair whose tree the agent starts in.
  const pair = scope.data.items.find((item) => item.tree?.id === treeId);
  const tree = pair?.tree;
  // Read for that project whatever its state: its folder says where the tree is.
  const resources = useQuery({
    ...projectResourcesOptions(wsId, pair?.project_id ?? ""),
    enabled: pair !== undefined,
  });

  const title = <span className="shrink-0 text-muted-foreground">{t(($) => $.agent_terminal)}</span>;
  if (issue.isPending || scope.isPending || projects.isPending || (pair && resources.isPending)) {
    return <TerminalLoading title={title} />;
  }

  const folder = resources.data?.flatMap((resource) => projectFolderPlace(resource) ?? [])[0];
  const place: TerminalPlace = folder?.place ?? { kind: "local" };
  // A tree is a folder only once it was built.
  const cwd = tree?.state === "ready" && tree.path && folder ? tree.path : "";
  const projectTitle = (projectId: string) =>
    projects.data?.find((project) => project.id === projectId)?.title ?? projectId;
  // The other trees are where the agent can also work. Ones on another
  // computer than the first are not its to reach.
  const otherTrees: AgentTerminalTree[] = scope.data.items.flatMap((item) => {
    if (item.tree?.id === treeId || item.tree?.state !== "ready" || !item.tree.path) return [];
    const other = folders.find(({ project }) => project.id === item.project_id)?.place;
    if (other && placeKey(other) !== placeKey(place)) return [];
    return [
      {
        path: item.tree.path,
        repoName: projectTitle(item.project_id),
        branch: item.tree.task_branch,
        baseBranch: item.base_branch,
      },
    ];
  });
  const task = issue.data;

  return (
    <TerminalFor
      target={{
        kind: "agent",
        place,
        cwd,
        trees: [
          {
            path: tree?.path ?? "",
            repoName: pair ? projectTitle(pair.project_id) : "",
            branch: tree?.task_branch ?? "",
            baseBranch: pair?.base_branch ?? "",
          },
          ...otherTrees,
        ],
        task: {
          id: task?.id ?? issueId,
          workspaceId: wsId,
          identifier: task?.identifier ?? "",
          title: task?.title ?? "",
          description: task?.description ?? "",
        },
      }}
      sessionKey={`agent:${issueId}:${treeId}`}
      terminalId={terminalId}
      address={address}
      title={title}
    />
  );
}

/** A shell in the folder a project registered, which the header names with its project. */
function ProjectTerminal({
  project,
  terminalId,
  back,
}: {
  project: string;
  terminalId?: string;
  back: BackToList;
}) {
  const { t } = useT("terminal");
  const workspacePaths = useWorkspacePaths();
  const wsId = useWorkspaceId();
  const { folders, loading } = useProjectFolders(wsId);
  const folder = folders.find(({ resource }) => resource.id === project);
  const title = (shown?: ProjectFolder) => (
    <>
      <span className="min-w-0 truncate text-body font-medium">{shown?.project.title}</span>
      {shown && (
        <span
          className="min-w-0 truncate font-mono text-caption text-muted-foreground"
          title={projectFolderLabel(shown)}
        >
          {projectFolderLabel(shown)}
        </span>
      )}
    </>
  );
  // Until the folders are read there is nothing to name. A folder that was
  // removed from its project is not opened again.
  if (!folder) {
    return loading ? (
      <TerminalLoading title={title()} />
    ) : (
      <TerminalMessage title={title()} back={back} message={t(($) => $.errors.project_not_found)} />
    );
  }
  return (
    <TerminalFor
      target={{ kind: "shell", place: folder.place, cwd: folder.path }}
      sessionKey={`project:${project}`}
      terminalId={terminalId}
      address={workspacePaths.terminalProject(project)}
      back={back}
      title={title(folder)}
    />
  );
}

/** The projects a terminal can open in. */
function ProjectList() {
  const { t } = useT("terminal");
  const workspacePaths = useWorkspacePaths();
  const wsId = useWorkspaceId();
  const { folders, loading } = useProjectFolders(wsId);

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader className="gap-3">
        <h1 className="min-w-0 flex-1 truncate text-title font-semibold">{t(($) => $.title)}</h1>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          onClick={() => useModalStore.getState().open("create-project")}
        >
          {t(($) => $.add_project)}
        </Button>
      </PageHeader>
      <div className={cn("flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto py-2", PAGE_GUTTER)}>
        <ProjectRow
          href={workspacePaths.terminalProject(TERMINAL_DEFAULT_PROJECT)}
          title={t(($) => $.default_project)}
        />
        {folders.map(({ project, ...folder }) => (
          <ProjectRow
            key={folder.resource.id}
            href={workspacePaths.terminalProject(folder.resource.id)}
            title={project.title}
            path={projectFolderLabel(folder)}
          />
        ))}
        {!loading && folders.length === 0 && (
          <p className="px-2 py-1 text-caption text-muted-foreground">{t(($) => $.no_project_folders)}</p>
        )}
      </div>
    </div>
  );
}

function ProjectRow({ href, title, path }: { href: string; title: string; path?: string }) {
  return (
    <AppLink
      href={href}
      className="flex w-full shrink-0 items-center gap-3 rounded-md px-2 py-1.5 text-left transition-colors hover:bg-muted"
    >
      <FolderOpen className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-body text-foreground">{title}</span>
        {path && (
          <span className="block truncate font-mono text-caption text-muted-foreground" title={path}>
            {path}
          </span>
        )}
      </span>
    </AppLink>
  );
}

interface TerminalViewHandle {
  /** Start the target's terminal again. Does nothing while one is still opening or running. */
  restart(): void;
  /** Ask the app to end the process. */
  end(): void;
}

function isOpeningOrRunning(status: TerminalStatus): boolean {
  return status === "connecting" || status === "ready";
}

const TerminalSessionView = forwardRef<TerminalViewHandle, {
  target: TerminalHostTarget;
  sessionKey: string;
  /** The terminal to rejoin, when the address names one that is already open. */
  terminalId?: string;
  /** The page's address, without the id of a terminal. */
  address: string;
  onLiveChange: (live: boolean) => void;
}>(function TerminalSessionView({ target, sessionKey, terminalId, address, onLiveChange }, ref) {
  const navigation = useNavigation();
  const terminal = useRef<TerminalHandle>(null);
  const session = useTerminalSession({ target, key: sessionKey, terminalId });
  const state = useTerminalSessionState(session);
  const live = isOpeningOrRunning(state.status);

  useImperativeHandle(
    ref,
    () => ({
      restart: () => {
        if (isOpeningOrRunning(session.getState().status)) return;
        // What was on screen belongs to the terminal that ended.
        terminal.current?.reset();
        session.restart();
      },
      end: () => session.close(),
    }),
    [session],
  );

  useEffect(() => {
    onLiveChange(live);
    return () => onLiveChange(false);
  }, [live, onLiveChange]);

  return (
    <Terminal
      ref={terminal}
      session={session}
      onReady={(id) => {
        if (id === terminalId) return;
        navigation.replace(withTerminalId(address, id));
      }}
    />
  );
});
