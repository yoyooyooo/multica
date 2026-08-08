package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTaskToResponseCarriesCanonicalExecutionID(t *testing.T) {
	taskID := "11111111-1111-4111-8111-111111111111"
	executionID := "22222222-2222-4222-8222-222222222222"
	base := db.AgentTaskQueue{ID: parseUUID(taskID)}
	if got := taskToResponse(base, testWorkspaceID).ExecutionID; got != taskID {
		t.Fatalf("fallback execution_id=%q, want task id %q", got, taskID)
	}
	base.ExecutionID = pgtype.UUID{Bytes: parseUUID(executionID).Bytes, Valid: true}
	if got := taskToResponse(base, testWorkspaceID).ExecutionID; got != executionID {
		t.Fatalf("execution_id=%q, want %q", got, executionID)
	}
}
