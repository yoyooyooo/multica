-- T016: retire dead authority/delegation indexes. Single CONCURRENTLY statement.
DROP INDEX CONCURRENTLY IF EXISTS workload_pr_merge_delegation_current_execution_uidx;
