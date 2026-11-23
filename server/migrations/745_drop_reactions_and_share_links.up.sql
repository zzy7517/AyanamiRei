-- Drop comment and issue reactions and share links. What exists only for them
-- is deleted before the schema goes:
--   - the inbox rows of the retired reaction_added type, and the reactions key
--     of notification preferences, which muted that type alone;
--   - wakeup rules that listened only for reactions, and every receipt of a
--     reaction event; a rule that also listened for other events keeps them;
--   - a seat capacity intent for a share-link join becomes a release, which is
--     how the worker retires an operation it cannot finish.
--
-- Dropping the tables takes their indexes; 740-744 removed the concurrently
-- built ones first so a rollback can rebuild each of them the same way.

DELETE FROM inbox_item WHERE type = 'reaction_added';

UPDATE notification_preference
SET preferences = preferences - 'reactions'
WHERE preferences ? 'reactions';

DELETE FROM issue_wakeup_receipt WHERE event_type IN ('reaction.added', 'reaction.removed');
-- A rule that listened only for reactions could never fire again.
DELETE FROM issue_wakeup
WHERE kind = 'event' AND condition IS NULL
  AND event_types && ARRAY['reaction.added', 'reaction.removed']::text[]
  AND event_types <@ ARRAY['reaction.added', 'reaction.removed']::text[];
UPDATE issue_wakeup
SET event_types = array_remove(array_remove(event_types, 'reaction.added'), 'reaction.removed')
WHERE kind = 'event';

UPDATE seat_capacity_outbox
SET action = 'release',
    delivered_at = NULL,
    attempt_count = 0,
    next_attempt_at = now(),
    lease_token = NULL,
    last_error = NULL,
    dead_lettered_at = NULL,
    updated_at = now()
WHERE action = 'claim_share_join';

DROP TRIGGER IF EXISTS capture_comment_reaction_wakeup ON comment_reaction;
DROP TRIGGER IF EXISTS capture_issue_reaction_wakeup ON issue_reaction;
DROP FUNCTION IF EXISTS capture_reaction_wakeup();

DROP TABLE IF EXISTS comment_reaction;
DROP TABLE IF EXISTS issue_reaction;
DROP TABLE IF EXISTS workspace_share_link;

ALTER TABLE seat_capacity_outbox DROP CONSTRAINT IF EXISTS seat_capacity_outbox_action_check;
ALTER TABLE seat_capacity_outbox ADD CONSTRAINT seat_capacity_outbox_action_check CHECK (action IN (
    'reserve_invitation',
    'consume_invitation',
    'confirm',
    'release',
    'release_member'
));
-- The check that ties claim_share_join to its link and user reads
-- share_link_id, so it goes with the column.
ALTER TABLE seat_capacity_outbox DROP COLUMN IF EXISTS share_link_id;
