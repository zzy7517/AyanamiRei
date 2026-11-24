import { describe, expect, it } from "vitest";
import { NextRequest } from "next/server";
import { proxy } from "../proxy";
import manifest, { PWA_START_URL } from "./manifest";

function launch(cookies: Record<string, string>, host = "www.multica.ai") {
  const cookieHeader = Object.entries(cookies)
    .map(([key, value]) => `${key}=${value}`)
    .join("; ");

  return proxy(
    new NextRequest(`https://${host}${PWA_START_URL}`, {
      headers: cookieHeader ? { cookie: cookieHeader } : undefined,
    }),
  ).headers.get("location");
}

describe("web app manifest", () => {

  it("does not launch at the root path", () => {
    // "/" is a redirect shell (last workspace, or the fixed local workspace) rather than a page;
    // the launch path deep-links into the inbox route of the resolved
    // workspace instead of relying on the root redirect's /issues default.
    expect(manifest().start_url).not.toBe("/");
  });

  it("launches into the last workspace", () => {
    expect(launch({ last_workspace_slug: "acme" })).toContain("/acme/inbox");
  });

  it("launches into the fixed workspace when no slug is known", () => {
    expect(launch({})).toContain("/dev");
  });

  it("points every shortcut at a path that resolves the same way", () => {
    const shortcuts = manifest().shortcuts ?? [];

    expect(shortcuts.length).toBeGreaterThan(0);
    for (const shortcut of shortcuts) {
      const resolve = (cookie: string) =>
        proxy(
          new NextRequest(`https://www.multica.ai${shortcut.url}`, {
            headers: { cookie },
          }),
        ).headers.get("location");

      expect(resolve("last_workspace_slug=acme")).toContain(
        `/acme${shortcut.url}`,
      );
      expect(resolve("")).toContain("/dev");
    }
  });
});
