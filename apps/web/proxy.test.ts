import { describe, expect, it } from "vitest";
import { unstable_doesMiddlewareMatch } from "next/experimental/testing/server";
import { NextRequest } from "next/server";
import { MULTICA_LOCALE_HEADER } from "./lib/locale-routing";
import { config, proxy } from "./proxy";

function makeRequest(
  path: string,
  cookies: Record<string, string> = {},
  host = "app.multica.test",
) {
  const cookieHeader = Object.entries(cookies)
    .map(([key, value]) => `${key}=${value}`)
    .join("; ");

  return new NextRequest(`https://${host}${path}`, {
    headers: cookieHeader ? { cookie: cookieHeader } : undefined,
  });
}

function redirectLocation(
  path: string,
  cookies: Record<string, string> = {},
  host?: string,
) {
  return proxy(makeRequest(path, cookies, host)).headers.get("location");
}

function restoreEnv(key: string, value: string | undefined) {
  if (value === undefined) delete process.env[key];
  else process.env[key] = value;
}

function withoutRuntimeUpstreams(run: () => void) {
  const previousRemoteApiUrl = process.env.REMOTE_API_URL;
  const previousDocsUrl = process.env.DOCS_URL;
  const previousPublicApiUrl = process.env.NEXT_PUBLIC_API_URL;
  const previousPort = process.env.PORT;
  delete process.env.REMOTE_API_URL;
  delete process.env.DOCS_URL;
  delete process.env.NEXT_PUBLIC_API_URL;
  process.env.PORT = "3000";

  try {
    run();
  } finally {
    restoreEnv("REMOTE_API_URL", previousRemoteApiUrl);
    restoreEnv("DOCS_URL", previousDocsUrl);
    restoreEnv("NEXT_PUBLIC_API_URL", previousPublicApiUrl);
    restoreEnv("PORT", previousPort);
  }
}

describe("proxy legacy workspace route redirects", () => {
  const sessionCookies = { last_workspace_slug: "acme" };

  it.each([
    ["issues", "/acme/issues"],
    ["projects", "/acme/projects"],
    ["agents", "/acme/agents"],
    ["inbox", "/acme/inbox"],
    ["autopilots", "/acme/autopilots"],
    ["runtimes", "/acme/runtimes"],
    ["skills", "/acme/skills"],
    ["settings", "/acme/settings"],
    ["usage", "/acme/usage"],
  ])(
    "redirects legacy /%s URLs through the last workspace slug",
    (segment, expectedPath) => {
      expect(redirectLocation(`/${segment}?tab=all`, sessionCookies)).toBe(
        `https://app.multica.test${expectedPath}?tab=all`,
      );
    },
  );

  it("preserves nested legacy paths and query strings", () => {
    expect(
      redirectLocation("/projects/project-123?view=members", sessionCookies),
    ).toBe("https://app.multica.test/acme/projects/project-123?view=members");
  });

  it("sends legacy URLs without a last workspace cookie to the fixed workspace", () => {
    expect(redirectLocation("/projects?view=members")).toBe(
      "https://app.multica.test/dev",
    );
  });

  it("does not redirect workspace-scoped URLs whose first segment is already a slug", () => {
    expect(redirectLocation("/acme/projects", sessionCookies)).toBeNull();
  });
});

