"use client";

import { useCallback, useEffect, useRef, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { getApi, ApiError } from "../api";
import { useAuthStore } from "../auth";
import { initAnalytics } from "../analytics";
import { configStore } from "../config";
import { normalizeCommandURL } from "../runtimes/commands";
import { workspaceListOptions } from "../workspace/queries";
import { createLogger } from "../logger";
import type { ClientIdentity } from "./types";

const logger = createLogger("system-user");
const RECOVERY_RETRY_DELAYS_MS = [1_000, 2_000, 4_000, 8_000, 16_000, 30_000] as const;

export function SystemUserInitializer({
  children,
  identity,
}: {
  children: ReactNode;
  identity?: ClientIdentity;
}) {
  const qc = useQueryClient();
  const retryGeneration = useAuthStore((state) => state.retryGeneration);
  const configLoadedRef = useRef(false);
  const configRequestRef = useRef<Promise<boolean> | null>(null);
  const userPendingRef = useRef(false);
  const retryConfigRef = useRef<() => void>(() => {});

  const loadConfig = useCallback((): Promise<boolean> => {
    if (configLoadedRef.current) return Promise.resolve(true);
    if (configRequestRef.current) return configRequestRef.current;

    configStore.getState().setDaemonConfigStatus("loading");
    const request = getApi().getConfig().then((cfg) => {
      configStore.getState().setAttachmentPublicUrl(cfg.attachment_public_url);
      if (cfg.cdn_domain) {
        configStore.getState().setCdnConfig({ cdnDomain: cfg.cdn_domain, cdnSigned: cfg.cdn_signed === true });
      }
      configStore.getState().setAuthConfig({
        allowSignup: false,
        googleClientId: "",
        workspaceCreationDisabled: cfg.workspace_creation_disabled === true,
        vcsIntegrationAvailable: cfg.vcs_integration_available === true,
      });
      const daemonSetupTarget = cfg.daemon_setup_target ?? (
        normalizeCommandURL(cfg.daemon_server_url) && normalizeCommandURL(cfg.daemon_app_url) ? "self_host" : "unknown"
      );
      configStore.getState().setDaemonConfig({
        daemonServerUrl: cfg.daemon_server_url,
        daemonAppUrl: cfg.daemon_app_url,
        daemonSetupTarget,
      });
      configStore.getState().setFeatureFlags(cfg.feature_flags);
      configStore.getState().setServerVersion(cfg.server_version);
      configStore.getState().setLocalWorktreeSupported(cfg.local_worktree_supported === true);
      configStore.getState().setWebhookPayloadConditionsSupported(cfg.webhook_payload_conditions_supported === true);
      configStore.getState().setAgentConversationStartersSupported(cfg.agent_conversation_starters_supported === true);
      configStore.getState().setIssueCreatePropertiesSupported(cfg.issue_create_properties_supported === true);
      configStore.getState().setCommentDeleteKeepRepliesSupported(cfg.comment_delete_keep_replies_supported === true);
      configStore.getState().setThemeStoreEnabled(cfg.theme_store_enabled === true);
      if (cfg.posthog_key) {
        initAnalytics({ key: cfg.posthog_key, host: cfg.posthog_host || "", appVersion: identity?.version, environment: cfg.analytics_environment });
      }
      configLoadedRef.current = true;
      return true;
    }).catch((err) => {
      logger.error("system configuration unavailable", err);
      configStore.getState().setDaemonConfigStatus("error");
      return false;
    }).finally(() => { configRequestRef.current = null; });
    configRequestRef.current = request;
    return request;
  }, [identity?.version]);

  useEffect(() => {
    let cancelled = false;
    let inFlight = false;
    let retryAfterFlight = false;
    let retryIndex = 0;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;

    const retryNow = () => {
      if (cancelled || configLoadedRef.current) return;
      retryIndex = 0;
      if (retryTimer !== undefined) clearTimeout(retryTimer);
      if (inFlight) { retryAfterFlight = true; return; }
      void attempt();
    };

    const attempt = async () => {
      if (cancelled || configLoadedRef.current) return;
      if (retryTimer !== undefined) { clearTimeout(retryTimer); retryTimer = undefined; }
      if (inFlight) { retryAfterFlight = true; return; }
      inFlight = true;
      const loaded = await loadConfig();
      inFlight = false;
      if (cancelled) return;
      if (loaded) { window.removeEventListener("online", retryNow); return; }
      if (retryAfterFlight) { retryAfterFlight = false; void attempt(); return; }
      retryTimer = setTimeout(() => { retryTimer = undefined; void attempt(); }, RECOVERY_RETRY_DELAYS_MS[retryIndex]);
      retryIndex = Math.min(retryIndex + 1, RECOVERY_RETRY_DELAYS_MS.length - 1);
    };

    retryConfigRef.current = retryNow;
    window.addEventListener("online", retryNow);
    void attempt();
    return () => { cancelled = true; retryConfigRef.current = () => {}; window.removeEventListener("online", retryNow); if (retryTimer !== undefined) clearTimeout(retryTimer); };
  }, [loadConfig]);

  useEffect(() => {
    const api = getApi();
    let cancelled = false;
    let settled = false;
    let inFlight = false;
    let retryAfterFlight = false;
    let retryIndex = 0;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;

    const retryNow = () => {
      if (cancelled || settled) return;
      retryIndex = 0;
      if (retryTimer !== undefined) clearTimeout(retryTimer);
      if (inFlight) { retryAfterFlight = true; return; }
      void attempt();
    };

    const warmWorkspaces = () => {
      void qc.fetchQuery(workspaceListOptions()).catch((err: unknown) => {
        if (!cancelled) logger.error("workspace bootstrap failed", err);
      });
    };

    const attempt = async () => {
      if (cancelled || settled || inFlight) return;
      if (retryTimer !== undefined) { clearTimeout(retryTimer); retryTimer = undefined; }
      inFlight = true;
      try {
        const user = await api.getMe();
        if (cancelled) return;
        settled = true;
        window.removeEventListener("online", retryNow);
        useAuthStore.getState().setUser(user);
        warmWorkspaces();
        if (userPendingRef.current) { userPendingRef.current = false; retryConfigRef.current(); }
      } catch (err) {
        if (cancelled) return;
        if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
          settled = true;
          logger.error("fixed system user is unavailable", err);
          return;
        }
        userPendingRef.current = true;
        useAuthStore.setState({ user: null, isLoading: true, status: "recovering" });
        retryTimer = setTimeout(() => { retryTimer = undefined; void attempt(); }, RECOVERY_RETRY_DELAYS_MS[retryIndex]);
        retryIndex = Math.min(retryIndex + 1, RECOVERY_RETRY_DELAYS_MS.length - 1);
      } finally {
        inFlight = false;
        if (retryAfterFlight && !cancelled && !settled) { retryAfterFlight = false; void attempt(); }
      }
    };

    window.addEventListener("online", retryNow);
    void attempt();
    return () => { cancelled = true; window.removeEventListener("online", retryNow); if (retryTimer !== undefined) clearTimeout(retryTimer); };
  }, [qc, retryGeneration]);

  return <>{children}</>;
}
