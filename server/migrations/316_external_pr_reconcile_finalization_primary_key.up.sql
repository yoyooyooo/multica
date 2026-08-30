-- Attach the exact id authority from migration 297. The conditional block is
-- required because PostgreSQL renames the attached index to *_pkey before the
-- migration ledger row is written; a crash between those two writes must be
-- recoverable without accepting a wrong relation or silently creating one.
DO $$
DECLARE
    table_oid oid := to_regclass('external_pr_reconcile_finalization');
    source_index_oid oid := to_regclass('external_pr_reconcile_finalization_id_uidx');
    target_constraint_oid oid;
    target_index_oid oid;
    target_index_name text;
    primary_key_count integer;
    source_constraint_count integer;
    source_exact boolean;
    target_exact boolean;
BEGIN
    IF table_oid IS NULL THEN
        RAISE EXCEPTION 'external_pr_reconcile_finalization table is missing; refuse primary-key recovery';
    END IF;

    SELECT count(*)::integer
      INTO primary_key_count
      FROM pg_constraint
     WHERE conrelid = table_oid AND contype = 'p';

    SELECT c.oid, c.conindid, idx.relname
      INTO target_constraint_oid, target_index_oid, target_index_name
      FROM pg_constraint c
      JOIN pg_class idx ON idx.oid = c.conindid
     WHERE c.conrelid = table_oid
       AND c.contype = 'p'
       AND c.conname = 'external_pr_reconcile_finalization_pkey';

    IF primary_key_count > 1 OR (primary_key_count = 1 AND target_constraint_oid IS NULL) THEN
        RAISE EXCEPTION 'external_pr_reconcile_finalization has an unexpected primary-key authority; refuse recovery';
    END IF;

    IF target_constraint_oid IS NOT NULL THEN
        SELECT EXISTS (
            SELECT 1
              FROM pg_constraint c
              JOIN pg_class idx ON idx.oid = c.conindid
              JOIN pg_index i ON i.indexrelid = idx.oid
              JOIN pg_am am ON am.oid = idx.relam
              JOIN pg_attribute a ON a.attrelid = i.indrelid
                                 AND a.attnum = (i.indkey::smallint[])[0]
              JOIN pg_opclass opc ON opc.oid = i.indclass[0]
              JOIN pg_namespace opn ON opn.oid = opc.opcnamespace
             WHERE c.oid = target_constraint_oid
               AND c.convalidated
               AND NOT c.condeferrable
               AND NOT c.condeferred
               AND i.indrelid = table_oid
               AND idx.relname = 'external_pr_reconcile_finalization_pkey'
               AND idx.relkind = 'i'
               AND am.amname = 'btree'
               AND i.indisunique AND i.indisready AND i.indisvalid
               AND i.indnkeyatts = 1 AND i.indnatts = 1
               AND i.indpred IS NULL AND i.indexprs IS NULL
               AND (i.indkey::smallint[])[0] = a.attnum
               AND a.attname = 'id' AND a.atttypid = 'uuid'::regtype
               AND i.indcollation[0] = 0::oid
               AND i.indoption[0] = 0::smallint
               AND opn.nspname = 'pg_catalog' AND opc.opcname = 'uuid_ops'
        ) INTO target_exact;
        IF NOT target_exact OR target_index_oid IS NULL THEN
            RAISE EXCEPTION 'external_pr_reconcile_finalization primary-key authority is not exact; refuse recovery';
        END IF;
        IF source_index_oid IS NOT NULL THEN
            RAISE EXCEPTION 'external_pr_reconcile_finalization has both attached and source index authorities; refuse recovery';
        END IF;
        RETURN;
    END IF;

    IF source_index_oid IS NULL THEN
        RAISE EXCEPTION 'external_pr_reconcile_finalization id authority is missing; refuse primary-key attach';
    END IF;

    SELECT count(*)::integer
      INTO source_constraint_count
      FROM pg_constraint
     WHERE conindid = source_index_oid;
    IF source_constraint_count <> 0 THEN
        RAISE EXCEPTION 'external_pr_reconcile_finalization source index is already constraint-owned; refuse recovery';
    END IF;

    SELECT EXISTS (
        SELECT 1
          FROM pg_class idx
          JOIN pg_index i ON i.indexrelid = idx.oid
          JOIN pg_am am ON am.oid = idx.relam
          JOIN pg_attribute a ON a.attrelid = i.indrelid
                             AND a.attnum = (i.indkey::smallint[])[0]
          JOIN pg_opclass opc ON opc.oid = i.indclass[0]
          JOIN pg_namespace opn ON opn.oid = opc.opcnamespace
         WHERE idx.oid = source_index_oid
           AND i.indrelid = table_oid
           AND idx.relkind = 'i'
           AND am.amname = 'btree'
           AND i.indisunique AND i.indisready AND i.indisvalid
           AND i.indnkeyatts = 1 AND i.indnatts = 1
           AND i.indpred IS NULL AND i.indexprs IS NULL
           AND (i.indkey::smallint[])[0] = a.attnum
           AND a.attname = 'id' AND a.atttypid = 'uuid'::regtype
           AND i.indcollation[0] = 0::oid
           AND i.indoption[0] = 0::smallint
           AND opn.nspname = 'pg_catalog' AND opc.opcname = 'uuid_ops'
    ) INTO source_exact;
    IF NOT source_exact THEN
        RAISE EXCEPTION 'external_pr_reconcile_finalization source id index is not exact; refuse primary-key attach';
    END IF;

    ALTER TABLE external_pr_reconcile_finalization
        ADD CONSTRAINT external_pr_reconcile_finalization_pkey
        PRIMARY KEY USING INDEX external_pr_reconcile_finalization_id_uidx;
END
$$;
