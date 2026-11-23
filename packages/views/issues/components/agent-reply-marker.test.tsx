import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { AgentReplyMarker } from "./agent-reply-marker";

describe("AgentReplyMarker", () => {
  it("says a reply is waiting, in words assistive technology reads as the name", () => {
    renderWithI18n(<AgentReplyMarker issue={{ agent_replied_at: "2026-11-22T10:00:00Z" }} />);

    expect(screen.getByText("New reply")).toBeVisible();
  });

  it("renders nothing once the reply is seen, and for a task built without the field", () => {
    // No translation provider here: nothing may be read once the marker is gone.
    const view = render(<AgentReplyMarker issue={{ agent_replied_at: null }} />);
    expect(view.container).toBeEmptyDOMElement();

    view.rerender(<AgentReplyMarker issue={{}} />);
    expect(view.container).toBeEmptyDOMElement();
  });
});
