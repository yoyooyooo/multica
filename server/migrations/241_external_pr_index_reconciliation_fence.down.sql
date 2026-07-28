DO $$
BEGIN
    RAISE EXCEPTION 'migration 241 is a forward-only reconciliation fence; restore from an accepted pre-241 backup instead of rolling back across it';
END
$$;
