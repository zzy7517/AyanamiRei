"use client";

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { buildCreateAgentRequest, type AgentDraft } from "@multica/core/agents";
import { api, ApiError } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { refreshFeaturesOnLockedError } from "@multica/core/billing";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Agent } from "@multica/core/types";
import {
  cacheAgentResponse,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { usePaidFeatureErrorMessage } from "../../billing";

interface AgentCreateErrors {
  nameError: string | null;
  formError: string | null;
}

export function classifyAgentCreateError(
  error: unknown,
  fallbackMessage: string,
  conflictMessage: string,
): AgentCreateErrors {
  const message =
    error instanceof Error && error.message ? error.message : fallbackMessage;
  return error instanceof ApiError && error.status === 409
    ? { nameError: conflictMessage, formError: null }
    : { nameError: null, formError: message };
}

/**
 * Commits the draft and leaves the creation flow.
 *
 * Deliberately not optimistic: the flow navigates away on success, so the agent
 * has to exist before the destination renders.
 */
export function useCreateAgentSubmit(options: {
  draft: AgentDraft;
  runtimeId: string | null;
  /** Provider of `runtimeId`, so settings it cannot run are not submitted. */
  runtimeProvider?: string;
  /** Creation-source attribution for the `agent_created` analytics event. */
  template?: string;
  duplicateSource?: Agent | null;
  /** Runs after the agent is committed, before navigation. */
  onCreated?: (agent: Agent) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const qc = useQueryClient();
  const paidFeatureError = usePaidFeatureErrorMessage();

  const [creating, setCreating] = useState(false);
  const [nameError, setNameError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);

  const {
    draft,
    runtimeId,
    runtimeProvider,
    template,
    duplicateSource,
    onCreated,
  } = options;

  const create = async () => {
    if (!runtimeId || creating) return;
    setCreating(true);
    setNameError(null);
    setFormError(null);
    try {
      const agent = await api.createAgent(
        buildCreateAgentRequest({
          draft,
          runtimeId,
          runtimeProvider,
          template,
          duplicateSource,
        }),
      );
      if (!agent.id) throw new Error(t(($) => $.creation_studio.create_failed));

      await onCreated?.(agent);
      cacheAgentResponse(qc, wsId, agent);
      // The create response is authoritative enough to open immediately.
      // Reconcile other list projections in the background instead of making
      // navigation wait on a second network request.
      void qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      // Replace, not push: back from the done step must not reopen a form
      // whose agent already exists.
      navigation.replace(paths.newAgentDone(agent.id));
    } catch (error) {
      const nextErrors = classifyAgentCreateError(
        error,
        t(($) => $.creation_studio.create_failed),
        t(($) => $.creation_studio.name_conflict),
      );
      refreshFeaturesOnLockedError(qc, wsId, error);
      setNameError(nextErrors.nameError);
      setFormError(
        nextErrors.formError === null ? null : paidFeatureError(error, nextErrors.formError),
      );
      setCreating(false);
    }
  };

  return {
    create,
    creating,
    nameError,
    formError,
    clearNameError: () => setNameError(null),
  };
}
