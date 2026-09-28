-- Daily tasks from /daily-task share the scheduled_tasks table. daily_minute
-- is the local time of day (minutes after midnight) the task repeats at; it is
-- NULL for a one-off /schedule-task. After a daily task runs, its row moves to
-- the next day instead of being deleted.
ALTER TABLE scheduled_tasks ADD COLUMN daily_minute INTEGER CHECK (daily_minute BETWEEN 0 AND 1439);
