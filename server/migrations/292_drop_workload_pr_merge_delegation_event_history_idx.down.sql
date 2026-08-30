-- Reverse of T016 index drop is intentionally incomplete: recreating exact
-- historical predicates belongs to the original 272-291 migrations if a full
-- generation rollback restore is used. No-op keeps migrate down from failing
-- closed on this retirement band.
SELECT 1;
