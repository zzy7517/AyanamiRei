import { useEffect } from "react";
import { toast } from "sonner";
import { useWorkspaceList } from "@multica/core/workspace";
import { useT } from "@multica/views/i18n";
import { parseIssueDeepLink } from "../../../shared/issue-deep-link";
import { routeIssueDeepLinkPath } from "./navigation";

export function IssueDeepLinkBridge() {
  const { t } = useT("issues");
  const { workspaces, ready } = useWorkspaceList();
  const config = window.desktopAPI.runtimeConfig;

  useEffect(() => {
    // Main keeps the link queued through cold start until this listener can
    // resolve the destination against the workspace list.
    if (!ready || !config.ok) return;
    return window.desktopAPI.onIssueOpen((url) => {
      const target = parseIssueDeepLink(url, config.config.appUrl);
      if (!target) return;
      if (target.kind === "other-instance") {
        toast.error(t(($) => $.desktop.link_other_instance));
        return;
      }
      if (!workspaces.some((workspace) => workspace.slug === target.workspaceSlug)) {
        toast.error(t(($) => $.desktop.link_workspace_unavailable));
        return;
      }
      routeIssueDeepLinkPath(target.path);
    });
  }, [ready, workspaces, config, t]);

  return null;
}
