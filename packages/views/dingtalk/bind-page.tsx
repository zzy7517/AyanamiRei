"use client";

import { useEffect, useRef, useState } from "react";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Button } from "@multica/ui/components/ui/button";
import { api, ApiError } from "@multica/core/api";
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

// DingTalkBindPage is the destination the bot's "link your account" prompt
// points at. Redemption waits for the local user record, because the server
// binds the installation to that user. There is no login page: if that
// record never arrives, the page sends the visitor into the app.
//
// The token comes in via `?token=<raw>`. We POST it to
// /api/dingtalk/binding/redeem; the backend returns 410 (invalid/expired),
// 409 (already bound to another user), 403 (not a workspace member) or 200 with
// the bound installation. Each maps to distinct copy via dingtalk_bind in
// common.json.
export function DingTalkBindPage({ token }: { token: string | null }) {
  const { t } = useT("common");
  const user = useAuthStore((s) => s.user);
  const isAuthLoading = useAuthStore((s) => s.isLoading);
  const [state, setState] = useState<RedeemState>({ kind: "idle" });
  const redeemingToken = useRef<string | null>(null);

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
    if (redeemingToken.current === token) return;
    redeemingToken.current = token;
    setState({ kind: "redeeming" });
    (async () => {
      try {
        const resp = await api.redeemDingTalkBindingToken(token);
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
          <h1 className="text-title font-semibold">{t(($) => $.dingtalk_bind.page_title)}</h1>
          {state.kind === "idle" || state.kind === "redeeming" ? (
            <p className="text-body text-muted-foreground">{t(($) => $.dingtalk_bind.redeeming)}</p>
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
              <p className="text-body font-medium">{t(($) => $.dingtalk_bind.done_title)}</p>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.dingtalk_bind.done_description)}
              </p>
            </>
          ) : (
            <>
              <p className="text-body font-medium">{t(($) => $.dingtalk_bind.error_title)}</p>
              <p className="text-caption text-muted-foreground">
                {(() => {
                  switch (state.reason) {
                    case "missing_token":
                      return t(($) => $.dingtalk_bind.error_missing_token);
                    case "expired":
                      return t(($) => $.dingtalk_bind.error_expired);
                    case "already_bound":
                      return t(($) => $.dingtalk_bind.error_already_bound);
                    case "not_member":
                      return t(($) => $.dingtalk_bind.error_not_member);
                    default:
                      return t(($) => $.dingtalk_bind.error_unknown);
                  }
                })()}
              </p>
              <p className="text-micro text-muted-foreground">
                {t(($) => $.dingtalk_bind.error_admin_hint)}
              </p>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

// Classify a redeem failure by the server's HTTP status, not the error text:
// the backend returns 410 (invalid/expired), 409 (already bound to another
// user) or 403 (not a workspace member). Matching ApiError.status keeps the
// branch copy decoupled from the exact server wording.
function redemptionFailureReason(err: unknown): string {
  if (!(err instanceof ApiError)) return "unknown";
  switch (err.status) {
    case 410:
      return "expired";
    case 409:
      return "already_bound";
    case 403:
      return "not_member";
    default:
      return "unknown";
  }
}
