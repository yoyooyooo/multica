DO $$
BEGIN
    RAISE EXCEPTION 'migration 275 is a forward-only reconciliation fence; restore from an accepted pre-275 backup instead of rolling back across it';
END
$$;
