package cli

import (
	"errors"
	"net/http"
	"testing"
)

func TestWorkCoordinationV1RouteClassifierMatrix(t *testing.T) {
	cases := []struct {
		name, method, path, code string
		status                   int
		exit                     int
	}{
		{"scope create conflict", http.MethodPost, "/api/coordination/scopes", "coordination_idempotency_conflict", http.StatusConflict, ExitConflict},
		{"scope get not found", http.MethodGet, "/api/coordination/scopes/00000000-0000-0000-0000-000000000001", "coordination_not_found", http.StatusNotFound, ExitNotFound},
		{"scope by root forbidden", http.MethodGet, "/api/coordination/scopes/by-root?root_issue_id=x", "coordination_forbidden", http.StatusForbidden, ExitAuth},
		{"issue delete blocked", http.MethodDelete, "/api/issues/00000000-0000-0000-0000-000000000001", "coordination_delete_blocked", http.StatusConflict, ExitConflict},
		{"batch delete blocked", http.MethodPost, "/api/issues/batch-delete", "coordination_delete_blocked", http.StatusConflict, ExitConflict},
		{"workspace delete blocked", http.MethodDelete, "/api/workspaces/00000000-0000-0000-0000-000000000001", "coordination_delete_blocked", http.StatusConflict, ExitConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := coordinationHTTPError(tc.method, tc.path, tc.status, tc.code)
			err := CoordinationProductError(raw)
			var product *ProductError
			if !errors.As(err, &product) || product.Code != tc.code {
				t.Fatalf("expected ProductError, got %T %v", err, err)
			}
			if got := ExitCodeFor(err); got != tc.exit {
				t.Fatalf("exit=%d want=%d", got, tc.exit)
			}
		})
	}
}

func TestWorkCoordinationV1FutureConflictCodesStayLegacy(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/coordination/scopes"},
		{http.MethodGet, "/api/coordination/scopes/by-root"},
		{http.MethodGet, "/api/coordination/scopes/00000000-0000-0000-0000-000000000001"},
		{http.MethodDelete, "/api/issues/00000000-0000-0000-0000-000000000001"},
		{http.MethodPost, "/api/issues/batch-delete"},
		{http.MethodDelete, "/api/workspaces/00000000-0000-0000-0000-000000000001"},
	}
	for _, route := range routes {
		for _, code := range []string{
			"coordination_capacity_exceeded",
			"coordination_revision_conflict",
			"coordination_dependency_scope_conflict",
		} {
			name := route.method + " " + route.path + " " + code
			t.Run(name, func(t *testing.T) {
				raw := coordinationHTTPError(route.method, route.path, http.StatusConflict, code)
				if got := CoordinationProductError(raw); got != raw {
					t.Fatalf("future code upgraded: %T %v", got, got)
				}
				if got := ExitCodeFor(raw); got != ExitGeneric {
					t.Fatalf("legacy exit=%d", got)
				}
			})
		}
	}
	for _, raw := range []*HTTPError{
		coordinationHTTPError(http.MethodGet, "/api/coordination/scopes/00000000-0000-0000-0000-000000000001", http.StatusConflict, "coordination_delete_blocked"),
		coordinationHTTPError(http.MethodDelete, "/api/issues/00000000-0000-0000-0000-000000000001", http.StatusConflict, "coordination_idempotency_conflict"),
	} {
		if got := CoordinationProductError(raw); got != raw {
			t.Fatalf("wrong route/code combination upgraded: %T %v", got, got)
		}
	}
}

func TestWorkCoordinationKnownConflictProductErrorsMapExitSix(t *testing.T) {
	for _, code := range []string{
		"coordination_capacity_exceeded",
		"coordination_revision_conflict",
		"coordination_idempotency_conflict",
		"coordination_dependency_scope_conflict",
		"coordination_delete_blocked",
	} {
		err := &ProductError{StatusCode: http.StatusConflict, Code: code, Message: "safe"}
		if got := ExitCodeFor(err); got != ExitConflict {
			t.Fatalf("%s exit=%d", code, got)
		}
	}
}

func TestWorkCoordinationMalformedOrMismatchedEnvelopeStaysLegacy(t *testing.T) {
	cases := []*HTTPError{
		{Method: http.MethodPost, Path: "/api/coordination/scopes", StatusCode: http.StatusConflict, ContentType: "text/plain", Body: "conflict"},
		coordinationHTTPError(http.MethodPost, "/api/coordination/scopes", http.StatusConflict, "unknown"),
		coordinationHTTPError(http.MethodPost, "/api/coordination/scopes", http.StatusConflict, "coordination_invalid_payload"),
		{Method: http.MethodPost, Path: "/api/coordination/scopes", StatusCode: http.StatusConflict, ContentType: "application/json", Body: `{"error":{"code":"coordination_idempotency_conflict","code":"coordination_revision_conflict","message":"safe"}}`},
		{Method: http.MethodPost, Path: "/api/coordination/scopes", StatusCode: http.StatusConflict, ContentType: "application/json", Body: `{"error":{"code":"coordination_idempotency_conflict","message":"safe","details":{"sql":"no"}}}`},
		{Method: http.MethodPost, Path: "/api/coordination/scopes", StatusCode: http.StatusConflict, ContentType: "application/json", Body: `{"error":{"code":"coordination_idempotency_conflict","message":"safe","unknown":true}}`},
		{Method: http.MethodPost, Path: "/api/coordination/scopes", StatusCode: http.StatusConflict, ContentType: "application/json", Body: `{"error":{"code":"coordination_idempotency_conflict","message":" padded "}}`},
		{Method: http.MethodPost, Path: "/api/coordination/scopes", StatusCode: http.StatusConflict, ContentType: "application/json", Body: `{"error":{"code":"coordination_idempotency_conflict","message":"safe"},"unknown":true}`},
		{Method: http.MethodPost, Path: "/api/coordination/scopes", StatusCode: http.StatusConflict, ContentType: "application/json", Body: `{"error":{"code":"coordination_idempotency_conflict","message":"safe"}} {}`},
		coordinationHTTPError(http.MethodPost, "/api/issues", http.StatusConflict, "coordination_delete_blocked"),
	}
	for _, raw := range cases {
		err := CoordinationProductError(raw)
		if err != raw {
			t.Fatalf("mismatch must preserve original HTTP error: got %T %v", err, err)
		}
		var product *ProductError
		if errors.As(err, &product) {
			t.Fatalf("unexpected ProductError for %+v", raw)
		}
		if got := ExitCodeFor(err); got != ExitGeneric {
			t.Fatalf("legacy exit=%d", got)
		}
	}
}

func coordinationHTTPError(method, path string, status int, code string) *HTTPError {
	return &HTTPError{
		Method:      method,
		Path:        path,
		StatusCode:  status,
		ContentType: "application/json; charset=utf-8",
		Body:        `{"error":{"code":"` + code + `","message":"safe"}}`,
	}
}
