-- Retain raw observations for diagnosis but stop presenting unknown/degraded
-- lengths as active. No account credentials, config or attempt counts change.
UPDATE codex_turn_states
SET status = CASE WHEN length(state) IN (312,356) THEN 'degraded' ELSE 'rejected' END
WHERE status = 'active' AND length(state) NOT IN (292,332);
