DO $$
BEGIN
  RAISE EXCEPTION 'refuse rollback across T017 external-pr-core schema fence 301; restore a pre-301 database dump';
END $$;
