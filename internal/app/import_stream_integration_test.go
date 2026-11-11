//go:build integration

package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/tracerbiubiubiu/zhuzhao-utils/aksk"

	"github.com/tracerbiubiubiu/activelist/internal/config"
	"github.com/tracerbiubiubiu/activelist/internal/handler"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

func importTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:15-alpine", postgres.WithDatabase("stream_import"), postgres.WithUsername("al"), postgres.WithPassword("synthetic-test-only"), testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(time.Minute)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	require.NoError(t, repository.MigrateUp(dsn))
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func streamServices(t *testing.T, max int64) (*pgxpool.Pool, *service.TypeService, *service.DataService) {
	t.Helper()
	pool := importTestPool(t)
	types := service.NewTypeService(pool)
	_, err := types.Register(context.Background(), service.RegisterInput{TypeName: "stream_import", Fields: []meta.Field{{Name: "name", Type: "string"}}}, "E800111")
	require.NoError(t, err)
	return pool, types, service.NewDataService(pool, 20, 100, 2, max)
}

func signedImportRequest(path string, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	aksk.Sign(req, body, aksk.SignOptions{AK: "zhuzhao", SK: []byte("synthetic-stream-sk"), Operator: "E800111", RequestID: "stream-import-audit"})
	return req
}

// The actual app provider used to put a 16MiB in-memory verifier in front of the
// 1GiB import handler. Test its assembled routes, not just the handler in isolation.
func TestImportStreaming_ProviderAcceptsLargeSignedBody(t *testing.T) {
	_, types, data := streamServices(t, 1<<30)
	cfg := &config.Config{Security: config.Security{Callers: map[string]string{"zhuzhao": "synthetic-stream-sk"}}}
	router := provideEngine(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), types, data, nil)
	large := append([]byte{'['}, bytes.Repeat([]byte{' '}, 17<<20)...)
	large = append(large, ']')
	t.Run("import_over_16MiB", func(t *testing.T) {
		req := signedImportRequest("/api/v1/data/stream_import/import", large)
		// No Content-Length must still be bounded and authenticated.
		req.ContentLength = -1
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})
	t.Run("ordinary_CRUD_still_16MiB", func(t *testing.T) {
		req := signedImportRequest("/api/v1/data/stream_import", large)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusRequestEntityTooLarge, w.Code, w.Body.String())
	})
}

// Failure must occur before import's destructive transaction. Preserve a real
// existing document through authentication, size, read, parse and storage errors.
func TestImportStreaming_FailurePreservesDataAndCleansSpool(t *testing.T) {
	_, types, data := streamServices(t, 64)
	ctx := context.Background()
	original, err := data.Insert(ctx, "stream_import", service.InsertInput{Data: map[string]any{"name": "keep me"}}, "E800111")
	require.NoError(t, err)
	for _, mode := range []string{"size", "bad-signature", "invalid-JSON", "read-error", "cancelled", "disk-error"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			tmpdir := dir
			router := func() http.Handler {
				if mode == "disk-error" {
					tmpdir = filepath.Join(dir, "not-directory")
					require.NoError(t, os.WriteFile(tmpdir, []byte("x"), 0600))
				}
				return handler.New(handler.Deps{Types: types, Data: data, Callers: map[string][]byte{"zhuzhao": []byte("synthetic-stream-sk")}, MaxBodyBytes: 16 << 20, ImportTempDir: tmpdir})
			}()
			body := []byte(`[{"id":"1","data":{"name":"replacement"}}]`)
			want := http.StatusUnauthorized
			if mode == "size" {
				body = bytes.Repeat([]byte{' '}, 65)
				want = http.StatusRequestEntityTooLarge
			}
			if mode == "invalid-JSON" {
				body = []byte("[")
				want = http.StatusBadRequest
			}
			if mode == "disk-error" {
				want = http.StatusServiceUnavailable
			}
			if mode == "read-error" || mode == "cancelled" {
				want = http.StatusBadRequest
			}
			req := signedImportRequest("/api/v1/data/stream_import/import", body)
			req.ContentLength = -1
			if mode == "bad-signature" {
				req.Body = io.NopCloser(bytes.NewReader([]byte("tampered")))
			}
			if mode == "read-error" {
				req.Body = io.NopCloser(streamReadError{})
			}
			if mode == "cancelled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				req = req.WithContext(c)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, want, w.Code, w.Body.String())
			got, err := data.Get(ctx, "stream_import", original.ID)
			require.NoError(t, err)
			require.Equal(t, "keep me", got.Data["name"])
			files, err := os.ReadDir(dir)
			require.NoError(t, err)
			expected := 0
			if mode == "disk-error" {
				expected = 1
			}
			require.Len(t, files, expected, "temporary files leaked")
		})
	}
	t.Run("authenticated_success_then_cleanup", func(t *testing.T) {
		dir := t.TempDir()
		router := handler.New(handler.Deps{Types: types, Data: data, Callers: map[string][]byte{"zhuzhao": []byte("synthetic-stream-sk")}, MaxBodyBytes: 16 << 20, ImportTempDir: dir})
		body := []byte(`[{"id":"1","data":{"name":"replacement"}}]`)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, signedImportRequest("/api/v1/data/stream_import/import", body))
		require.Equal(t, 200, w.Code, w.Body.String())
		got, err := data.Get(ctx, "stream_import", 1)
		require.NoError(t, err)
		require.Equal(t, "replacement", got.Data["name"])
		files, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Empty(t, files)
	})
}

type streamReadError struct{}

func (streamReadError) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
