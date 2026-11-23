import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { StreamingMarkdown } from "./StreamingMarkdown";

afterEach(cleanup);

describe("StreamingMarkdown mention links", () => {
  it("shows a link to a member or to @all as plain text without a link", () => {
    render(
      <StreamingMarkdown
        isStreaming
        content={
          "Hi [@Ada](mention://member/11111111-1111-4111-8111-111111111111) and [@all](mention://all/all)"
        }
      />,
    );

    expect(screen.getByText("Hi @Ada and @all")).toBeInTheDocument();
    expect(document.querySelector("a")).toBeNull();
  });

  it("shows a legacy member shortcode as its plain label", () => {
    render(
      <StreamingMarkdown
        isStreaming
        content={'Hi [@ id="11111111-1111-4111-8111-111111111111" label="Ada"].'}
      />,
    );

    expect(screen.getByText("Hi @Ada.")).toBeInTheDocument();
    expect(document.querySelector("a")).toBeNull();
  });
});
