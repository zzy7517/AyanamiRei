"use client";

import { useEffect, useCallback, useMemo } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";
import type { Comment, TimelineEntry } from "@multica/core/types";
import type {
  CommentCreatedPayload,
  CommentUpdatedPayload,
  CommentDeletedPayload,
  CommentResolvedPayload,
  CommentUnresolvedPayload,
  ActivityCreatedPayload,
} from "@multica/core/types";
import {
  issueTimelineOptions,
  issueCommentThreadOptions,
  issueKeys,
} from "@multica/core/issues/queries";
import {
  useCreateComment,
  useUpdateComment,
  useDeleteComment,
  useResolveComment,
} from "@multica/core/issues/mutations";
import { commentToTimelineEntry, mergeCommentThread } from "@multica/core/issues/comment-thread";
import { sortTimelineEntriesAsc } from "@multica/core/issues/timeline-sort";
import {
  unhandledCommentTriggerOutcomes,
  mentionLabelsByTarget,
} from "@multica/core/issues/comment-trigger-outcomes";
import { useWSEvent, useWSReconnect } from "@multica/core/realtime";
import { removeCommentSubtree } from "@multica/core/issues/comment-deletion";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { blockedShortReasonLabel } from "../blocked-trigger-copy";

type TLCache = TimelineEntry[];


function acceptsCommentRevision(
  current: TimelineEntry,
  incoming: Comment,
): boolean {
  return (
    current.revision === undefined ||
    (incoming.revision !== undefined && incoming.revision >= current.revision)
  );
}

function applyCommentSnapshot(
  qc: QueryClient,
  issueId: string,
  comment: Comment,
) {
  let missingRevision = false;
  qc.setQueryData<TLCache>(issueKeys.timeline(issueId), (old) =>
    old?.map((entry) => {
      if (entry.id !== comment.id) return entry;
      if (entry.revision !== undefined && comment.revision === undefined) {
        missingRevision = true;
        return entry;
      }
      return acceptsCommentRevision(entry, comment)
        ? {
            ...commentToTimelineEntry(comment),
            actor_name: entry.actor_name,
            actor_avatar_url: entry.actor_avatar_url,
          }
        : entry;
    }),
  );
  if (missingRevision) {
    // During a rolling deployment, an old server may emit an unversioned
    // snapshot after this cache has already observed a versioned one. Never
    // overwrite the ordered value; refetch from the authoritative API instead.
    qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
  }
}

