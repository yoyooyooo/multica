-- T016: retire dead authority/delegation indexes. Single CONCURRENTLY statement.
DROP INDEX CONCURRENTLY IF EXISTS workspace_workload_authority_workspace_id_uidx;
