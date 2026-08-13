package main

import (
	"os"
	"strings"
	"testing"
)

func TestExternalPRReconcileIndexesHaveIndependentConcurrentMigrationsAndHooks(t *testing.T) {
	versions := []string{
		"303_external_pr_reconcile_work_identity_index",
		"304_external_pr_reconcile_work_claim_index",
		"305_external_pr_reconcile_work_issue_index",
	}
	if len(externalPRReconcileIndexSpecs) != len(versions) {
		t.Fatalf("external PR reconcile specs=%d want=%d", len(externalPRReconcileIndexSpecs), len(versions))
	}
	finalizationVersions := []string{
		"307_external_pr_reconcile_finalization_work_index",
		"308_comment_external_pr_finalization_index",
	}
	if len(externalPRFinalizationIndexSpecs) != len(finalizationVersions) {
		t.Fatalf("external PR finalization specs=%d want=%d", len(externalPRFinalizationIndexSpecs), len(finalizationVersions))
	}
	for i, version := range versions {
		if preMigrationHooks[version] == nil {
			t.Fatalf("missing invalid-index reconciliation hook for %s", version)
		}
		spec := externalPRReconcileIndexSpecs[i]
		if spec.Name == "" || spec.Table != "external_pr_reconcile_work" || len(spec.Columns) == 0 {
			t.Fatalf("incomplete index spec for %s: %#v", version, spec)
		}
		if spec.Unique != (i == 0) {
			t.Fatalf("index %s unique=%v, want %v", version, spec.Unique, i == 0)
		}
	}
	for i, version := range finalizationVersions {
		if preMigrationHooks[version] == nil {
			t.Fatalf("missing invalid-index reconciliation hook for %s", version)
		}
		spec := externalPRFinalizationIndexSpecs[i]
		if spec.Name == "" || len(spec.Columns) == 0 || !spec.Unique || spec.PredicateSQL == "" {
			t.Fatalf("incomplete finalization index spec for %s: %#v", version, spec)
		}
	}
	if len(externalPRInboxIndexSpecs) != 1 || preMigrationHooks["312_inbox_item_delivery_key_index"] == nil || postFenceReconciliationHooks["312_inbox_item_delivery_key_index"] == nil {
		t.Fatalf("missing migration 312 exact-definition recovery hooks/spec: %#v", externalPRInboxIndexSpecs)
	}
	inboxSpec := externalPRInboxIndexSpecs[0]
	if inboxSpec.Name != "inbox_item_delivery_key_uidx" || inboxSpec.Table != "inbox_item" || !inboxSpec.Unique || len(inboxSpec.Columns) != 4 || inboxSpec.PredicateSQL != "delivery_key IS NOT NULL" {
		t.Fatalf("incomplete inbox delivery-key index spec: %#v", inboxSpec)
	}
	primaryKeyVersions := []string{
		"313_external_pr_reconcile_work_id_index",
		"314_external_pr_reconcile_finalization_id_index",
	}
	if len(externalPRPrimaryKeyIndexSpecs) != len(primaryKeyVersions) {
		t.Fatalf("external PR primary-key specs=%d want=%d", len(externalPRPrimaryKeyIndexSpecs), len(primaryKeyVersions))
	}
	for i, version := range primaryKeyVersions {
		if preMigrationHooks[version] == nil {
			t.Fatalf("missing primary-key index reconciliation hook for %s", version)
		}
		spec := externalPRPrimaryKeyIndexSpecs[i]
		if spec.Name == "" || !spec.Unique || len(spec.Columns) != 1 || spec.Columns[0] != "id" {
			t.Fatalf("incomplete primary-key index spec for %s: %#v", version, spec)
		}
	}
}

