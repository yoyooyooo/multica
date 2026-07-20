package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestWorkCoordinationRoutesThroughRouter(t *testing.T) {
	ctx := context.Background()
	var rootID string
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,$2,'member',$3,'none',990501) RETURNING id`, testWorkspaceID, fmt.Sprintf("WCS router %d", time.Now().UnixNano()), testUserID).Scan(&rootID); err != nil {
		t.Fatalf("insert router root: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_receipt WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_scope WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, rootID)
	})

	body, _ := json.Marshal(map[string]string{"root_issue_id": rootID, "workflow_profile_key": "matt-loop"})
	req, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/coordination/scopes", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create ensure request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "router-ensure")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ensure request: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		defer resp.Body.Close()
		t.Fatalf("ensure status=%d", resp.StatusCode)
	}
	var created struct {
		Scope struct {
			ID string `json:"id"`
		} `json:"scope"`
	}
	readJSON(t, resp, &created)
	if created.Scope.ID == "" {
		t.Fatal("ensure route returned no scope id")
	}

	resp = authRequest(t, http.MethodGet, "/api/coordination/scopes/"+created.Scope.ID, nil)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("get-by-id status=%d", resp.StatusCode)
	}
	var byID map[string]any
	readJSON(t, resp, &byID)

	query := url.Values{"root_issue_id": []string{rootID}, "workflow_profile_key": []string{"matt-loop"}}
	resp = authRequest(t, http.MethodGet, "/api/coordination/scopes/by-root?"+query.Encode(), nil)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("get-by-root status=%d", resp.StatusCode)
	}
	var byRoot map[string]any
	readJSON(t, resp, &byRoot)
}
