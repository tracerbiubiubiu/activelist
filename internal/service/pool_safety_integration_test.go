//go:build integration

package service_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

// 单连接是池耗尽的确定性模型：每条事务错误/幂等路径都必须只占一条连接。
// 不靠低并发概率命中；若持事务再向池取连接，子用例将耗尽 deadline。
func TestPoolSafety_TransactionPathsUseOneConnection(t *testing.T) {
	original, _ := setupPG(t)
	cfg := original.Config()
	cfg.MaxConns = 1
	original.Close()
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	types := service.NewTypeService(pool)
	data := service.NewDataService(pool, 20, 100, 100, 1<<20)
	input := service.RegisterInput{TypeName: "pool_safety", Fields: []meta.Field{
		{Name: "name", Type: "string", Required: true},
		{Name: "legacy", Type: "string"},
	}}
	_, err = types.Register(context.Background(), input, "audit")
	require.NoError(t, err)
	doc, err := data.Insert(context.Background(), input.TypeName, service.InsertInput{
		Data: map[string]any{"name": "sample", "legacy": "old"},
	}, "audit")
	require.NoError(t, err)

	check := func(t *testing.T, fn func(context.Context) error, wantHTTP int, wantCode, wantStatus string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := fn(ctx)
		if wantHTTP == 0 {
			require.NoError(t, err)
		} else {
			ae := mustAE(t, err)
			require.Equal(t, wantHTTP, ae.HTTP)
			require.Equal(t, wantCode, ae.Code)
			if wantStatus != "" {
				require.Equal(t, wantStatus, ae.Detail["current_status"])
			}
		}
		require.NoError(t, ctx.Err(), "不得等待自身事务占用的池连接直到超时")
	}
	t.Run("duplicate_active", func(t *testing.T) {
		check(t, func(ctx context.Context) error { _, err := types.Register(ctx, input, "audit"); return err }, http.StatusConflict, apperr.CodeTypeExists, meta.StatusActive)
	})
	t.Run("unknown_field", func(t *testing.T) {
		check(t, func(ctx context.Context) error {
			_, err := data.Update(ctx, input.TypeName, doc.ID, service.UpdateInput{Version: doc.Version, Data: map[string]any{"unknown": "value"}}, "audit")
			return err
		}, http.StatusUnprocessableEntity, apperr.CodeValidation, "")
	})
	_, err = types.Evolve(context.Background(), input.TypeName, service.EvolveInput{Version: 1, Fields: input.Fields[:1]}, "audit")
	require.NoError(t, err)
	t.Run("deprecated_field", func(t *testing.T) {
		check(t, func(ctx context.Context) error {
			_, err := data.Update(ctx, input.TypeName, doc.ID, service.UpdateInput{Version: doc.Version, Data: map[string]any{"name": "changed"}}, "audit")
			return err
		}, http.StatusUnprocessableEntity, apperr.CodeFieldDepr, "")
	})
	_, err = types.Deprecate(context.Background(), input.TypeName, "audit")
	require.NoError(t, err)
	t.Run("idempotent_deprecate", func(t *testing.T) {
		check(t, func(ctx context.Context) error { _, err := types.Deprecate(ctx, input.TypeName, "audit"); return err }, 0, "", "")
	})
	t.Run("duplicate_deprecated", func(t *testing.T) {
		check(t, func(ctx context.Context) error { _, err := types.Register(ctx, input, "audit"); return err }, http.StatusConflict, apperr.CodeTypeExists, meta.StatusDeprecated)
	})
}
