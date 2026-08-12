-- Forward-only T016 retirement fence. Schema restore requires a pre-299 dump.
DO $$
BEGIN
  RAISE EXCEPTION 'refuse rollback across T016 dead-authority retirement fence 299; restore a pre-299 database dump';
END $$;
