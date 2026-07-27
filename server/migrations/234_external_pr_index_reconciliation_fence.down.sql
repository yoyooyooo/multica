DO $$
BEGIN
    RAISE EXCEPTION 'migration 234 is a forward-only reconciliation fence; restore from an accepted pre-234 backup instead of rolling back across it';
END
$$;