export function useIssueTimeline(issueId: string, userId?: string, focusCommentId?: string) {
  const { t } = useT("issues");
  const qc = useQueryClient();

  const query = useQuery(issueTimelineOptions(issueId));
  const { data, isLoading: loading, dataUpdatedAt } = query;

  const needsThread = !!focusCommentId && !loading && !data?.some((entry) => entry.id === focusCommentId);
  const threadQuery = useQuery({
    ...issueCommentThreadOptions(issueId, focusCommentId ?? ""),
    enabled: needsThread,
  });
  useEffect(() => {
    if (threadQuery.error && needsThread) toast.error(t(($) => $.detail.load_failed));
  }, [threadQuery.error, needsThread, t]);
  const timeline = useMemo<TimelineEntry[]>(
    () => mergeCommentThread(data ?? [], threadQuery.data ?? []),
    [data, threadQuery.data],
  );

  // Stable mutation handles. TanStack v5 returns a fresh result wrapper from
  // useMutation per render, but the inner mutateAsync / mutate functions are
  // stable. Pull just those so the useCallback identities downstream don't
  // flip on every parent re-render — listing the whole mutation object would
  // defeat React.memo on CommentCard.
  const { mutateAsync: createComment } = useCreateComment(issueId);
  const { mutateAsync: updateComment } = useUpdateComment(issueId);
  const { mutateAsync: deleteCommentAsync } = useDeleteComment(issueId);
  const { mutateAsync: resolveCommentAsync } = useResolveComment(issueId);

  // Reconnect recovery: invalidate so the next render refetches the full
  // timeline. Cheaper than diffing across a possibly-long disconnect.
  useWSReconnect(
    useCallback(() => {
      qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
    }, [qc, issueId]),
  );

  // --- WS event handlers ---

  useWSEvent(
    "comment:created",
    useCallback(
      (payload: unknown) => {
        const { comment } = payload as CommentCreatedPayload;
        if (comment.issue_id !== issueId) return;
        qc.setQueryData<TLCache>(issueKeys.timeline(issueId), (old) => {
          const entry = commentToTimelineEntry(comment);
          if (!old) return [entry];
          if (old.some((e) => e.id === comment.id)) return old;
          return sortTimelineEntriesAsc([...old, entry]);
        });
      },
      [qc, issueId],
    ),
  );

  useWSEvent(
    "comment:updated",
    useCallback(
      (payload: unknown) => {
        const { comment } = payload as CommentUpdatedPayload;
        if (comment.issue_id !== issueId) return;
        applyCommentSnapshot(qc, issueId, comment);
      },
      [qc, issueId],
    ),
  );

  // Granular handlers for comment:resolved / comment:unresolved. The payload
  // carries the full Comment with the new resolved_at/resolved_by_* fields,
  // which `commentToTimelineEntry` already preserves, so the existing
  // entry can simply be replaced in place. Without these handlers the only
  // path that updated the cache was `useRealtimeSync`'s global invalidate,
  // which forces a full timeline refetch and busts every CommentCard memo.
  useWSEvent(
    "comment:resolved",
    useCallback(
      (payload: unknown) => {
        const { comment } = payload as CommentResolvedPayload;
        if (comment.issue_id !== issueId) return;
        applyCommentSnapshot(qc, issueId, comment);
      },
      [qc, issueId],
    ),
  );

  useWSEvent(
    "comment:unresolved",
    useCallback(
      (payload: unknown) => {
        const { comment } = payload as CommentUnresolvedPayload;
        if (comment.issue_id !== issueId) return;
        applyCommentSnapshot(qc, issueId, comment);
      },
      [qc, issueId],
    ),
  );

  useWSEvent(
    "comment:deleted",
    useCallback(
      (payload: unknown) => {
        const { comment_id, issue_id } = payload as CommentDeletedPayload;
        if (issue_id !== issueId) return;
        // A comment with replies is tombstoned (comment:updated), never
        // removed, so any cached reply of a removed comment is stale: older
        // servers cascaded the delete to every descendant.
        qc.setQueryData<TLCache>(issueKeys.timeline(issueId), (old) =>
          old ? removeCommentSubtree(old, comment_id) : old,
        );
      },
      [qc, issueId],
    ),
  );

  useWSEvent(
    "activity:created",
    useCallback(
      (payload: unknown) => {
        const p = payload as ActivityCreatedPayload;
        if (p.issue_id !== issueId) return;
        const entry = p.entry;
        if (!entry || !entry.id) return;
        qc.setQueryData<TLCache>(issueKeys.timeline(issueId), (old) => {
          if (!old) return [entry];
          if (old.some((e) => e.id === entry.id)) return old;
          return sortTimelineEntriesAsc([...old, entry]);
        });
      },
      [qc, issueId],
    ),
  );

  // --- Mutation functions ---

  // The comment saved, but a mention did not clearly trigger (blocked, or an
  // unknown/future status we must not assume succeeded). Warn instead of a
  // silent no-op (MUL-4525 §2): the comment IS posted, but N explicitly-named
  // targets were not triggered.
  const warnUnhandledTriggers = useCallback(
    (triggerOutcomes: unknown, content?: string) => {
      const unhandled = unhandledCommentTriggerOutcomes(triggerOutcomes);
      if (unhandled.length === 0) return;
      // Name the target when a single mention was refused — the posted comment's
      // markup carries the label the user typed (the wire outcome omits it for
      // enumeration-safety). Several refusals fall back to a count.
      if (unhandled.length === 1) {
        const outcome = unhandled[0]!;
        const name = mentionLabelsByTarget(content ?? "").get(
          `${outcome.target_type}:${outcome.target_id}`,
        );
        if (name) {
          toast.warning(
            t(($) => $.comment.posted_partial_trigger_named, {
              name,
              reason: blockedShortReasonLabel(outcome.reason_code, t),
            }),
          );
          return;
        }
      }
      toast.warning(
        t(($) => $.comment.posted_partial_trigger, { count: unhandled.length }),
      );
    },
    [t],
  );

  // Returns true on success, false on failure. The composer keeps the user's
  // text (editor locked + button spinning) until this settles and clears only
  // on success — so a slow send no longer leaves the box full next to an
  // already-posted comment, and a failed send keeps the draft.
  const submitComment = useCallback(
    async (content: string, attachmentIds?: string[], suppressAgentIds?: string[], steerTaskIds?: string[]): Promise<string | false> => {
      if (!content.trim() || !userId) return false;
      try {
        const comment = await createComment({ content, attachmentIds, suppressAgentIds, steerTaskIds });
        warnUnhandledTriggers(comment?.trigger_outcomes, comment?.content);
        return comment.id;
      } catch (err) {
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.comment.send_failed),
        );
        return false;
      }
    },
    [userId, createComment, warnUnhandledTriggers, t],
  );

  const submitReply = useCallback(
    async (parentId: string, content: string, attachmentIds?: string[], suppressAgentIds?: string[], steerTaskIds?: string[]): Promise<string | false> => {
      if (!content.trim() || !userId) return false;
      try {
        const comment = await createComment({
          content,
          type: "comment",
          parentId,
          attachmentIds,
          suppressAgentIds,
          steerTaskIds,
        });
        warnUnhandledTriggers(comment?.trigger_outcomes, comment?.content);
        return comment.id;
      } catch (err) {
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.comment.send_reply_failed),
        );
        return false;
      }
    },
    [userId, createComment, warnUnhandledTriggers, t],
  );

  const editComment = useCallback(
    async (commentId: string, content: string, attachmentIds: string[], suppressAgentIds?: string[], contentBase?: string) => {
      const comment = await updateComment({
        commentId,
        content,
        attachmentIds,
        suppressAgentIds,
        contentBase,
      });
      warnUnhandledTriggers(comment?.trigger_outcomes, comment?.content);
    },
    [updateComment, warnUnhandledTriggers],
  );

  const deleteComment = useCallback(
    async (commentId: string) => {
      try {
        await deleteCommentAsync(commentId);
      } catch (err) {
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.comment.delete_failed),
        );
      }
    },
    [deleteCommentAsync, t],
  );

  const toggleResolveComment = useCallback(
    async (commentId: string, resolved: boolean) => {
      try {
        await resolveCommentAsync({ commentId, resolved });
      } catch (err) {
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : resolved
              ? t(($) => $.comment.resolve.resolve_failed)
              : t(($) => $.comment.resolve.unresolve_failed),
        );
      }
    },
    [resolveCommentAsync, t],
  );

  return {
    timeline,
    loading: loading || (needsThread && threadQuery.isPending),
    /**
     * When the cached timeline was last written (Query's `dataUpdatedAt`).
     * Lets a caller tell whether `timeline` already reflects a read it
     * started; `loading` cannot, as it is false while cached data refreshes.
     */
    dataUpdatedAt,
    submitComment,
    submitReply,
    editComment,
    deleteComment,
    toggleResolveComment,
  };
}
