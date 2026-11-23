-- Restores the schema of 026, 027, 327 and the share-link part of 415 and 419
-- (the seat capacity index itself is rebuilt by 740). Reaction and share-link
-- rows, the inbox rows and preferences of the retired type, the deleted wakeup
-- rules with their receipts, and the share-link seat intents are not restored.
-- No foreign keys (repo rule): the application removed dependent rows itself.
-- The primary key and the indexes of workspace_share_link are rebuilt
-- concurrently by the down directions of 744, 743, 742 and 741.

ALTER TABLE seat_capacity_outbox ADD COLUMN share_link_id UUID;
ALTER TABLE seat_capacity_outbox DROP CONSTRAINT IF EXISTS seat_capacity_outbox_action_check;
ALTER TABLE seat_capacity_outbox ADD CONSTRAINT seat_capacity_outbox_action_check CHECK (action IN (
    'reserve_invitation',
    'consume_invitation',
    'claim_share_join',
    'confirm',
    'release',
    'release_member'
));
ALTER TABLE seat_capacity_outbox ADD CONSTRAINT seat_capacity_outbox_check2
    CHECK ((action <> 'claim_share_join') OR (share_link_id IS NOT NULL AND user_id IS NOT NULL));

CREATE TABLE comment_reaction (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    comment_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    actor_type TEXT NOT NULL CHECK (actor_type IN ('member', 'agent')),
    actor_id UUID NOT NULL,
    emoji TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (comment_id, actor_type, actor_id, emoji)
);

CREATE INDEX idx_comment_reaction_comment_id ON comment_reaction(comment_id);

CREATE TABLE issue_reaction (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    actor_type TEXT NOT NULL CHECK (actor_type IN ('member', 'agent')),
    actor_id UUID NOT NULL,
    emoji TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (issue_id, actor_type, actor_id, emoji)
);

CREATE INDEX idx_issue_reaction_issue_id ON issue_reaction(issue_id);

CREATE TABLE workspace_share_link (
    id           UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    code         TEXT NOT NULL,
    created_by   UUID NOT NULL,
    role         TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member')),
    expires_at   TIMESTAMPTZ,
    max_uses     INT,
    use_count    INT NOT NULL DEFAULT 0,
    is_active    BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE FUNCTION capture_reaction_wakeup() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE row_data jsonb := CASE WHEN TG_OP='DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
 owner_issue uuid; source_id uuid; source_agent uuid; target_type text; target_id uuid; payload jsonb;
BEGIN
 IF TG_TABLE_NAME='comment_reaction' THEN
  target_type := 'comment'; target_id := (row_data->>'comment_id')::uuid;
  SELECT issue_id INTO owner_issue FROM comment WHERE id=target_id AND workspace_id=(row_data->>'workspace_id')::uuid;
 ELSE target_type := 'issue'; target_id := (row_data->>'issue_id')::uuid; owner_issue := target_id;
 END IF;
 source_id := NULLIF(current_setting('multica.source_task_id',true),'')::uuid;
 IF source_id IS NOT NULL THEN SELECT agent_id INTO source_agent FROM agent_task_queue WHERE id=source_id;
 ELSIF TG_OP='INSERT' AND row_data->>'actor_type'='agent' THEN source_agent := (row_data->>'actor_id')::uuid;
 END IF;
 payload := jsonb_build_object('reaction_id',row_data->'id','target_type',target_type,'target_id',target_id,'emoji',row_data->'emoji',
  'reaction_actor_type',row_data->'actor_type','reaction_actor_id',row_data->'actor_id');
 IF TG_OP='INSERT' THEN payload := payload || jsonb_build_object('actor_type',row_data->'actor_type','actor_id',row_data->'actor_id'); END IF;
 PERFORM capture_issue_wakeup(owner_issue,CASE WHEN TG_OP='INSERT' THEN 'reaction.added' ELSE 'reaction.removed' END,
  gen_random_uuid()::text,source_agent,source_id,payload);
 RETURN COALESCE(NEW,OLD);
END $$;
CREATE TRIGGER capture_comment_reaction_wakeup AFTER INSERT OR DELETE ON comment_reaction FOR EACH ROW EXECUTE FUNCTION capture_reaction_wakeup();
CREATE TRIGGER capture_issue_reaction_wakeup AFTER INSERT OR DELETE ON issue_reaction FOR EACH ROW EXECUTE FUNCTION capture_reaction_wakeup();
