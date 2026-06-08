package handler

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/storage"
)

// TestIsUploadDenied is a pure-function check on the denylist used by
// UploadFile. No DB / handler fixture required — runs in any environment.
func TestIsUploadDenied(t *testing.T) {
	cases := []struct {
		name        string
		filename    string
		contentType string
		want        bool
	}{
		// Allowed shapes — these are the everyday legitimate uploads.
		{"png is allowed", "logo.png", "image/png", false},
		{"pdf is allowed", "report.pdf", "application/pdf", false},
		{"plain text is allowed", "notes.txt", "text/plain", false},
		// SVG is allowed at upload time — the SVG-XSS chain is broken
		// at the serve path (Content-Disposition: attachment) and SVG
		// logos / diagrams are a common legitimate upload.
		{"svg is allowed", "logo.svg", "image/svg+xml", false},
		// JS is allowed because source-code attachments preview as
		// text/plain via /api/attachments/{id}/content. Blocking it
		// here would break the preview feature without adding security
		// on top of the disposition fix.
		{"js source upload is allowed", "snippet.js", "application/javascript", false},

		// Denied: HTML family by extension.
		{".html denied", "evil.html", "text/plain", true},
		{".htm denied", "evil.htm", "text/plain", true},
		{".xhtml denied", "evil.xhtml", "text/plain", true},
		{".shtml denied", "evil.shtml", "text/plain", true},
		{".xht denied", "evil.xht", "text/plain", true},
		{".phtml denied", "evil.phtml", "text/plain", true},

		// Denied: HTML by sniffed content type even if extension is benign.
		// This is the renamed-payload case — logo.png that is actually
		// HTML must still be refused.
		{"text/html under image extension", "logo.png", "text/html", true},
		{"text/html with charset param", "logo.png", "text/html; charset=utf-8", true},
		{"application/xhtml+xml", "diagram.svg", "application/xhtml+xml", true},

		// Case-insensitive on extension and content type.
		{"upper-case extension", "evil.HTML", "text/plain", true},
		{"upper-case content-type", "logo.png", "TEXT/HTML", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUploadDenied(tc.filename, tc.contentType); got != tc.want {
				t.Errorf("isUploadDenied(%q, %q) = %v, want %v",
					tc.filename, tc.contentType, got, tc.want)
			}
		})
	}
}

// TestUploadFile_RejectsHTMLByExtension verifies the upload-edge gate fires
// when a caller tries to upload a .html file. Defense-in-depth on top of
// the Content-Disposition: attachment fix from PR #3023.
func TestUploadFile_RejectsHTMLByExtension(t *testing.T) {
	if testHandler == nil {
		t.Skip("test database not available")
	}
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "evil.html")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("<script>alert(1)</script>"))
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload-file", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 for .html upload, got %d: %s", w.Code, w.Body.String())
	}
}

// TestUploadFile_RejectsHTMLByContentType verifies the sniffer-side gate.
// A caller renames an HTML payload to logo.png — the extension check
// passes, but http.DetectContentType returns "text/html" so the
// content-type denylist refuses the upload.
func TestUploadFile_RejectsHTMLByContentType(t *testing.T) {
	if testHandler == nil {
		t.Skip("test database not available")
	}
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	// Disguise as PNG — extension passes, content sniffs as text/html.
	part, err := writer.CreateFormFile("file", "logo.png")
	if err != nil {
		t.Fatal(err)
	}
	// Leading "<!DOCTYPE html" is the strongest text/html sniff signal.
	part.Write([]byte("<!DOCTYPE html><html><body><script>alert(1)</script></body></html>"))
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload-file", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 for renamed HTML payload, got %d: %s", w.Code, w.Body.String())
	}
}

// TestUploadFile_AllowsLegitimateImage is a regression guard: the new
// denylist must not start refusing routine image uploads.
func TestUploadFile_AllowsLegitimateImage(t *testing.T) {
	if testHandler == nil {
		t.Skip("test database not available")
	}
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "logo.png")
	if err != nil {
		t.Fatal(err)
	}
	// Real PNG signature — DetectContentType returns image/png.
	part.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload-file", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for legitimate PNG, got %d: %s", w.Code, w.Body.String())
	}

	// Clean up the attachment row so this test is rerunnable.
	if _, err := testPool.Exec(
		context.Background(),
		`DELETE FROM attachment WHERE workspace_id = $1 AND filename = $2`,
		testWorkspaceID,
		"logo.png",
	); err != nil {
		t.Fatalf("cleanup attachment: %v", err)
	}
}

