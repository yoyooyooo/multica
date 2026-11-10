package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestExternalPRCompletionPreservesCancelledSiblingWarning(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-test-token")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "test-instance")
	t.Setenv("MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS", "ags")

	for i, staged := range []bool{false, true} {
		t.Run(fmt.Sprintf("staged=%v", staged), func(t *testing.T) {
			parent := dbfx.Issue(t, "External parent with cancelled dependency")
			cols := testutil.Cols{"parent_issue_id": parent}
			if staged {
				cols["stage"] = 1
			}
			child := dbfx.Issue(t, "Merged external child", cols)
			cols["status"] = "cancelled"
			dbfx.Issue(t, "Cancelled sibling", cols)
			request := externalPRTestRequest(t, child, int32(700100+i))
			response := callExternalPR(t, testHandler.CompleteIssueFromExternalPR, request)
			if response.Code != http.StatusOK {
				t.Fatalf("callback=%d %s", response.Code, response.Body.String())
			}
			work := claimExternalPRTestWork(t, child)
			if err := testHandler.processExternalPRWork(context.Background(), work); err != nil {
				t.Fatal(err)
			}
			var content string
			dbfx.QueryRow(t, `SELECT content FROM comment WHERE id=$1`, work.ID).Scan(&content)
			if !strings.Contains(content, "cancelled") || !strings.Contains(content, "confirm") {
				t.Fatalf("external completion lost upstream cancellation warning: %s", content)
			}
		})
	}
}

func TestExternalPRCompletionFailsClosedOnUnresolvedStatus(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-test-token")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "test-instance")
	t.Setenv("MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS", "ags")

	parent := dbfx.Issue(t, "External unresolved-status parent")
	child := dbfx.Issue(t, "External unresolved-status child", testutil.Cols{
		"parent_issue_id": parent, "status": "missing_fork_status",
	})
	request := externalPRTestRequest(t, child, 700110)
	response := callExternalPR(t, testHandler.CompleteIssueFromExternalPR, request)
	if response.Code != http.StatusOK {
		t.Fatalf("callback=%d %s", response.Code, response.Body.String())
	}
	work := claimExternalPRTestWork(t, child)
	if err := testHandler.processExternalPRWork(context.Background(), work); err == nil {
		t.Fatal("unresolved custom status must not authorize completion")
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id=$1`, child).Scan(&status)
	if status != "missing_fork_status" {
		t.Fatalf("unresolved status changed to %q", status)
	}
}
