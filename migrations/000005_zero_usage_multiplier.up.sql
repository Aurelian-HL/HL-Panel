-- Zero group multipliers retain actual traffic while charging no quota.
ALTER TABLE usage_events DROP CONSTRAINT usage_event_entry_multiplier_range;
ALTER TABLE usage_events DROP CONSTRAINT usage_event_exit_multiplier_range;
ALTER TABLE usage_events ADD CONSTRAINT usage_event_entry_multiplier_range CHECK (entry_multiplier_micros BETWEEN 0 AND 1000000000);
ALTER TABLE usage_events ADD CONSTRAINT usage_event_exit_multiplier_range CHECK (exit_multiplier_micros BETWEEN 0 AND 1000000000);
