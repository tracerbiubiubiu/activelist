//go:build integration

// 唯一键查重集成验证（2026-10-27 所有者提出——多源汇聚整体去重）：
// 单键/组合键 409 / 软删行不占用唯一性（恢复撞键 409）/ 缺键行不去重 /
// 更新排除自身与撞他行。
package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

func insertErr(t *testing.T, dsvc *service.DataService, typeName string, data map[string]any) (*repository.Document, error) {
	t.Helper()
	return dsvc.Insert(context.Background(), typeName, service.InsertInput{Data: data}, "system")
}

func requireConflict409(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var ae *apperr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 409, ae.HTTP)
}

func TestUnique_SingleKey(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()
	const name = "zz_uniq_single"

	_, err := tsvc.Register(ctx, service.RegisterInput{
		TypeName:     name,
		Fields:       []meta.Field{{Name: "ip", Type: "string"}, {Name: "note", Type: "string"}},
		UniqueFields: []string{"ip"},
	}, "system")
	require.NoError(t, err)

	d1, err := insertErr(t, dsvc, name, map[string]any{"ip": "1.1.1.1", "note": "first"})
	require.NoError(t, err)

	// 同键再插 → 409 带存量行 id
	_, err = insertErr(t, dsvc, name, map[string]any{"ip": "1.1.1.1", "note": "dup"})
	requireConflict409(t, err)
	var ae *apperr.Error
	require.ErrorAs(t, err, &ae)
	require.EqualValues(t, d1.ID, ae.Detail["existing_id"])

	// 软删后同键可插（软删行不占用唯一性）
	_, err = dsvc.SoftDelete(ctx, name, d1.ID, "system")
	require.NoError(t, err)
	_, err = insertErr(t, dsvc, name, map[string]any{"ip": "1.1.1.1", "note": "reinsert"})
	require.NoError(t, err)

	// 恢复撞活跃行 → 409
	_, err = dsvc.Restore(ctx, name, d1.ID, "system")
	requireConflict409(t, err)
}

func TestUnique_CompositeKey(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()
	const name = "zz_uniq_comp"

	_, err := tsvc.Register(ctx, service.RegisterInput{
		TypeName:     name,
		Fields:       []meta.Field{{Name: "host", Type: "string"}, {Name: "cve", Type: "string"}},
		UniqueFields: []string{"host", "cve"},
	}, "system")
	require.NoError(t, err)

	_, err = insertErr(t, dsvc, name, map[string]any{"host": "h1", "cve": "CVE-1"})
	require.NoError(t, err)
	// 半撞（同 host 不同 cve）→ 放行
	_, err = insertErr(t, dsvc, name, map[string]any{"host": "h1", "cve": "CVE-2"})
	require.NoError(t, err)
	// 全撞 → 409
	_, err = insertErr(t, dsvc, name, map[string]any{"host": "h1", "cve": "CVE-1"})
	requireConflict409(t, err)
	var ae *apperr.Error
	require.ErrorAs(t, err, &ae)
	require.Contains(t, ae.Msg, "host+cve")
}

func TestUnique_UpdateExcludesSelf(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()
	const name = "zz_uniq_upd"

	_, err := tsvc.Register(ctx, service.RegisterInput{
		TypeName:     name,
		Fields:       []meta.Field{{Name: "ip", Type: "string"}},
		UniqueFields: []string{"ip"},
	}, "system")
	require.NoError(t, err)
	_, err = insertErr(t, dsvc, name, map[string]any{"ip": "5.5.5.5"})
	require.NoError(t, err)
	db, err := insertErr(t, dsvc, name, map[string]any{"ip": "6.6.6.6"})
	require.NoError(t, err)

	// 自更新（未改唯一键）→ 放行
	_, err = dsvc.Update(ctx, name, db.ID, service.UpdateInput{
		Data: map[string]any{"ip": "6.6.6.6"}, Version: db.Version,
	}, "system")
	require.NoError(t, err)
	// 更新撞他行 → 409
	_, err = dsvc.Update(ctx, name, db.ID, service.UpdateInput{
		Data: map[string]any{"ip": "5.5.5.5"}, Version: db.Version + 1,
	}, "system")
	requireConflict409(t, err)
}

func TestUnique_MissingKeyNotDeduped(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()
	const name = "zz_uniq_miss"

	_, err := tsvc.Register(ctx, service.RegisterInput{
		TypeName:     name,
		Fields:       []meta.Field{{Name: "ip", Type: "string"}, {Name: "note", Type: "string"}},
		UniqueFields: []string{"ip"},
	}, "system")
	require.NoError(t, err)
	// 唯一键缺省（optional）行不去重——两行缺键共存
	_, err = insertErr(t, dsvc, name, map[string]any{"note": "x"})
	require.NoError(t, err)
	_, err = insertErr(t, dsvc, name, map[string]any{"note": "y"})
	require.NoError(t, err)
}
