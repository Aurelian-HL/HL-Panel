ALTER TABLE device_group_members
    DROP CONSTRAINT device_group_members_weight_range,
    ADD CONSTRAINT device_group_members_weight_range CHECK (weight BETWEEN 0 AND 1000);

ALTER TABLE endpoint_pool_members
    DROP CONSTRAINT endpoint_pool_members_weight_range,
    ADD CONSTRAINT endpoint_pool_members_weight_range CHECK (weight BETWEEN 0 AND 1000);
