"use client";

/**
 * A terminal in a project's folder, or the agent's command line in a tree of a
 * task.
 *
 * The URL says what the page shows. `?project=<project>` opens a shell in a
 * project's folder: the default project, which is there without adding one, or
 * a folder a project registered, named by the id of that folder. `?cwd=
 * <directory>` opens one in a directory of a task, which is where the task
 * page's Shell link leads. `?issue=<task id>&tree=<tree id>` opens an agent
 * terminal in that tree. `&t=<terminal id>` rejoins a terminal that is already
 * running, which is how a desktop tab finds its terminal again after a switch.
 * The id is written once the server names the terminal and stays after the
 * process has ended, so returning to the page does not quietly open another
 * shell or start the agent again.
 *
 * With none of these the page lists the projects a terminal can open in.
 */

import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueShown } from "@multica/core/issues/agent-reply";
import { useModalStore } from "@multica/core/modals";
import { useWorkspacePaths } from "@multica/core/paths";
import { useProjectLocalDirectories } from "@multica/core/projects";
import {
  TERMINAL_DEFAULT_PROJECT,
  useTerminalSession,
  useTerminalSessionState,
  type TerminalAgentTarget,
  type TerminalStatus,
} from "@multica/core/terminal";
import { Button } from "@multica/ui/components/ui/button";
import { ArrowLeft, FolderOpen } from "@multica/ui/icons";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { useMarkAgentReplySeenWhileViewing } from "../issues/hooks/use-mark-agent-reply-seen-while-viewing";
import { PAGE_GUTTER, PageHeader } from "../layout/page-header";
import { AppLink, useNavigation } from "../navigation";
import { Terminal, type TerminalHandle } from "./terminal";

/** What the page starts when the address names no running terminal. */
export type TerminalTarget =
  | { project: string }
  | { cwd: string }
  | { agent: TerminalAgentTarget };

