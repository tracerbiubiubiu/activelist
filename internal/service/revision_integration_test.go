//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/service"
	"github.com/tracerbiubiubiu/activelist/migrations"
)

// 导入、清空后ID重用、重导和后续多次更新均不能再次发出旧客户端的version。
func TestRevision_FullReplaceNeverReusesVersion(t *testing.T) {
	pool, types := setupPG(t)
	data := newDataSvc(pool)
	ctx := context.Background()
	const name = "revision_aba"
	_, err := types.Register(ctx, service.RegisterInput{TypeName: name, Fields: []meta.Field{{Name: "name", Type: "string"}}}, "audit")
	require.NoError(t, err)
	old, err := data.Insert(ctx, name, service.InsertInput{Data: map[string]any{"name": "old"}}, "audit")
	require.NoError(t, err)
	oldVersions := []int64{old.Version}

	checkStale := func(t *testing.T) {
		t.Helper()
		for _, stale := range oldVersions {
			_, err := data.Update(ctx, name, old.ID, service.UpdateInput{Version: stale, Data: map[string]any{"name": "stale"}}, "audit")
			ae := mustAE(t, err)
			require.Equal(t, 409, ae.HTTP, "旧version=%d不得在新数据上复活", stale)
		}
	}
	file := `[{"id":"1","version":9223372036854775807,"data":{"name":"imported"}}]`
	t.Run("replace_old_v1", func(t *testing.T) {
		_, err := data.Import(ctx, name, strings.NewReader(file), "audit")
		require.NoError(t, err)
		checkStale(t)
	})
	current, err := data.Get(ctx, name, old.ID)
	require.NoError(t, err)
	for range 4 {
		oldVersions = append(oldVersions, current.Version)
		updated, err := data.Update(ctx, name, current.ID, service.UpdateInput{Version: current.Version, Data: map[string]any{"name": "newer"}}, "audit")
		require.NoError(t, err)
		require.Greater(t, updated.Version, current.Version)
		current = updated
	}
	oldVersions = append(oldVersions, current.Version)
	t.Run("empty_replace_then_recreate", func(t *testing.T) {
		_, err := data.Import(ctx, name, strings.NewReader(`[]`), "audit")
		require.NoError(t, err)
		recreated, err := data.Insert(ctx, name, service.InsertInput{Data: map[string]any{"name": "recreated"}}, "audit")
		require.NoError(t, err)
		require.Equal(t, old.ID, recreated.ID, "ID可复用，revision不可复用")
		checkStale(t)
	})
	t.Run("reimport_then_updates", func(t *testing.T) {
		for range 3 {
			before, err := data.Get(ctx, name, old.ID)
			require.NoError(t, err)
			oldVersions = append(oldVersions, before.Version)
			_, err = data.Import(ctx, name, strings.NewReader(file), "audit")
			require.NoError(t, err)
			after, err := data.Get(ctx, name, old.ID)
			require.NoError(t, err)
			require.Greater(t, after.Version, before.Version)
			checkStale(t)
		}
	})
}

