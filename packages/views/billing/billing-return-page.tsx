"use client";

import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, Loader2, RefreshCw } from "@multica/ui/icons";
import { useAuthStore } from "@multica/core/auth";
import { paths } from "@multica/core/paths";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";

/**
 * Landing page for Stripe Checkout and Billing Portal returns.
 *
 * Stripe only knows the return URLs a deployment configured, and a deployment
 * has exactly one set of them — so they cannot contain a workspace slug. Cloud
 * therefore appends the workspace it already authorized (`workspace_id`) plus
 * `result`, and this page turns that into the slug-based Settings URL the user
 * actually needs. Hardcoding one workspace's slug in the Stripe configuration
 * would strand every other workspace.
 *
 * Three things this page deliberately does NOT do:
 *
 *   - It never trusts a redirect target from the query string. Only
 *     `workspace_id` and `result` are read, and the destination is rebuilt from
 *     `paths`, so a crafted return link cannot become an open redirect.
 *   - It does not report success on its own. `result=success` means Stripe
 *     finished, not that the subscription is recorded; the destination page is
 *     what waits for the webhook. This page only forwards the marker.
 *   - It does not silently drop the user somewhere else when something is
 *     wrong. Paying and then landing on an unexplained page is worse than a
 *     short message, so the failure states below say what happened.
 */

const RESULT_VALUES = new Set([
  "success",
  "cancel",
  "portal",
  "autopilot_addon",
  "autopilot_addon_cancel",
]);
// An add-on return carries its Checkout Session so Billing can confirm the
// payment at once. Only Stripe's ID shape is forwarded.
const SESSION_ID_RE = /^cs_[A-Za-z0-9_]+$/;
// Same shape check the rest of core uses for route UUIDs. Only a well-formed ID
// is worth carrying into the workspace settings URL.
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function BillingReturnPage() {
  const { t } = useT("billing");
  const navigation = useNavigation();
  const isAuthLoading = useAuthStore((s) => s.isLoading);

  const workspaceIdRaw = navigation.searchParams.get("workspace_id");
  const workspaceIdParam =
    workspaceIdRaw && UUID_RE.test(workspaceIdRaw) ? workspaceIdRaw : null;
  const resultParam = navigation.searchParams.get("result");
  const result = resultParam && RESULT_VALUES.has(resultParam) ? resultParam : null;
  const sessionIdParam = navigation.searchParams.get("session_id");
  const addonSessionId =
    result === "autopilot_addon" && sessionIdParam && SESSION_ID_RE.test(sessionIdParam)
      ? sessionIdParam
      : null;

  const {
    data: workspaces,
    isPending: isWorkspacesPending,
    isError: isWorkspacesError,
    refetch: refetchWorkspaces,
  } = useQuery({
    ...workspaceListOptions(),
    enabled: !isAuthLoading,
  });

  const match =
    workspaceIdParam && workspaces
      ? workspaces.find((ws) => ws.id === workspaceIdParam)
      : undefined;
  // Only after the list has actually arrived can "not found" mean anything.
  const isUnresolvable = !!workspaces && !match;

  useEffect(() => {
    if (isAuthLoading) return;
    // The local user record is not a login gate. Resolve the workspace the
    // return already names and open its billing settings. The destination is
    // rebuilt from workspace_id and result, so Stripe's session id and any
    // other query value stay off the URL unless this page itself needs them.
    if (isWorkspacesPending || isWorkspacesError || !workspaces) return;
    if (!match) return;

    const destination = `${paths.workspace(match.slug).settings()}?tab=billing${
      result ? `&result=${result}` : ""
    }${addonSessionId ? `&session_id=${addonSessionId}` : ""}`;
    // replace(), not push(): the Stripe hop should not sit in history where a
    // back navigation would re-run this redirect.
    navigation.replace(destination);
  }, [
    addonSessionId,
    isAuthLoading,
    isWorkspacesError,
    isWorkspacesPending,
    match,
    navigation,
    result,
    workspaces,
  ]);

  // The workspace list is what turns an ID into a slug, so without it there is
  // no destination to send anyone to. Retrying beats an endless spinner.
  //
  // The copy below deliberately claims nothing about the outcome. This page
  // cannot know it: `cancel` means the user backed out, `portal` may have been
  // opened and closed, and even `success` only means Stripe finished — the
  // subscription is not recorded until the webhook lands. Asserting "saved" on a
  // payment result screen is the one thing worse than saying "check Billing".
  if (isWorkspacesError) {
    return (
      <ReturnMessage
        title={t(($) => $.return_page.load_failed_title)}
        description={t(($) => $.return_page.load_failed_description)}
        action={
          <Button
            className="h-11"
            variant="outline"
            onClick={() => void refetchWorkspaces()}
          >
            <RefreshCw />
            {t(($) => $.return_page.retry)}
          </Button>
        }
      />
    );
  }

  // Deleted workspace, revoked membership, a missing or malformed workspace_id,
  // or a hand-edited link.
  if (isUnresolvable) {
    return (
      <ReturnMessage
        title={t(($) => $.return_page.workspace_missing_title)}
        description={t(($) => $.return_page.workspace_missing_description)}
        action={
          <Button className="h-11" onClick={() => navigation.replace(paths.root())}>
            {t(($) => $.return_page.go_to_app)}
          </Button>
        }
      />
    );
  }

  return (
    <div
      className="flex min-h-dvh items-center justify-center bg-background p-6"
      role="status"
      aria-live="polite"
    >
      <div className="flex items-center gap-3 text-body text-muted-foreground">
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" />
        <span>{t(($) => $.return_page.redirecting)}</span>
      </div>
    </div>
  );
}

function ReturnMessage({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action: React.ReactNode;
}) {
  return (
    <div className="flex min-h-dvh items-center justify-center bg-background p-6">
      <div
        className="flex max-w-[60ch] flex-col items-start gap-3"
        role="alert"
        aria-live="polite"
      >
        <div className="flex items-center gap-2 text-body font-medium">
          <AlertCircle className="size-4 text-destructive" />
          <span>{title}</span>
        </div>
        <p className="text-caption leading-5 text-muted-foreground">
          {description}
        </p>
        {action}
      </div>
    </div>
  );
}
