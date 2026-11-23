import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";
import { agentReplyBody } from "@multica/core/issues/agent-reply";
import { I18nProvider } from "@multica/core/i18n/react";
import { getTestResources } from "../../test/i18n";
import { useAgentReplyNotificationText } from "./use-agent-reply-notification-text";

function Wrapper({ children }: { children: ReactNode }) {
  return <I18nProvider locale="en" resources={getTestResources("en")}>{children}</I18nProvider>;
}

describe("useAgentReplyNotificationText", () => {
  it("gives the banner its wording while the tree is mounted, and takes it back", () => {
    expect(agentReplyBody()).toBe("");

    const view = renderHook(() => useAgentReplyNotificationText(), { wrapper: Wrapper });
    expect(agentReplyBody()).toBe("New reply");

    view.unmount();
    expect(agentReplyBody()).toBe("");
  });
});
