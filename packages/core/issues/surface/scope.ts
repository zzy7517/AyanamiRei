import type { IssueAssigneeType, IssueCreatorType } from "../../types";

/** Whether a workspace or project surface lists every issue or only the ones
 *  assigned to agents. */
export type WorkspaceIssueActorKind = "all" | "agents";

export type IssueScope =
  | { type: "workspace"; actorKind?: WorkspaceIssueActorKind }
  | { type: "project"; projectId: string; actorKind?: WorkspaceIssueActorKind }
  | { type: "actor"; relation: "assigned"; actorType: IssueAssigneeType; actorId: string }
  | { type: "actor"; relation: "created"; actorType: IssueCreatorType; actorId: string }
  | { type: "team"; teamId: string };

/**
 * THE single translation between the UI's coarse assignee-type tab and the
 * API's `assignee_types` values. Every channel (list GET params, table
 * query spec, gantt) must compile the tab through this function — do not
 * inline the literal arrays anywhere else.
 */
export function assigneeTypesForActorKind(
  actorKind: WorkspaceIssueActorKind | undefined,
): IssueAssigneeType[] | undefined {
  return actorKind === "agents" ? ["agent"] : undefined;
}

/** Saved-view scope_variant → tab. Unknown/absent variants mean "all". */
export function actorKindForViewVariant(
  variant: string | null | undefined,
): WorkspaceIssueActorKind {
  return variant === "agents" ? "agents" : "all";
}

export class UnsupportedIssueScopeError extends Error {
  constructor(scope: IssueScope, operation: string) {
    super(`Issue scope "${issueScopeKey(scope)}" is not supported for ${operation}.`);
    this.name = "UnsupportedIssueScopeError";
  }
}

export function issueScopeKey(scope: IssueScope): string {
  switch (scope.type) {
    case "workspace":
      return `workspace:${scope.actorKind ?? "all"}`;
    case "project":
      // The unrestricted tab keeps the historical key so existing persisted
      // display state survives; the Agents tab gets its own key (and thus
      // its own per-tab display state), matching the workspace tabs.
      return scope.actorKind === "agents"
        ? `project:${scope.projectId}:${scope.actorKind}`
        : `project:${scope.projectId}`;
    case "actor":
      return `actor:${scope.actorType}:${scope.actorId}:${scope.relation}`;
    case "team":
      return `team:${scope.teamId}`;
  }
}
