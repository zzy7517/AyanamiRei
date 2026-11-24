import { useParams } from "react-router-dom";
import { IssueDetailRoute, IssueTreesSection } from "@multica/views/issues/components";

// Module level so the detail, which re-renders often, keeps the same slot.
const renderTrees = (issueId: string) => <IssueTreesSection issueId={issueId} />;

export function IssueDetailPage({ onDelete }: { onDelete?: () => void }) {
  // React Router hands `name` over decoded; on the plain issue route it is
  // undefined.
  const { id, name } = useParams<{ id: string; name?: string }>();
  if (!id) return null;
  // Render errors bubble to the root route errorElement (DesktopRouteErrorPage),
  // which contains the crash inside the tab content pane. No page-level boundary
  // here — a whole-page wrapper duplicates the route-level error UI.
  return (
    <IssueDetailRoute
      routeId={id}
      onDelete={onDelete}
      deliverable={name}
      trees={renderTrees}
    />
  );
}