export function TerminalPage() {
  const { t } = useT("terminal");
  const navigation = useNavigation();
  const workspacePaths = useWorkspacePaths();
  const project = navigation.searchParams.get("project")?.trim() ?? "";
  const directory = navigation.searchParams.get("cwd")?.trim() ?? "";
  const issueId = navigation.searchParams.get("issue")?.trim() ?? "";
  const treeId = navigation.searchParams.get("tree")?.trim() ?? "";
  // A tree belongs to one task: an address with only one of the two opens nothing.
  const agent = issueId && treeId ? { issue_id: issueId, tree_id: treeId } : null;
  // An id on its own says which terminal to rejoin, but not what it is for.
  const terminalId = agent || project || directory ? navigation.searchParams.get("t") || undefined : undefined;
  // The viewer is watching this agent, so its replies are seen as they land:
  // none is left marked, and none is announced over the terminal showing it.
  const wsId = useWorkspaceId();
  const shownIssueId = agent ? issueId : null;
  useIssueShown(shownIssueId);
  useMarkAgentReplySeenWhileViewing(wsId, shownIssueId ?? "");

  const back = { label: t(($) => $.all_projects), to: workspacePaths.terminal() };

  if (agent) {
    return (
      <TerminalFor
        target={{ agent }}
        terminalId={terminalId}
        address={workspacePaths.terminalAgent(issueId, treeId)}
        // As the directory is to a shell: what this page opens, and the way
        // to open it again once the agent has ended.
        title={<span className="shrink-0 text-muted-foreground">{t(($) => $.agent_terminal)}</span>}
      />
    );
  }
  if (project === TERMINAL_DEFAULT_PROJECT) {
    return (
      <TerminalFor
        target={{ project }}
        terminalId={terminalId}
        address={workspacePaths.terminalProject(project)}
        back={back}
        title={<span className="min-w-0 truncate text-body font-medium">{t(($) => $.default_project)}</span>}
      />
    );
  }
  if (project) return <ProjectTerminal project={project} terminalId={terminalId} back={back} />;
  if (directory) {
    return (
      <TerminalFor
        target={{ cwd: directory }}
        terminalId={terminalId}
        address={workspacePaths.terminalIn(directory)}
        title={
          <span className="min-w-0 truncate font-mono text-caption text-muted-foreground">{directory}</span>
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
function TerminalFor({
  target,
  terminalId,
  address,
  back,
  title,
}: {
  target: TerminalTarget;
  terminalId?: string;
  /** The page's address for the target, without the id of a terminal. */
  address: string;
  /**
   * The way back to the list. It is disabled while the terminal is opening or
   * running: leaving would leave the shell behind, still running.
   */
  back?: BackToList;
  /** What the header says about the target, before its controls. */
  title: ReactNode;
}) {
  const { t } = useT("terminal");
  const navigation = useNavigation();
  const view = useRef<TerminalViewHandle>(null);
  const [live, setLive] = useState(false);

  const restart = () => {
    // The address drops the ended terminal's id, which a switch back to this
    // tab would otherwise rejoin.
    navigation.replace(address);
    view.current?.restart();
  };

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
        <Button type="button" size="sm" disabled={live} onClick={restart}>
          {t(($) => $.restart)}
        </Button>
        {live && (
          <Button type="button" size="sm" variant="ghost" onClick={() => view.current?.end()}>
            {t(($) => $.end_session)}
          </Button>
        )}
      </PageHeader>
      <div className="min-h-0 flex-1 px-3 pt-1 pb-2">
        <TerminalSessionView
          ref={view}
          target={target}
          terminalId={terminalId}
          onLiveChange={setLive}
        />
      </div>
    </div>
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
  const workspacePaths = useWorkspacePaths();
  const wsId = useWorkspaceId();
  const { folders } = useProjectLocalDirectories(wsId);
  const folder = folders.find(({ resource }) => resource.id === project);
  // Until the projects are read, and for a folder that was removed, there is nothing to name.
  const path = folder?.resource.resource_ref.local_path;
  return (
    <TerminalFor
      target={{ project }}
      terminalId={terminalId}
      address={workspacePaths.terminalProject(project)}
      back={back}
      title={
        <>
          <span className="min-w-0 truncate text-body font-medium">{folder?.project.title}</span>
          {path && (
            <span className="min-w-0 truncate font-mono text-caption text-muted-foreground" title={path}>
              {path}
            </span>
          )}
        </>
      }
    />
  );
}

/** The projects a terminal can open in. */
function ProjectList() {
  const { t } = useT("terminal");
  const workspacePaths = useWorkspacePaths();
  const wsId = useWorkspaceId();
  const { folders, loading } = useProjectLocalDirectories(wsId);

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
        {folders.map(({ project, resource }) => (
          <ProjectRow
            key={resource.id}
            href={workspacePaths.terminalProject(resource.id)}
            title={project.title}
            path={resource.resource_ref.local_path}
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
  /** Ask the server to end the process. */
  end(): void;
}

function isOpeningOrRunning(status: TerminalStatus): boolean {
  return status === "connecting" || status === "ready";
}

const TerminalSessionView = forwardRef<TerminalViewHandle, {
  target: TerminalTarget;
  /** The terminal to rejoin, when the address names one that is already running. */
  terminalId?: string;
  onLiveChange: (live: boolean) => void;
}>(function TerminalSessionView({ target, terminalId, onLiveChange }, ref) {
  const { t } = useT("terminal");
  const navigation = useNavigation();
  const workspacePaths = useWorkspacePaths();
  const terminal = useRef<TerminalHandle>(null);
  const session = useTerminalSession(
    "cwd" in target
      ? { cwd: target.cwd, terminalId }
      : "agent" in target
        ? { agent: target.agent, terminalId }
        : { project: target.project, terminalId },
  );
  const state = useTerminalSessionState(session);
  const live = isOpeningOrRunning(state.status);
  const reattached = t(($) => $.status.reattached);

  // A rejoined terminal is not replayed. Say so on the screen that comes back empty.
  const rejoined = useRef(Boolean(terminalId));
  const announced = useRef(false);

  useImperativeHandle(
    ref,
    () => ({
      restart: () => {
        if (isOpeningOrRunning(session.getState().status)) return;
        // A terminal started from here was not rejoined, so the next ready has
        // nothing to announce about an empty screen.
        announced.current = true;
        if ("cwd" in target) session.open(target.cwd);
        else if ("agent" in target) session.openAgent(target.agent);
        else session.openProject(target.project);
      },
      end: () => session.close(),
    }),
    [session, target, announced],
  );

  useEffect(() => {
    onLiveChange(live);
    return () => onLiveChange(false);
  }, [live, onLiveChange]);

  useEffect(() => {
    if (!rejoined.current || announced.current || state.status !== "ready") return;
    announced.current = true;
    terminal.current?.write(`\x1b[90m${reattached}\x1b[0m\r\n`);
  }, [state.status, reattached]);

  return (
    <Terminal
      ref={terminal}
      session={session}
      onReady={(id) => {
        if (id === terminalId) return;
        navigation.replace(
          "cwd" in target
            ? workspacePaths.terminalIn(target.cwd, id)
            : "agent" in target
              ? workspacePaths.terminalAgent(target.agent.issue_id, target.agent.tree_id, id)
              : workspacePaths.terminalProject(target.project, id),
        );
      }}
      onRequestClose={() => session.close()}
    />
  );
});
