//go:build integration

package app

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tracerbiubiubiu/activelist/internal/config"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/service"
	"github.com/tracerbiubiubiu/zhuzhao-utils/aksk"
	"github.com/tracerbiubiubiu/zhuzhao-utils/errcode"
)

// Simulate real storage inconsistency by removing a registered dynamic table.
// This does not imply that a normal API caller can execute DDL.
func TestFailureEnvelope_RealPGExportDiagnostics(t *testing.T) {
	pool := importTestPool(t)
	ctx := context.Background()
	types := service.NewTypeService(pool)
	data := service.NewDataService(pool, 20, 100, 2, 1<<20)
	var logs bytes.Buffer
	cfg := &config.Config{Security: config.Security{Callers: map[string]string{"zhuzhao": "synthetic-error-sk"}}}
	router := provideEngine(cfg, slog.New(slog.NewJSONHandler(&logs, nil)), types, data, nil)
	for round := range 8 {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			name := fmt.Sprintf("error_probe_%d", round)
			_, err := types.Register(ctx, service.RegisterInput{TypeName: name, Fields: []meta.Field{{Name: "name", Type: "string"}}}, "E800115")
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `DROP TABLE "`+name+`"`)
			require.NoError(t, err)
			logs.Reset()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/data/"+name+"/export", nil)
			aksk.Sign(req, nil, aksk.SignOptions{AK: "zhuzhao", SK: []byte("synthetic-error-sk"), Operator: "E800115", RequestID: "error-envelope-audit"})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, 500, w.Code)
			require.NotContains(t, w.Body.String(), "SQLSTATE")
			require.NotContains(t, w.Body.String(), name)
			require.Contains(t, w.Body.String(), errcode.ErrInternal.Message)
			require.Contains(t, logs.String(), "SQLSTATE 42P01", "original DB diagnostic must remain in server error log")
			require.Contains(t, logs.String(), "error-envelope-audit", "diagnostic must be traceable to its request")
			t.Logf("round=%d HTTP=500 public_diagnostic=false private_error_logged=true request_correlated=true", round)
		})
	}
}
