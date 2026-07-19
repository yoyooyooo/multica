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
	var rootID, downstreamID, upstreamID string
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,$2,'member',$3,'none',990501) RETURNING id`, testWorkspaceID, fmt.Sprintf("WCS router %d", time.Now().UnixNano()), testUserID).Scan(&rootID); err != nil {
		t.Fatalf("insert router root: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id) VALUES ($1,$2,'member',$3,'none',990502,$4) RETURNING id`, testWorkspaceID, fmt.Sprintf("WCS router downstream %d", time.Now().UnixNano()), testUserID, rootID).Scan(&downstreamID); err != nil {
		t.Fatalf("insert router downstream: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id) VALUES ($1,$2,'member',$3,'none',990503,$4) RETURNING id`, testWorkspaceID, fmt.Sprintf("WCS router upstream %d", time.Now().UnixNano()), testUserID, rootID).Scan(&upstreamID); err != nil {
		t.Fatalf("insert router upstream: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_dependency WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_receipt WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_scope WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=ANY($1::uuid[])`, []string{downstreamID, upstreamID, rootID})
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

	dependencyBody, _ := json.Marshal(map[string]any{"expected_revision": 0, "downstream_issue_id": downstreamID, "upstream_issue_id": upstreamID})
	dependencyReq, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/coordination/scopes/"+created.Scope.ID+"/dependencies", bytes.NewReader(dependencyBody))
	if err != nil {
		t.Fatalf("create dependency request: %v", err)
	}
	dependencyReq.Header.Set("Authorization", "Bearer "+testToken)
	dependencyReq.Header.Set("X-Workspace-ID", testWorkspaceID)
	dependencyReq.Header.Set("Content-Type", "application/json")
	dependencyReq.Header.Set("Idempotency-Key", "router-dependency-add")
	resp, err = http.DefaultClient.Do(dependencyReq)
	if err != nil {
		t.Fatalf("dependency request: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		defer resp.Body.Close()
		t.Fatalf("dependency status=%d", resp.StatusCode)
	}
	var dependencyCreated struct {
		Dependency struct {
			ID string `json:"id"`
		} `json:"dependency"`
	}
	readJSON(t, resp, &dependencyCreated)
	if dependencyCreated.Dependency.ID == "" {
		t.Fatal("dependency route returned no dependency id")
	}

	resp = authRequest(t, http.MethodGet, "/api/coordination/scopes/"+created.Scope.ID+"/dependencies?limit=100", nil)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("dependency list status=%d", resp.StatusCode)
	}
	var dependencyPage struct {
		Dependencies []map[string]any `json:"dependencies"`
	}
	readJSON(t, resp, &dependencyPage)
	if len(dependencyPage.Dependencies) != 1 {
		t.Fatalf("dependency list count=%d", len(dependencyPage.Dependencies))
	}

	resolveBody, _ := json.Marshal(map[string]any{"expected_revision": 1})
	resolveReq, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/coordination/scopes/"+created.Scope.ID+"/dependencies/"+dependencyCreated.Dependency.ID+"/resolve", bytes.NewReader(resolveBody))
	if err != nil {
		t.Fatalf("create resolve request: %v", err)
	}
	resolveReq.Header.Set("Authorization", "Bearer "+testToken)
	resolveReq.Header.Set("X-Workspace-ID", testWorkspaceID)
	resolveReq.Header.Set("Content-Type", "application/json")
	resolveReq.Header.Set("Idempotency-Key", "router-dependency-resolve")
	resp, err = http.DefaultClient.Do(resolveReq)
	if err != nil {
		t.Fatalf("resolve request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("resolve status=%d", resp.StatusCode)
	}
	var dependencyResolved map[string]any
	readJSON(t, resp, &dependencyResolved)

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
