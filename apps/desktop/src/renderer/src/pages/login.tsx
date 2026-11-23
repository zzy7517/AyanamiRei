import { LoginPage } from "@multica/views/auth";
import { DragStrip } from "@multica/views/platform";
import { useT } from "@multica/views/i18n";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";

function requireRuntimeAppUrl(): string {
  const runtimeConfig = window.desktopAPI.runtimeConfig;
  if (!runtimeConfig.ok) {
    throw new Error(
      "Invariant violated: DesktopLoginPage rendered before App accepted runtime config",
    );
  }
  return runtimeConfig.config.appUrl;
}

export function DesktopLoginPage({
  preSharedTokenRejected = false,
}: {
  /** The server refused the pre-shared token from desktop.json, which is why
   *  this page is showing. */
  preSharedTokenRejected?: boolean;
} = {}) {
  const webUrl = requireRuntimeAppUrl();
  const { t } = useT("auth");
  const handleGoogleLogin = () => {
    // Open web login page in the default browser with platform=desktop flag.
    // The web callback will redirect back via multica:// deep link with the token.
    window.desktopAPI.openExternal(
      `${webUrl}/login?platform=desktop`,
    );
  };

  return (
    <div className="flex h-screen flex-col">
      <DragStrip />
      <LoginPage
        logo={<MulticaIcon mode="intro" size={48} />}
        onSuccess={() => {
          // Auth store update triggers AppContent re-render → shows DesktopShell.
          // Initial workspace navigation happens in routes.tsx via IndexRedirect.
        }}
        onGoogleLogin={handleGoogleLogin}
        expiredMessage={
          preSharedTokenRejected ? t(($) => $.desktop.token_rejected) : undefined
        }
      />
    </div>
  );
}
