import { useEffect } from "react";
import { registerAgentReplyBody } from "@multica/core/issues/agent-reply";
import { useT } from "../../i18n";

/** Gives the "new reply" banner its wording, in the language the app shows.
 *  The banner names the task itself and takes nothing else from here. */
export function useAgentReplyNotificationText() {
  const { t } = useT("issues");
  useEffect(() => {
    registerAgentReplyBody(t(($) => $.agent_activity.new_reply));
    return () => registerAgentReplyBody(null);
  }, [t]);
}