// 全部写入口共享分配器，事务回滚造成的空洞不能被再次发出。
func TestRevision_AllWritersAndRollback(t *testing.T) {
	pool, types := setupPG(t)
	data := service.NewDataService(pool, 20, 100, 1, 1<<20)
	ctx := context.Background()
	for _, name := range []string{"revision_a", "revision_b"} {
		_, err := types.Register(ctx, service.RegisterInput{TypeName: name, Fields: []meta.Field{{Name: "name", Type: "string", Required: true}}}, "audit")
		require.NoError(t, err)
	}
	a, err := data.Insert(ctx, "revision_a", service.InsertInput{Data: map[string]any{"name": "a"}}, "audit")
	require.NoError(t, err)
	b, err := data.Insert(ctx, "revision_b", service.InsertInput{Data: map[string]any{"name": "b"}}, "audit")
	require.NoError(t, err)
	require.Greater(t, b.Version, a.Version)
	updated, err := data.Update(ctx, "revision_a", a.ID, service.UpdateInput{Version: a.Version, Data: map[string]any{"name": "update"}}, "audit")
	require.NoError(t, err)
	require.Greater(t, updated.Version, b.Version)
	deleted, err := data.SoftDelete(ctx, "revision_a", a.ID, "audit")
	require.NoError(t, err)
	require.Greater(t, deleted.Version, updated.Version)
	restored, err := data.Restore(ctx, "revision_a", a.ID, "audit")
	require.NoError(t, err)
	require.Greater(t, restored.Version, deleted.Version)

	_, err = data.Import(ctx, "revision_a", strings.NewReader(`[{"id":"1","data":{"name":"valid"}},{"id":"2","data":{}}]`), "audit")
	require.Equal(t, 422, mustAE(t, err).HTTP)
	var consumed int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT last_value FROM public._al_document_revision_seq`).Scan(&consumed))
	kept, err := data.Get(ctx, "revision_a", a.ID)
	require.NoError(t, err)
	require.Equal(t, restored.Version, kept.Version)
	require.Equal(t, "update", kept.Data["name"])
	fresh, err := data.Insert(ctx, "revision_a", service.InsertInput{Data: map[string]any{"name": "after rollback"}}, "audit")
	require.NoError(t, err)
	require.Greater(t, fresh.Version, consumed)
}

// 真实迁移SQL：旧库高版本不改写，sequence起点高于所有存量表；down/up成对可执行。
func TestRevision_MigrationInitializesAboveExistingVersions(t *testing.T) {
	pool, types := setupPG(t)
	ctx := context.Background()
	_, err := types.Register(ctx, service.RegisterInput{TypeName: "revision_legacy", Fields: []meta.Field{{Name: "name", Type: "string"}}}, "audit")
	require.NoError(t, err)
	down, err := migrations.FS.ReadFile("000002_document_revision.down.sql")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err)
	raw, err := json.Marshal(map[string]any{"name": "legacy"})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO revision_legacy(data, version) VALUES ($1, 5000)`, raw)
	require.NoError(t, err)
	up, err := migrations.FS.ReadFile("000002_document_revision.up.sql")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err)
	data := newDataSvc(pool)
	old, err := data.Get(ctx, "revision_legacy", 1)
	require.NoError(t, err)
	require.EqualValues(t, 5000, old.Version)
	fresh, err := data.Insert(ctx, "revision_legacy", service.InsertInput{Data: map[string]any{"name": "new"}}, "audit")
	require.NoError(t, err)
	require.Greater(t, fresh.Version, old.Version)
}

// Legacy public writes advance a row at most once per transaction. Even when
// an empty import has removed every row, clients may retain those old versions.
// Seeding only MAX(current version) would reissue version=1 after this upgrade.
func TestRevision_MigrationFencesDeletedLegacyVersions(t *testing.T) {
	pool, types := setupPG(t)
	ctx := context.Background()
	const name = "revision_legacy_empty"
	_, err := types.Register(ctx, service.RegisterInput{TypeName: name, Fields: []meta.Field{{Name: "name", Type: "string"}}}, "audit")
	require.NoError(t, err)
	down, err := migrations.FS.ReadFile("000002_document_revision.down.sql")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err)
	var id, version int64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO revision_legacy_empty(data) VALUES ('{"name":"old"}') RETURNING id,version`).Scan(&id, &version))
	oldVersions := []int64{version}
	for range 6 {
		// Each pool statement commits separately, as each legacy HTTP write did.
		require.NoError(t, pool.QueryRow(ctx, `UPDATE revision_legacy_empty SET version=version+1 WHERE id=$1 RETURNING version`, id).Scan(&version))
		oldVersions = append(oldVersions, version)
	}
	data := newDataSvc(pool)
	_, err = data.Import(ctx, name, strings.NewReader(`[]`), "audit")
	require.NoError(t, err)
	up, err := migrations.FS.ReadFile("000002_document_revision.up.sql")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err)
	fresh, err := data.Insert(ctx, name, service.InsertInput{Data: map[string]any{"name": "new"}}, "audit")
	require.NoError(t, err)
	require.Equal(t, id, fresh.ID)
	require.Greater(t, fresh.Version, oldVersions[len(oldVersions)-1], "upgrade must fence deleted legacy history too")
	for _, stale := range oldVersions {
		_, err = data.Update(ctx, name, id, service.UpdateInput{Version: stale, Data: map[string]any{"name": "stale"}}, "audit")
		require.Equal(t, 409, mustAE(t, err).HTTP)
	}
}
