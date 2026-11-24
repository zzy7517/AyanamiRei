package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestLocalUploadEntryIsDisabled(t *testing.T) {
	t.Setenv("S3_BUCKET", "")
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "")
	dir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", dir)
	key := "workspaces/" + testWorkspaceID + "/" + uuid.NewString() + ".png"
	full := filepath.Join(dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{full, full + ".meta.json"} {
		if err := os.WriteFile(name, []byte("PRIVATE"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	router, _ := newTestRouter(testPool, realtime.NewHub(), events.New(), RouterOptions{})
	for _, path := range []string{
		"/uploads",
		"/uploads/",
		"/uploads/workspaces/",
		"/uploads/workspaces/" + testWorkspaceID + "/",
		"/uploads/" + key,
		"/uploads/" + key + ".meta.json/",
	} {
		for _, token := range []string{"", testToken} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				req := httptest.NewRequest(method, path, nil)
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				if w.Code != http.StatusNotFound {
					t.Errorf("%s %s (authenticated=%t) = %d, want 404", method, path, token != "", w.Code)
				}
			}
		}
	}
}

func TestLocalAttachmentAPIStillRequiresMembership(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.test")
	t.Setenv("S3_BUCKET", "")
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "")
	dir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", dir)
	id := uuid.NewString()
	key := "workspaces/" + testWorkspaceID + "/" + id + ".png"
	full := filepath.Join(dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	const body = "PRIVATE IMAGE"
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := testutil.New(testPool, testWorkspaceID, testUserID)
	fixture.Insert(t, "attachment", testutil.Cols{
		"id": id, "workspace_id": testWorkspaceID,
		"uploader_type": "member", "uploader_id": testUserID,
		"filename": "private.png", "url": "/uploads/" + key,
		"content_type": "image/png", "size_bytes": len(body),
	})
	router, _ := newTestRouter(testPool, realtime.NewHub(), events.New(), RouterOptions{})
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"no bearer", ""},
		{"non-task bearer", testToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/attachments/"+id+"/download", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
			}
			if got := w.Body.String(); got != body {
				t.Fatalf("body = %q, want %q", got, body)
			}
			if got := w.Header().Get("Content-Disposition"); got != `inline; filename="private.png"` {
				t.Errorf("Content-Disposition = %q, want original filename", got)
			}
			if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'self' https://app.example.test") {
				t.Errorf("Content-Security-Policy = %q, want configured preview origin", got)
			}
		})
	}

	foreignWS := fixture.Workspace(t, "Attachment other workspace", "attach-other-"+uuid.NewString())
	foreignID := uuid.NewString()
	foreignKey := "workspaces/" + foreignWS + "/" + foreignID + ".png"
	foreignFull := filepath.Join(dir, filepath.FromSlash(foreignKey))
	if err := os.MkdirAll(filepath.Dir(foreignFull), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignFull, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Insert(t, "attachment", testutil.Cols{
		"id": foreignID, "workspace_id": foreignWS,
		"uploader_type": "member", "uploader_id": testUserID,
		"filename": "other.png", "url": "/uploads/" + foreignKey,
		"content_type": "image/png", "size_bytes": len(body),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/attachments/"+foreignID+"/download", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("other workspace status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}