describe("proxy runtime upstream rewrites", () => {
  it("matches globally versioned Plugin API requests", () => {
    expect(config.matcher).toContain("/v1/:path*");
  });

  it("does not rewrite API requests when no runtime API origin is configured", () => {
    withoutRuntimeUpstreams(() => {
      const res = proxy(makeRequest("/api/config?x=1"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBeNull();
      expect(
        res.headers.get(`x-middleware-request-${MULTICA_LOCALE_HEADER}`),
      ).toBe("en");
    });
  });

  it("does not rewrite Plugin API requests when no runtime API origin is configured", () => {
    withoutRuntimeUpstreams(() => {
      const res = proxy(makeRequest("/v1/context?x=1"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBeNull();
    });
  });

  it("does not rewrite docs requests when no runtime docs origin is configured", () => {
    withoutRuntimeUpstreams(() => {
      const res = proxy(makeRequest("/docs/zh"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBeNull();
      expect(
        res.headers.get(`x-middleware-request-${MULTICA_LOCALE_HEADER}`),
      ).toBe("en");
    });
  });

  it("rewrites API requests to the runtime API origin", () => {
    const previous = process.env.REMOTE_API_URL;
    process.env.REMOTE_API_URL = "http://backend:8080";
    try {
      const res = proxy(makeRequest("/api/config?x=1"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBe(
        "http://backend:8080/api/config?x=1",
      );
    } finally {
      restoreEnv("REMOTE_API_URL", previous);
    }
  });

  it("rewrites the CLI health probe to the runtime API origin", () => {
    const previous = process.env.REMOTE_API_URL;
    process.env.REMOTE_API_URL = "http://backend:8080";
    try {
      const res = proxy(makeRequest("/health"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBe(
        "http://backend:8080/health",
      );
    } finally {
      restoreEnv("REMOTE_API_URL", previous);
    }
  });

  it("rewrites Plugin API requests to the runtime API origin", () => {
    const previous = process.env.REMOTE_API_URL;
    process.env.REMOTE_API_URL = "http://backend:8080";
    try {
      const res = proxy(makeRequest("/v1/issues/MUL-6581?x=1"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBe(
        "http://backend:8080/v1/issues/MUL-6581?x=1",
      );
    } finally {
      restoreEnv("REMOTE_API_URL", previous);
    }
  });

  it("rewrites docs requests to the runtime docs origin", () => {
    const previous = process.env.DOCS_URL;
    process.env.DOCS_URL = "http://docs:4000";
    try {
      const res = proxy(makeRequest("/docs/zh/agents"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBe(
        "http://docs:4000/docs/zh/agents",
      );
    } finally {
      restoreEnv("DOCS_URL", previous);
    }
  });

  it("rewrites websocket requests to the runtime API origin", () => {
    const previous = process.env.REMOTE_API_URL;
    process.env.REMOTE_API_URL = "http://backend:8080";
    try {
      const res = proxy(makeRequest("/ws"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBe(
        "http://backend:8080/ws",
      );
    } finally {
      restoreEnv("REMOTE_API_URL", previous);
    }
  });

  it("does not rewrite retired auth routes", () => {
    const previous = process.env.REMOTE_API_URL;
    process.env.REMOTE_API_URL = "http://backend:8080";
    try {
      const res = proxy(makeRequest("/auth/callback"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBeNull();
      expect(
        res.headers.get(`x-middleware-request-${MULTICA_LOCALE_HEADER}`),
      ).toBe("en");
    } finally {
      restoreEnv("REMOTE_API_URL", previous);
    }
  });
});

describe("proxy root and locale handling", () => {
  const sessionCookies = { last_workspace_slug: "acme" };

  it("redirects root visits to the last workspace", () => {
    const res = proxy(
      makeRequest("/", { last_workspace_slug: "acme" }),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe(
      "https://app.multica.test/acme/issues",
    );
  });

  it("preserves the query string on the root redirect", () => {
    expect(
      redirectLocation("/?view=board", sessionCookies),
    ).toBe("https://app.multica.test/acme/issues?view=board");
  });

  it("redirects the root to the fixed workspace when no slug is known", () => {
    expect(redirectLocation("/")).toBe(
      "https://app.multica.test/dev/issues",
    );
  });

  it("forwards locale on workspace routes", () => {
    const res = proxy(makeRequest("/dev/issues", { "multica-locale": "zh-Hans" }));

    expect(res.status).toBe(200);
    expect(res.headers.get("location")).toBeNull();
    expect(
      res.headers.get(`x-middleware-request-${MULTICA_LOCALE_HEADER}`),
    ).toBe("zh-Hans");
  });

  it("forwards locale on workspace routes", () => {
    const res = proxy(makeRequest("/acme/issues", { "multica-locale": "ja" }));

    expect(res.status).toBe(200);
    expect(res.headers.get("location")).toBeNull();
    expect(
      res.headers.get(`x-middleware-request-${MULTICA_LOCALE_HEADER}`),
    ).toBe("ja");
  });
});

describe("upload route bypasses the buffering proxy", () => {
  it.each(["/api/upload-file", "/api/upload-file/"])(
    "excludes %s from Proxy matching",
    (url) => expect(unstable_doesMiddlewareMatch({ config, url })).toBe(false),
  );

  it.each(["/api", "/api/config", "/api/upload-file-extra", "/v1/issues", "/docs/intro", "/onboarding"])(
    "keeps the existing proxy behavior for %s",
    (url) => expect(unstable_doesMiddlewareMatch({ config, url })).toBe(true),
  );
});