func TestExternalPRFinalizationStateMatchesRetryQueries(t *testing.T) {
	migration, err := os.ReadFile("../../migrations/306_external_pr_reconcile_finalization.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	const expectedCheck = "CHECK (state IN ('pending', 'retry_wait', 'succeeded', 'recorded', 'dead'))"
	if !strings.Contains(string(migration), expectedCheck) {
		t.Fatalf("finalization migration must allow retry_wait state: %q", expectedCheck)
	}
	query, err := os.ReadFile("../../pkg/db/queries/external_pr_finalization.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(query), "state IN ('pending', 'retry_wait')") || !strings.Contains(string(query), "ELSE 'retry_wait'") {
		t.Fatal("finalization queries must claim and persist retry_wait")
	}
}

func TestExternalPRFinalizationRetryStateRepairMigrationIsReversible(t *testing.T) {
	up, err := os.ReadFile("../../migrations/310_external_pr_reconcile_finalization_retry_state.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/310_external_pr_reconcile_finalization_retry_state.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"up": up, "down": down} {
		text := string(content)
		upper := strings.ToUpper(text)
		if !strings.Contains(text, "external_pr_reconcile_finalization_state_check") || !strings.Contains(text, "DROP CONSTRAINT") || !strings.Contains(text, "ADD CONSTRAINT") {
			t.Fatalf("%s repair migration must replace the named state constraint", name)
		}
		if !strings.Contains(upper, "BEGIN;") || !strings.Contains(upper, "COMMIT;") {
			t.Fatalf("%s repair migration must be transactionally atomic", name)
		}
		if strings.Contains(text, "pg_constraint") || strings.Contains(text, "conrelid") || strings.Contains(text, "contype") {
			t.Fatalf("%s repair migration must not scan or remove unrelated CHECK constraints", name)
		}
		if strings.Contains(strings.ToUpper(text), "FOREIGN KEY") || strings.Contains(strings.ToUpper(text), "CASCADE") {
			t.Fatalf("%s repair migration must not add foreign keys or cascades", name)
		}
	}
	if !strings.Contains(string(up), "'retry_wait'") || !strings.Contains(string(down), "WHERE state = 'retry_wait'") {
		t.Fatal("repair migration up/down must cover retry_wait explicitly")
	}
}

func TestExternalPRInboxDeliveryKeyMigrationIsScopedAndReversible(t *testing.T) {
	up, err := os.ReadFile("../../migrations/311_inbox_item_delivery_key.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/311_inbox_item_delivery_key.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	indexUp, err := os.ReadFile("../../migrations/312_inbox_item_delivery_key_index.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	indexDown, err := os.ReadFile("../../migrations/312_inbox_item_delivery_key_index.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(up), "ALTER TABLE inbox_item") || !strings.Contains(string(up), "delivery_key") || !strings.Contains(string(down), "DROP COLUMN IF EXISTS delivery_key") {
		t.Fatal("inbox delivery key migrations must add and remove only the scoped column")
	}
	if strings.Contains(strings.ToUpper(string(up)+string(down)+string(indexUp)+string(indexDown)), "FOREIGN KEY") || strings.Contains(strings.ToUpper(string(up)+string(down)+string(indexUp)+string(indexDown)), "CASCADE") {
		t.Fatal("inbox delivery key migrations must not add foreign keys or cascades")
	}
	upper := strings.ToUpper(string(indexUp))
	if strings.Count(upper, "CREATE ") != 1 || !strings.Contains(upper, "CREATE UNIQUE INDEX CONCURRENTLY") || !strings.Contains(string(indexDown), "DROP INDEX CONCURRENTLY") {
		t.Fatal("inbox delivery key index must be one independent concurrent index with a paired down")
	}
}

func TestExternalPRPrimaryKeyMigrationsUseIndependentIndexesAndConstraints(t *testing.T) {
	for _, file := range []string{
		"../../migrations/313_external_pr_reconcile_work_id_index.up.sql",
		"../../migrations/314_external_pr_reconcile_finalization_id_index.up.sql",
	} {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		upper := strings.ToUpper(string(content))
		if strings.Count(upper, "CREATE ") != 1 || !strings.Contains(upper, "CREATE UNIQUE INDEX CONCURRENTLY") {
			t.Fatalf("%s must contain exactly one independent concurrent unique index", file)
		}
	}
	for _, file := range []string{
		"../../migrations/315_external_pr_reconcile_work_primary_key.up.sql",
		"../../migrations/316_external_pr_reconcile_finalization_primary_key.up.sql",
	} {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		upper := strings.ToUpper(string(content))
		if strings.Contains(upper, "CREATE INDEX") || !strings.Contains(upper, "ADD CONSTRAINT") || !strings.Contains(upper, "PRIMARY KEY USING INDEX") {
			t.Fatalf("%s must attach an existing index without creating another index", file)
		}
	}
	for _, file := range []string{
		"../../migrations/302_external_pr_reconcile_work.up.sql",
		"../../migrations/306_external_pr_reconcile_finalization.up.sql",
	} {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		upper := strings.ToUpper(string(content))
		if strings.Contains(upper, "PRIMARY KEY") {
			t.Fatalf("%s must not create an inline primary-key index", file)
		}
	}
	for _, file := range []string{
		"../../migrations/313_external_pr_reconcile_work_id_index.down.sql",
		"../../migrations/314_external_pr_reconcile_finalization_id_index.down.sql",
		"../../migrations/315_external_pr_reconcile_work_primary_key.down.sql",
		"../../migrations/316_external_pr_reconcile_finalization_primary_key.down.sql",
	} {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		upper := strings.ToUpper(string(content))
		if !strings.Contains(upper, "DROP") {
			t.Fatalf("%s must contain inverse cleanup", file)
		}
	}
}

func TestExternalPRReconcileMigrationSQLUsesSingleConcurrentIndexStatement(t *testing.T) {
	for _, file := range []string{
		"../../migrations/303_external_pr_reconcile_work_identity_index.up.sql",
		"../../migrations/304_external_pr_reconcile_work_claim_index.up.sql",
		"../../migrations/305_external_pr_reconcile_work_issue_index.up.sql",
		"../../migrations/307_external_pr_reconcile_finalization_work_index.up.sql",
		"../../migrations/308_comment_external_pr_finalization_index.up.sql",
		"../../migrations/312_inbox_item_delivery_key_index.up.sql",
		"../../migrations/313_external_pr_reconcile_work_id_index.up.sql",
		"../../migrations/314_external_pr_reconcile_finalization_id_index.up.sql",
	} {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		upper := strings.ToUpper(string(content))
		if strings.Count(upper, "CREATE ") != 1 || !strings.Contains(upper, "CONCURRENTLY") {
			t.Fatalf("%s must contain exactly one concurrent CREATE INDEX statement", file)
		}
	}
}
