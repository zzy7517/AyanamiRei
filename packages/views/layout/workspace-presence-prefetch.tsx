"use client";

import { useWorkspaceId } from "@multica/core";
import { useWorkspacePresencePrefetch } from "@multica/core/agents";
import { useAgentReplyNotificationText } from "../issues/hooks/use-agent-reply-notification-text";

// Mount once inside any subtree that's already gated on "workspace resolved"
// (DashboardLayout on web, WorkspaceRouteLayout on desktop). useWorkspaceId
// throws when called outside a resolved workspace — the gating in those
// layouts guarantees this component never sees that state.
export function WorkspacePresencePrefetch() {
  const wsId = useWorkspaceId();
  // The "new reply" banner is worded here, where every window that shows a
  // workspace mounts once; the banner itself is raised elsewhere.
  useAgentReplyNotificationText();
  useWorkspacePresencePrefetch(wsId);
  return null;
}
