DROP TRIGGER IF EXISTS trg_issue_completed_at ON issue;
DROP FUNCTION IF EXISTS stamp_issue_completed_at();
ALTER TABLE issue DROP COLUMN IF EXISTS completed_at;