// TestServeLocalUpload_RequiresAuth verifies the handler refuses a request
// where the upstream Auth middleware did not stamp X-User-ID. Auth is the
// outer gate; this assertion confirms the inner handler does not have a
// "default open" mode if ever reached without it.
func TestServeLocalUpload_RequiresAuth(t *testing.T) {
	if testHandler == nil {
		t.Skip("test database not available")
	}
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/uploads/workspaces/"+testWorkspaceID+"/anything.png", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeLocalUpload(local)(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without X-User-ID, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestServeLocalUpload_MemberCanRead is the happy path: a workspace member
// hitting their own workspace's upload bytes gets 200 + the file body.
func TestServeLocalUpload_MemberCanRead(t *testing.T) {
	if testHandler == nil {
		t.Skip("test database not available")
	}
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	key := "workspaces/" + testWorkspaceID + "/abc.png"
	if _, err := local.Upload(context.Background(), key, []byte("body-bytes"), "image/png", "logo.png"); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/uploads/"+key, nil)
	req.Header.Set("X-User-ID", testUserID)
	rec := httptest.NewRecorder()
	testHandler.ServeLocalUpload(local)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "body-bytes") {
		t.Errorf("body did not match: %q", rec.Body.String())
	}
}

// TestServeLocalUpload_NonMemberDenied verifies that an authenticated user
// hitting a workspace they do NOT belong to gets 404 (not 403, to avoid an
// IDOR oracle that would let them probe for workspace IDs they have no
// business knowing).
func TestServeLocalUpload_NonMemberDenied(t *testing.T) {
	if testHandler == nil {
		t.Skip("test database not available")
	}
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	// Foreign workspace ID — testUserID is not a member.
	foreignWorkspaceID := "00000000-0000-0000-0000-000000000099"
	key := "workspaces/" + foreignWorkspaceID + "/abc.png"
	if _, err := local.Upload(context.Background(), key, []byte("foreign-body"), "image/png", "logo.png"); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/uploads/"+key, nil)
	req.Header.Set("X-User-ID", testUserID)
	rec := httptest.NewRecorder()
	testHandler.ServeLocalUpload(local)(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-member, got %d: %s", rec.Code, rec.Body.String())
	}
	// 404 must not leak the bytes.
	if strings.Contains(rec.Body.String(), "foreign-body") {
		t.Errorf("response body leaked file contents: %q", rec.Body.String())
	}
}

// TestServeLocalUpload_RejectsDirectoryInPath verifies the handler refuses
// requests whose path resolves to a directory or workspace root, even for
// legitimately-authenticated members. This is the disclosure's
// "directory listing" vector applied at the route layer.
func TestServeLocalUpload_RejectsDirectoryInPath(t *testing.T) {
	if testHandler == nil {
		t.Skip("test database not available")
	}
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}
	// Seed two files so a listing would have something to leak.
	if _, err := local.Upload(context.Background(), "workspaces/"+testWorkspaceID+"/a.png", []byte("a"), "image/png", "a.png"); err != nil {
		t.Fatalf("Upload a: %v", err)
	}
	if _, err := local.Upload(context.Background(), "workspaces/"+testWorkspaceID+"/b.png", []byte("b"), "image/png", "b.png"); err != nil {
		t.Fatalf("Upload b: %v", err)
	}

	cases := []string{
		"/uploads/workspaces/" + testWorkspaceID + "/",
		"/uploads/workspaces/" + testWorkspaceID,
		"/uploads/",
		"/uploads/workspaces/",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("X-User-ID", testUserID)
			rec := httptest.NewRecorder()
			testHandler.ServeLocalUpload(local)(rec, req)

			if rec.Code == http.StatusOK {
				t.Errorf("status = 200, want 404 (directory request must not return 200)")
			}
			if strings.Contains(rec.Body.String(), "a.png") || strings.Contains(rec.Body.String(), "b.png") {
				t.Errorf("body leaked filenames: %q", rec.Body.String())
			}
		})
	}
}

// TestServeLocalUpload_UnknownPrefixDenied verifies the explicit-allowlist
// behavior: a key prefix the handler doesn't know about must 404 instead
// of falling through to the storage layer with no auth.
func TestServeLocalUpload_UnknownPrefixDenied(t *testing.T) {
	if testHandler == nil {
		t.Skip("test database not available")
	}
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}
	if _, err := local.Upload(context.Background(), "secrets/admin.png", []byte("secret"), "image/png", "x.png"); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/uploads/secrets/admin.png", nil)
	req.Header.Set("X-User-ID", testUserID)
	rec := httptest.NewRecorder()
	testHandler.ServeLocalUpload(local)(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown prefix, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("body leaked file contents: %q", rec.Body.String())
	}
}

// TestServeLocalUpload_UserPrefixAllowsAnyAuthedUser confirms that the
// /uploads/users/{userID}/* path is reachable by any authenticated user,
// matching the avatar-display use case (member lists / inbox items
// reference avatars across workspace boundaries).
func TestServeLocalUpload_UserPrefixAllowsAnyAuthedUser(t *testing.T) {
	if testHandler == nil {
		t.Skip("test database not available")
	}
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	// Owner-by-someone-else avatar — the testUserID is reading
	// somebody else's avatar bytes.
	otherUserID := "00000000-0000-0000-0000-000000000088"
	key := "users/" + otherUserID + "/avatar.png"
	if _, err := local.Upload(context.Background(), key, []byte("avatar-body"), "image/png", "avatar.png"); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/uploads/"+key, nil)
	req.Header.Set("X-User-ID", testUserID)
	rec := httptest.NewRecorder()
	testHandler.ServeLocalUpload(local)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for /uploads/users/*, got %d: %s", rec.Code, rec.Body.String())
	}
}
