package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkCoordinationBatchDeletePreservesRecoverablePartialSuccess(t *testing.T) {
	ctx := context.Background()
	workspaceID := mustHandlerUUID(t, testWorkspaceID)
	userID := mustHandlerUUID(t, testUserID)
	var deletableID, restrictedID pgtype.UUID
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS batch deletable','member',$2,'none',991101) RETURNING id`, workspaceID, userID).Scan(&deletableID); err != nil {
		t.Fatalf("insert deletable issue: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS batch restricted','member',$2,'none',991102) RETURNING id`, workspaceID, userID).Scan(&restrictedID); err != nil {
		t.Fatalf("insert restricted issue: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO attachment (id,workspace_id,issue_id,uploader_type,uploader_id,filename,url,content_type,size_bytes) VALUES
		(gen_random_uuid(),$1,$2,'member',$4,'deleted.txt','https://storage.test/wcs/deleted.txt','text/plain',1),
		(gen_random_uuid(),$1,$3,'member',$4,'restricted.txt','https://storage.test/wcs/restricted.txt','text/plain',1)`, workspaceID, deletableID, restrictedID, userID); err != nil {
		t.Fatalf("insert attachments: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=ANY($1::uuid[])`, []pgtype.UUID{deletableID, restrictedID})
	})

	fn := fmt.Sprintf("wcs_handler_restrict_%d", time.Now().UnixNano())
	trigger := fn + "_trigger"
	quotedFn := pgx.Identifier{fn}.Sanitize()
	quotedTrigger := pgx.Identifier{trigger}.Sanitize()
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'restricted' USING ERRCODE='23503'; END $$`, quotedFn)); err != nil {
		t.Fatalf("create trigger function: %v", err)
	}
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE DELETE ON issue FOR EACH ROW WHEN (OLD.id = '%s'::uuid) EXECUTE FUNCTION %s()`, quotedTrigger, uuidToString(restrictedID), quotedFn)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON issue`, quotedTrigger))
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, quotedFn))
	})

	bus := events.New()
	deletedEvents := 0
	bus.Subscribe(protocol.EventIssueDeleted, func(events.Event) { deletedEvents++ })
	var deletedStorageKeys []string
	h := *testHandler
	h.Bus = bus
	h.Storage = &coordinationEffectStorage{onDeleteKeys: func(keys []string) { deletedStorageKeys = append(deletedStorageKeys, keys...) }}
	req := newRequest(http.MethodPost, "/api/issues/batch-delete", map[string]any{
		"issue_ids": []string{uuidToString(restrictedID), uuidToString(deletableID), uuidToString(deletableID), "not-a-uuid"},
	})
	w := httptest.NewRecorder()
	h.BatchDeleteIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Deleted int `json:"deleted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Deleted != 1 {
		t.Fatalf("response=%+v err=%v body=%s", response, err, w.Body.String())
	}
	var deletableExists, restrictedExists bool
	if err := testPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE id=$1), EXISTS(SELECT 1 FROM issue WHERE id=$2)`, deletableID, restrictedID).Scan(&deletableExists, &restrictedExists); err != nil {
		t.Fatalf("check issues: %v", err)
	}
	if deletableExists || !restrictedExists || deletedEvents != 1 || len(deletedStorageKeys) != 1 || deletedStorageKeys[0] != "wcs/deleted.txt" {
		t.Fatalf("deletable=%v restricted=%v events=%d storage=%v", deletableExists, restrictedExists, deletedEvents, deletedStorageKeys)
	}
}
