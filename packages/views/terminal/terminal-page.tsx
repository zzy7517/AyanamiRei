"use client";

/**
 * A terminal in a directory of the machine the daemon runs on, or the agent's
 * command line in a tree of a task.
 *
 * The URL says what the page shows: `?cwd=<directory>` opens a shell there,
 * `?issue=<task id>&tree=<tree id>` opens an agent terminal in that tree, and
 * `&t=<terminal id>` rejoins a terminal that is already running, which is how
 * a desktop tab finds its terminal again after a switch. The id is written
 * once the server names the terminal and stays after the process has ended,
 * so returning to the page does not quietly open another shell or start the
 * agent again.
 */

import {
  forwardRef,
  useEffect,
  useId,
  useImperativeHandle,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueShown } from "@multica/core/issues/agent-reply";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  useTerminalSession,
  useTerminalSessionState,
  type TerminalAgentTarget,
  type TerminalStatus,
} from "@multica/core/terminal";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { useMarkAgentReplySeenWhileViewing } from "../issues/hooks/use-mark-agent-reply-seen-while-viewing";
import { PageHeader } from "../layout/page-header";
import { useNavigation } from "../navigation";
import { Terminal, type TerminalHandle } from "./terminal";

/** What the page starts when the address names no running terminal. */
export type TerminalTarget =
  | { cwd: string }
  | { agent: TerminalAgentTarget };

export function TerminalPage() {
  const { t } = useT("terminal");
  const navigation = useNavigation();
  const workspacePaths = useWorkspacePaths();
  const directory = navigation.searchParams.get("cwd")?.trim() ?? "";
  const issueId = navigation.searchParams.get("issue")?.trim() ?? "";
  const treeId = navigation.searchParams.get("tree")?.trim() ?? "";
  // A tree belongs to one task: an address with only one of the two opens nothing.
  const agent = issueId && treeId ? { issue_id: issueId, tree_id: treeId } : null;
  const terminalId = navigation.searchParams.get("t") || undefined;
  const view = useRef<TerminalViewHandle>(null);
  const [live, setLive] = useState(false);
  // The viewer is watching this agent, so its replies are seen as they land:
  // none is left marked, and none is announced over the terminal showing it.
  const wsId = useWorkspaceId();
  const shownIssueId = agent ? issueId : null;
  useIssueShown(shownIssueId);
  useMarkAgentReplySeenWhileViewing(wsId, shownIssueId ?? "");

  const open = (next: string) => {
    // Another directory is another session, which the address brings with it.
    // The same one keeps its session, so that is started again from here.
    const restart = next === directory;
    navigation.replace(workspacePaths.terminalIn(next));
    if (restart) view.current?.restart();
  };

  const target: TerminalTarget | null = agent
    ? { agent }
    : directory
      ? { cwd: directory }
      : null;

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader className="gap-3">
        {agent ? (
          // As the directory is to a shell: what this page opens, and the way
          // to open it again once the agent has ended.
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <span className="shrink-0 text-muted-foreground">{t(($) => $.agent_terminal)}</span>
            <Button
              type="button"
              size="sm"
              disabled={live}
              onClick={() => {
                // The address drops the ended terminal's id, which a switch
                // back to this tab would otherwise rejoin.
                navigation.replace(workspacePaths.terminalAgent(issueId, treeId));
                view.current?.restart();
              }}
            >
              {t(($) => $.restart)}
            </Button>
          </div>
        ) : (
          <DirectoryForm
            key={directory}
            initial={directory}
            disabled={live}
            onOpen={open}
            className="flex-1"
          />
        )}
        {live && (
          <Button type="button" size="sm" variant="ghost" onClick={() => view.current?.end()}>
            {t(($) => $.end_session)}
          </Button>
        )}
      </PageHeader>
      {target && (
        <div className="min-h-0 flex-1 px-3 pt-1 pb-2">
          <TerminalSessionView
            ref={view}
            target={target}
            terminalId={terminalId}
            onLiveChange={setLive}
          />
        </div>
      )}
    </div>
  );
}

function DirectoryForm({
  initial,
  disabled,
  onOpen,
  className,
}: {
  initial: string;
  /** While a terminal is opening or running: another one would leave it behind, still running. */
  disabled: boolean;
  onOpen: (directory: string) => void;
  className?: string;
}) {
  const { t } = useT("terminal");
  const inputId = useId();
  const hintId = useId();
  const [value, setValue] = useState(initial);
  const [rejected, setRejected] = useState(false);

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const next = value.trim();
    // The daemon judges the path against its own rules; this only stops what cannot be one.
    if (!next.startsWith("/")) {
      setRejected(true);
      return;
    }
    onOpen(next);
  };

  return (
    <form onSubmit={submit} className={cn("flex min-w-0 items-center gap-2", className)}>
      <Label htmlFor={inputId} className="shrink-0 text-muted-foreground">
        {t(($) => $.directory_label)}
      </Label>
      <Input
        id={inputId}
        value={value}
        onChange={(event) => {
          setValue(event.target.value);
          setRejected(false);
        }}
        placeholder={t(($) => $.directory_placeholder)}
        spellCheck={false}
        autoComplete="off"
        autoCapitalize="off"
        autoCorrect="off"
        aria-invalid={rejected || undefined}
        aria-describedby={rejected ? hintId : undefined}
        disabled={disabled}
        className="w-80 max-w-full font-mono"
      />
      <Button type="submit" size="sm" disabled={disabled}>
        {t(($) => $.open)}
      </Button>
      {rejected && (
        <span id={hintId} className="text-caption text-destructive">
          {t(($) => $.directory_required)}
        </span>
      )}
    </form>
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
      : { agent: target.agent, terminalId },
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
        else session.openAgent(target.agent);
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
            : workspacePaths.terminalAgent(target.agent.issue_id, target.agent.tree_id, id),
        );
      }}
      onRequestClose={() => session.close()}
    />
  );
});
