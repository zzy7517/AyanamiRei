import { useParams } from "react-router-dom";
import { useFeatureEnabled } from "@multica/core/config";
import { TASK_SESSIONS_V1_FLAG } from "@multica/core/feature-flags";
import { IssueDetailRoute, IssueTreesSection } from "@multica/views/issues/components";

// Module level so the detail, which re-renders often, keeps the same slot.
const renderTrees = (issueId: string) => <IssueTreesSection issueId={issueId} />;

export function IssueDetailPage({ onDelete }: { onDelete?: () => void }) {
  // React Router hands `name` over decoded; on the plain issue route it is
  // undefined.
  const { id, name } = useParams<{ id: string; name?: string }>();
  // The trees' actions open terminals, and only the desktop app has a
  // terminal route: web does not pass the section at all.
  const taskSessions = useFeatureEnabled(TASK_SESSIONS_V1_FLAG, false);

  if (!id) return null;
  // Render errors bubble to the root route errorElement (DesktopRouteErrorPage),
  // which contains the crash inside the tab content pane. No page-level boundary
  // here — a whole-page wrapper duplicates the route-level error UI.
  return (
    <IssueDetailRoute
      routeId={id}
      onDelete={onDelete}
      deliverable={name}
      trees={taskSessions ? renderTrees : undefined}
    />
  );
}
