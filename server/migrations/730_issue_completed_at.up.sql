-- When a task's status entered done or closed, built-in or custom; NULL while
-- its status is in any other category. A trigger stamps it, so every writer
-- of issue.status (HTTP update, bulk and board moves, PR automation,
-- wakeups, daemon reports, the runtime sweeper, raw SQL) is covered without
-- knowing about it, as record_issue_child_event covers sub-issue closure. A
-- task already closed when the column arrived is stamped by its next status
-- write.
ALTER TABLE issue ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;

CREATE OR REPLACE FUNCTION stamp_issue_completed_at() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE closed boolean;
BEGIN
 closed := NEW.status IN ('done','cancelled') OR EXISTS(SELECT 1 FROM issue_status s WHERE s.workspace_id=NEW.workspace_id AND s.key=NEW.status AND s.category IN ('done','closed'));
 IF NOT closed THEN
  NEW.completed_at := NULL;
 ELSIF TG_OP='INSERT' OR OLD.status IS DISTINCT FROM NEW.status OR NEW.completed_at IS NULL THEN
  NEW.completed_at := now();
 END IF;
 RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_issue_completed_at ON issue;
CREATE TRIGGER trg_issue_completed_at BEFORE INSERT OR UPDATE OF status ON issue
FOR EACH ROW EXECUTE FUNCTION stamp_issue_completed_at();
