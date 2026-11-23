import type { Issue } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/** "New reply" on a task row while its agent's latest reply is waiting to be
 *  seen. Renders nothing, and reads no translation, once the marker clears. */
export function AgentReplyMarker({
  issue,
  className,
}: {
  issue: Pick<Issue, "agent_replied_at">;
  className?: string;
}) {
  if (!issue.agent_replied_at) return null;
  return <NewReply className={className} />;
}

function NewReply({ className }: { className?: string }) {
  const { t } = useT("issues");
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded-full bg-brand/10 px-1.5 py-0.5 text-micro font-medium whitespace-nowrap text-brand",
        className,
      )}
    >
      {t(($) => $.agent_activity.new_reply)}
    </span>
  );
}
