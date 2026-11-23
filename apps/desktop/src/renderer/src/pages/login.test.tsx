import { cleanup, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "@multica/views/locales";
import { DesktopLoginPage } from "./login";

// The page says why it is showing. The auth store's `expired` is true for every
// session the server ended; which notice applies depends on the credential.
const auth = vi.hoisted(() => ({ expired: false }));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector?: (state: { expired: boolean }) => unknown) =>
      selector ? selector({ expired: auth.expired }) : { expired: auth.expired },
    { getState: () => ({ sendCode: vi.fn(), verifyCode: vi.fn() }) },
  ),
}));
vi.mock("@multica/core/api", () => ({ api: {} }));
vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ setQueryData: vi.fn() }),
}));
vi.mock("@multica/ui/components/common/multica-icon", () => ({
  MulticaIcon: () => null,
}));
vi.mock("@multica/views/platform", () => ({ DragStrip: () => null }));

beforeEach(() => {
  auth.expired = false;
  localStorage.clear();
  Object.assign(window, {
    desktopAPI: {
      openExternal: vi.fn(),
      runtimeConfig: { ok: true, config: { appUrl: "https://app.example.com" } },
    },
  });
});

function renderPage(preSharedTokenRejected?: boolean) {
  return render(
    <I18nProvider locale="en" resources={{ en: RESOURCES.en }}>
      {preSharedTokenRejected === undefined ? (
        <DesktopLoginPage />
      ) : (
        <DesktopLoginPage preSharedTokenRejected={preSharedTokenRejected} />
      )}
    </I18nProvider>,
  );
}

describe("DesktopLoginPage", () => {
  it("names the refused token and where to fix it when that is why it is showing", () => {
    auth.expired = true;

    renderPage(true);

    expect(
      screen.getByText(
        "The server rejected the authToken from ~/.multica/desktop.json. Check that it matches MULTICA_AUTH_TOKEN on the server, then restart the app.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/your session expired/i)).not.toBeInTheDocument();
  });

  it("keeps the sign-in form as it is otherwise", () => {
    renderPage(false);
    expect(
      screen.queryByText(/the server rejected the authToken/i),
    ).not.toBeInTheDocument();

    cleanup();
    renderPage();
    expect(
      screen.queryByText(/the server rejected the authToken/i),
    ).not.toBeInTheDocument();
  });
});
