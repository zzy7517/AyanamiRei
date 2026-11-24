import { useEffect, useRef } from "react";
import { toast } from "sonner";
import { workspaceSlugForPath } from "@multica/core/paths";
import { useWorkspaceList } from "@multica/core/workspace";
import { useT } from "@multica/views/i18n";
import { useTabStore } from "@/stores/tab-store";
import {
  nextWorkspaceForNewWindow,
  openWorkspaceInNewWindow,
  otherWindowWorkspaces,
} from "./workspace-windows";

/**
 * Main-window side of multi-window workspaces: tells main which workspace
 * this window shows, opens paths other windows hand over, and answers
 * Cmd/Ctrl+Shift+N. Mounted for every main-window state so a shortcut
 * pressed before the user record arrives is consumed instead of replayed.
 */
export function WorkspaceWindowBridge() {
  const { t } = useT("layout");
  const { workspaces } = useWorkspaceList();
  const activeWorkspaceSlug = useTabStore((s) => s.activeWorkspaceSlug);

  const latest = useRef({ workspaces, t });
  useEffect(() => {
    latest.current = { workspaces, t };
  }, [workspaces, t]);

  useEffect(() => {
    window.desktopAPI.reportWindowWorkspace?.(activeWorkspaceSlug);
  }, [activeWorkspaceSlug]);

  useEffect(
    () =>
      window.desktopAPI.onOpenWorkspacePath?.(({ path, navigate }) => {
        const slug = workspaceSlugForPath(path);
        if (!slug) return;
        useTabStore.getState().switchWorkspace(slug, path);
        if (navigate) useTabStore.getState().navigateActiveSession(path);
      }),
    [],
  );

  useEffect(
    () =>
      window.desktopAPI.onNewWindowShortcut?.(() => {
        const { workspaces: list, t: translate } = latest.current;
        if (list.length === 0) return;
        const slug = nextWorkspaceForNewWindow(
          list.map((workspace) => workspace.slug),
          useTabStore.getState().activeWorkspaceSlug,
          otherWindowWorkspaces(),
        );
        if (slug) {
          openWorkspaceInNewWindow(slug);
        } else {
          toast(translate(($) => $.rail.all_workspaces_open));
        }
      }),
    [],
  );

  return null;
}
