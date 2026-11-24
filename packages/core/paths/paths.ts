/**
 * Centralized URL path builder. All navigation in shared packages (packages/views)
 * MUST go through this module — no hardcoded string paths.
 *
 * Two kinds of paths:
 *  - workspace-scoped: paths.workspace(slug).xxx() — carry workspace in URL
 *  - global: paths.newWorkspace(), paths.onboarding() — pre-workspace routes
 *
 * Why pure functions + builder pattern:
 *  - Changing a route shape (e.g. adding workspace slug prefix) becomes a single-file edit
 *  - IDs are always URL-encoded here so callers can't forget
 *  - Zero runtime deps means this module is safe in Node (tests) and browsers
 */

const encode = (id: string) => encodeURIComponent(id);

declare const issueRouteRefBrand: unique symbol;

/**
 * The `{segment}` of an `/{ws}/issues/{segment}` link: the issue's identifier
 * (`MUL-123`), or its UUID only when the identifier is unknown. The issue page
 * rewrites a UUID address to the identifier once the issue loads, and on web
 * that rewrite mounts the page and refetches the issue a second time.
 *
 * Branded so a bare `issue.id` cannot reach `issueDetail`: build one with
 * `issueRouteRef`, or with `useIssueRouteRef` (`@multica/core/issues/canonical-id`)
 * when only the UUID is at hand.
 */
export type IssueRouteRef = string & { readonly [issueRouteRefBrand]: true };

/**
 * The route segment for an issue. `identifier` is a required key so a caller
 * that has none says so with `identifier: null`.
 */
export function issueRouteRef(issue: { id: string; identifier: string | null | undefined }): IssueRouteRef {
  return (issue.identifier || issue.id) as IssueRouteRef;
}

/**
 * `?focus=` token that scrolls the agent's General settings to its
 * conversation-starters editor and flashes it. Lives here because it is URL
 * vocabulary: `agentConversationStarters()` writes it and the page reads it,
 * and a shared constant is what stops the two from drifting apart.
 */
export const AGENT_FOCUS_CONVERSATION_STARTERS = "conversation_starters";

function workspaceScoped(slug: string) {
  const ws = `/${encode(slug)}`;
  return {
    root: () => `${ws}/issues`,
    usage: () => `${ws}/usage`,
    issues: () => `${ws}/issues`,
    issueDetail: (ref: IssueRouteRef) => `${ws}/issues/${encode(ref)}`,
    // Opens the newest upload of the deliverable named `filename` on this
    // issue. Addresses the file by name, not by attachment id, so the link
    // keeps pointing at the latest version as new ones are uploaded.
    issueDeliverable: (ref: IssueRouteRef, filename: string) =>
      `${ws}/issues/${encode(ref)}/deliverables/${encode(filename)}`,
    projects: () => `${ws}/projects`,
    projectDetail: (id: string) => `${ws}/projects/${encode(id)}`,
    autopilots: () => `${ws}/autopilots`,
    autopilotDetail: (id: string) => `${ws}/autopilots/${encode(id)}`,
    agents: () => `${ws}/agents`,
    // Step 1 of agent creation: choose where the agent runs.
    newAgent: () => `${ws}/agents/new`,
    // Step 2: configure the agent on the chosen runtime. A real route so a
    // half-filled form survives a refresh and can be linked to directly.
    newAgentManual: () => `${ws}/agents/new/manual`,
    // Step 3: the agent exists; offer its first uses.
    newAgentDone: (agentId: string) =>
      `${ws}/agents/new/done/${encode(agentId)}`,
    // One creation conversation. It is a durable object, not a step of the
    // routes above: it survives leaving the studio and is resumed later, so it
    // owns an address instead of being a query param on the screen that started it.
    newAgentAiSession: (sessionId: string) =>
      `${ws}/agents/new/ai/${encode(sessionId)}`,
    agentDetail: (id: string) => `${ws}/agents/${encode(id)}`,
    // The agent's General settings, scrolled to its conversation starters.
    agentConversationStarters: (id: string) =>
      `${ws}/agents/${encode(id)}?view=general&focus=${AGENT_FOCUS_CONVERSATION_STARTERS}`,
    inbox: () => `${ws}/inbox`,
    chat: () => `${ws}/chat`,
    chatWithAgent: (agentId: string) =>
      `${ws}/chat?agent=${encode(agentId)}`,
    chatSession: (sessionId: string) =>
      `${ws}/chat?session=${encode(sessionId)}`,
    views: () => `${ws}/views`,
    runtimes: () => `${ws}/runtimes`,
    runtimeDetail: (id: string) => `${ws}/runtimes/${encode(id)}`,
    runtimeSettings: (machineId: string, runtimeId: string) =>
      `${ws}/runtimes/${encode(machineId)}/runtime/${encode(runtimeId)}`,
    docs: () => `${ws}/docs`,
    docDetail: (id: string) => `${ws}/docs/${encode(id)}`,
    docHistory: (id: string) => `${ws}/docs/${encode(id)}/history`,
    docFolder: (id: string) => `${ws}/docs/folders/${encode(id)}`,
    skills: () => `${ws}/skills`,
    skillDetail: (id: string) => `${ws}/skills/${encode(id)}`,
    settings: () => `${ws}/settings`,
    attachmentPreview: (id: string) => `${ws}/attachments/${encode(id)}/preview`,
    terminal: () => `${ws}/terminal`,
    // The terminal page for a shell in one of a task's directories.
    // `terminalId` names a terminal that is already running, which the page
    // rejoins instead of opening a shell.
    terminalIn: (cwd: string, terminalId?: string) =>
      `${ws}/terminal?cwd=${encode(cwd)}${terminalId ? `&t=${encode(terminalId)}` : ""}`,
    // The terminal page for a project. `project` is the default project
    // (`TERMINAL_DEFAULT_PROJECT`) or the id of the local folder a project
    // registered. `terminalId` names a terminal that is already running, as
    // for a task's directory.
    terminalProject: (project: string, terminalId?: string) =>
      `${ws}/terminal?project=${encode(project)}${terminalId ? `&t=${encode(terminalId)}` : ""}`,
    // The terminal page for an agent session in a tree of a task, both by
    // UUID. `terminalId` names a session that is already running, which the
    // page rejoins instead of starting the agent again.
    terminalAgent: (issueId: string, treeId: string, terminalId?: string) =>
      `${ws}/terminal?issue=${encode(issueId)}&tree=${encode(treeId)}${terminalId ? `&t=${encode(terminalId)}` : ""}`,
  };
}

export const paths = {
  workspace: workspaceScoped,

  // Global (pre-workspace) routes. There is no login route: the local
  // install has one user and the clients open straight into a workspace.
  newWorkspace: () => "/workspaces/new",
  onboarding: () => "/onboarding",
  root: () => "/",
};

export type WorkspacePaths = ReturnType<typeof workspaceScoped>;

// Prefixes — not slug names — because we match against full URL paths.
// A path is global if it equals or begins with any of these.
// Note: `/workspaces/` (trailing slash) is the prefix — `workspaces` is reserved,
// so any path starting with `/workspaces/...` is system-owned, not user-owned.
const GLOBAL_PREFIXES = ["/workspaces/", "/onboarding"];

export function isGlobalPath(path: string): boolean {
  return GLOBAL_PREFIXES.some((p) => path === p || path.startsWith(p));
}
