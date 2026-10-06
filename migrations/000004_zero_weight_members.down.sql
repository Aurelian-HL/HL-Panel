DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM device_group_members WHERE weight = 0)
        OR EXISTS (SELECT 1 FROM endpoint_pool_members WHERE weight = 0) THEN
        RAISE EXCEPTION 'cannot roll back zero-weight support while paused members exist';
    END IF;
END
$$;

ALTER TABLE device_group_members
    DROP CONSTRAINT device_group_members_weight_range,
    ADD CONSTRAINT device_group_members_weight_range CHECK (weight BETWEEN 1 AND 1000);

ALTER TABLE endpoint_pool_members
    DROP CONSTRAINT endpoint_pool_members_weight_range,
    ADD CONSTRAINT endpoint_pool_members_weight_range CHECK (weight BETWEEN 1 AND 1000);
