"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { isAgentRuntimeBound, useWorkspacePresenceMap, type AgentAvailability } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { canAssignAgentToIssue, useCurrentMember } from "@multica/core/permissions";
import { runtimeDisplayLabel, runtimeListOptions } from "@multica/core/runtimes";
import { agentListOptions } from "@multica/core/workspace/queries";
import type { Agent } from "@multica/core/types";

export interface AgentEntry {
  agent: Agent;
  /** Whether the viewer may hand this agent work right now. */
  assignable: boolean;
  runtimeBound: boolean;
  availability: AgentAvailability | null;
  runtimeLabel: string | null;
}

const EMPTY_AGENTS: Agent[] = [];

/**
 * Agents as the palette shows them: archived ones out, each agent annotated
 * with whether the viewer can assign it and where it runs. One pass shared by
 * search, the assignee submenu and delegation.
 */
export function useActorDirectory(enabled: boolean) {
  const wsId = useWorkspaceId();
  const { userId, role } = useCurrentMember(wsId);
  const { data: agentsData = EMPTY_AGENTS } = useQuery({ ...agentListOptions(wsId), enabled });
  const { data: runtimes } = useQuery({ ...runtimeListOptions(wsId), enabled });
  const { byAgent } = useWorkspacePresenceMap(enabled ? wsId : undefined);

  const agents = useMemo<AgentEntry[]>(() => {
    const runtimeById = new Map((runtimes ?? []).map((runtime) => [runtime.id, runtime]));
    return agentsData
      .filter((agent) => !agent.archived_at)
      .map((agent) => {
        const decision = canAssignAgentToIssue(agent, { userId, role });
        const runtimeBound = isAgentRuntimeBound(agent);
        const runtime = agent.runtime_id ? runtimeById.get(agent.runtime_id) : undefined;
        return {
          agent,
          assignable: decision.allowed && runtimeBound,
          runtimeBound,
          availability: byAgent.get(agent.id)?.availability ?? null,
          runtimeLabel: runtime ? runtimeDisplayLabel(runtime) : null,
        };
      });
  }, [agentsData, byAgent, role, runtimes, userId]);

  return useMemo(() => ({ agents }), [agents]);
}
