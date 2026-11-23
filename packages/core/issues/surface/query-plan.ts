import type { CreateIssueRequest } from "../../types";
import type { IssueListFilter } from "../queries";
import {
  assigneeTypesForActorKind,
  issueScopeKey,
  UnsupportedIssueScopeError,
  type IssueScope,
} from "./scope";

/**
 * The scope's non-Table residue. Row membership for every list-shaped mode
 * (table, list, board, swimlane) is compiled into an IssueTableQuerySpec by
 * the surface controller and answered by the server-owned Table channel —
 * this plan only carries what that channel does not cover:
 *
 * - `scopeKey`: the surface's cache/persistence identity.
 * - `queryFilter`: the scope as legacy list-API params, consumed solely by
 *   the Gantt projection (whose scheduled-only window is not expressible in
 *   the Table spec).
 * - `createDefaults`: what a new issue created on this surface inherits.
 */
export interface IssueSurfaceQueryPlan {
  scopeKey: string;
  queryFilter: IssueListFilter;
  createDefaults: Partial<CreateIssueRequest>;
}

export function buildIssueSurfaceQueryPlan(
  scope: IssueScope,
): IssueSurfaceQueryPlan {
  const scopeKey = issueScopeKey(scope);

  switch (scope.type) {
    case "workspace": {
      const assigneeTypes = assigneeTypesForActorKind(scope.actorKind);
      return {
        scopeKey,
        queryFilter: assigneeTypes ? { assignee_types: assigneeTypes } : {},
        createDefaults: {},
      };
    }
    case "project": {
      const assigneeTypes = assigneeTypesForActorKind(scope.actorKind);
      return {
        scopeKey,
        queryFilter: assigneeTypes
          ? { project_id: scope.projectId, assignee_types: assigneeTypes }
          : { project_id: scope.projectId },
        createDefaults: { project_id: scope.projectId },
      };
    }
    case "actor":
      return scope.relation === "assigned"
        ? {
            scopeKey,
            queryFilter: { assignee_id: scope.actorId },
            createDefaults: { assignee_type: scope.actorType, assignee_id: scope.actorId },
          }
        : { scopeKey, queryFilter: { creator_id: scope.actorId }, createDefaults: {} };
    case "team":
      throw new UnsupportedIssueScopeError(scope, "issue surface query plan");
  }
}
