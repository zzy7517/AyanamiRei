"use client";

import { useEffect, useState } from "react";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Button } from "@multica/ui/components/ui/button";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { paths } from "@multica/core/paths";
import { AppLink } from "../navigation";
import { useT } from "../i18n";

type RedeemState =
  | { kind: "idle" }
  | { kind: "redeeming" }
  | { kind: "done"; workspaceId: string; installationId: string }
  | { kind: "needs-auth" }
  | { kind: "error"; reason: string };

// WecomBindPage is the destination the WeCom smart-bot's "link your Multica
// account" prompt points at. Redemption waits for the local user record,
// because the server binds the installation to that user. There is no login
// page: if that record never arrives, the page sends the visitor into the app.
//
// The token comes in via ?token=<raw>. We POST it to /api/wecom/binding/
// redeem; the backend returns 410 (invalid/expired), 409 (already bound to
// another user), 403 (not a workspace member) or 200 with the bound
// installation. Each maps to distinct copy via wecom_bind in common.json.
export function WecomBindPage({ token }: { token: string | null }) {
  const { t } = useT("common");
  const user = useAuthStore((s) => s.user);
  const isAuthLoading = useAuthStore((s) => s.isLoading);
  const [state, setState] = useState<RedeemState>({ kind: "idle" });

  useEffect(() => {
    if (!token) {
      setState({ kind: "error", reason: "missing_token" });
      return;
    }
    if (isAuthLoading) return;
    if (!user) {
      setState({ kind: "needs-auth" });
      return;
    }
    if (state.kind !== "idle" && state.kind !== "needs-auth") return;
    setState({ kind: "redeeming" });
    (async () => {
      try {
        const resp = await api.redeemWecomBindingToken(token);
        setState({
          kind: "done",
          workspaceId: resp.workspace_id,
          installationId: resp.installation_id,
        });
      } catch (e) {
        setState({
          kind: "error",
          reason: redemptionFailureReason(e),
        });
      }
    })();
  }, [token, user, isAuthLoading, state.kind]);

  return (
    <div className="mx-auto flex min-h-screen max-w-md flex-col items-center justify-center p-6">
      <Card className="w-full">
        <CardContent className="space-y-4">
          <h1 className="text-title font-semibold">{t(($) => $.wecom_bind.page_title)}</h1>
          {state.kind === "idle" || state.kind === "redeeming" ? (
            <p className="text-body text-muted-foreground">{t(($) => $.wecom_bind.redeeming)}</p>
          ) : state.kind === "needs-auth" ? (
            <Button
              size="sm"
              render={<AppLink href={paths.root()} />}
              nativeButton={false}
            >
              {t(($) => $.not_found.back_to_multica)}
            </Button>
          ) : state.kind === "done" ? (
            <>
              <p className="text-body font-medium">{t(($) => $.wecom_bind.done_title)}</p>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.wecom_bind.done_description)}
              </p>
            </>
          ) : (
            <>
              <p className="text-body font-medium">{t(($) => $.wecom_bind.error_title)}</p>
              <p className="text-caption text-muted-foreground">
                {(() => {
                  switch (state.reason) {
                    case "missing_token":
                      return t(($) => $.wecom_bind.error_missing_token);
                    case "expired":
                      return t(($) => $.wecom_bind.error_expired);
                    case "already_bound":
                      return t(($) => $.wecom_bind.error_already_bound);
                    case "not_member":
                      return t(($) => $.wecom_bind.error_not_member);
                    default:
                      return t(($) => $.wecom_bind.error_unknown);
                  }
                })()}
              </p>
              <p className="text-micro text-muted-foreground">
                {t(($) => $.wecom_bind.error_admin_hint)}
              </p>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function redemptionFailureReason(err: unknown): string {
  const msg = err instanceof Error ? err.message : "";
  const lower = msg.toLowerCase();
  if (lower.includes("invalid") || lower.includes("expired") || lower.includes("410")) {
    return "expired";
  }
  if (lower.includes("already bound") || lower.includes("409")) {
    return "already_bound";
  }
  if (lower.includes("workspace member") || lower.includes("403")) {
    return "not_member";
  }
  return "unknown";
}
