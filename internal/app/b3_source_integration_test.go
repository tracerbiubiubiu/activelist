//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tracerbiubiubiu/zhuzhao-utils/aksk"

	"github.com/tracerbiubiubiu/activelist/internal/config"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

func TestAL_B3SignedProducerAndLegacyGuards(t *testing.T) {
	pool := importTestPool(t)
	ctx := context.Background()
	types := service.NewTypeService(pool)
	fields := []meta.Field{
		{Name: "scope", Type: "string", Required: true},
		{Name: "name", Type: "string", Required: true},
		{Name: "secret", Type: "string", Sensitive: true},
	}
	for _, name := range []string{"b3_watch", "regular"} {
		definition := fields
		if name == "regular" {
			definition = fields[:2]
		}
		_, err := types.Register(ctx, service.RegisterInput{TypeName: name, Fields: definition, UniqueFields: []string{"name"}}, "test")
		require.NoError(t, err)
	}
	data := service.NewDataService(pool, 20, 100, 100, 1<<20)
	old, err := data.Insert(ctx, "b3_watch", service.InsertInput{Data: map[string]any{
		"scope": "owned", "name": "original", "secret": "unpublished-value",
	}}, "test")
	require.NoError(t, err)
	hidden, err := data.Insert(ctx, "b3_watch", service.InsertInput{Data: map[string]any{
		"scope": "other", "name": "reserved",
	}}, "test")
	require.NoError(t, err)
	_, err = data.Insert(ctx, "regular", service.InsertInput{Data: map[string]any{
		"scope": "other", "name": "reserved",
	}}, "test")
	require.NoError(t, err)
	cfg := &config.Config{
		B3: config.B3{
			TypeName: "b3_watch", AnchorField: "scope", AnchorValue: "owned",
			WriteFields: []string{"name"}, ReadFields: []string{"name"}, EventFields: []string{"name"},
			ProducerAK: "producer", CoreAK: "command", ConsumerID: "zhuzhao-b3-consumer",
			PushAK: "b3-al-push", PushSK: "synthetic-push-key", PushURL: "http://localhost/internal/activelist/changes/push",
			GrantRef: "grant-b3-0000001", GrantRevision: "1", ReadGrantRef: "grant-b3-read-0001", ReadGrantRevision: "1",
		},
		Security: config.Security{Callers: map[string]string{"producer": "synthetic-producer", "command": "synthetic-command", "legacy": "synthetic-legacy"}},
	}
	require.NoError(t, data.EnableB3(ctx, cfg.B3))
	router := provideEngine(cfg, nil, types, data, nil)
	run := func(method, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		aksk.Sign(req, []byte(body), aksk.SignOptions{AK: key, SK: []byte(cfg.Security.Callers[key])})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	businessResponse := func(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var payload map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
		delete(payload, "request_id")
		return payload
	}
	require.Equal(t, 403, run(http.MethodPost, "/api/v1/data/b3_watch", "legacy", `{"data":{"name":"denied"}}`).Code)
	require.Equal(t, 403, run(http.MethodPost, "/api/v1/data/regular", "producer", `{"data":{"scope":"owned","name":"denied"}}`).Code)
	require.Equal(t, 403, run(http.MethodPost, "/api/v1/data/b3_watch", "command", `{"data":{"name":"denied"}}`).Code)
	require.Equal(t, 403, run(http.MethodGet, "/api/v1/data/b3_watch/1", "legacy", "").Code)
	require.Equal(t, 403, run(http.MethodPost, "/api/v1/data/b3_watch/1/delete", "legacy", "{}").Code)
	require.Equal(t, 403, run(http.MethodPost, "/api/v1/data/restore", "legacy", `{"type_name":"b3_watch","id":"1"}`).Code)
	for _, path := range []string{"/internal/activelist/documents/update", "/internal/activelist/documents/get"} {
		require.Equal(t, 401, run(http.MethodPost, path, "producer", "{}").Code)
		require.Equal(t, 401, run(http.MethodPost, path, "legacy", "{}").Code)
		require.Equal(t, 400, run(http.MethodPost, path, "command", "{}").Code)
	}
	require.Equal(t, 403, run(http.MethodGet, "/api/v1/data/b3_watch", "legacy", "").Code)
	require.Equal(t, 403, run(http.MethodGet, "/api/v1/data/b3_watch/export", "legacy", "").Code)
	require.Equal(t, 403, run(http.MethodPost, "/api/v1/data/b3_watch/import", "legacy", `[]`).Code)
	require.Equal(t, 403, run(http.MethodPost, "/api/v1/admin/types/deprecate", "legacy", `{"type_name":"b3_watch"}`).Code)
	require.Equal(t, 403, run(http.MethodPost, "/api/v1/data/b3_watch", "producer", `{"data":{"scope":"other","name":"denied"}}`).Code)
	w := run(http.MethodPost, fmt.Sprintf("/api/v1/data/b3_watch/%d/update", old.ID), "producer", fmt.Sprintf(`{"version":%d,"data":{"name":"updated"}}`, old.Version))
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "unpublished-value")
	require.Equal(t, 200, run(http.MethodGet, "/api/v1/data/regular", "legacy", "").Code)
	t.Run("legacy type list and total exclude protected metadata", func(t *testing.T) {
		list := run(http.MethodGet, "/api/v1/admin/types", "legacy", "")
		require.Equal(t, 200, list.Code)
		var payload struct {
			Data struct {
				List  []meta.Definition `json:"list"`
				Total int               `json:"total"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(list.Body.Bytes(), &payload))
		require.Equal(t, 1, payload.Data.Total)
		require.Len(t, payload.Data.List, 1)
		require.Equal(t, "regular", payload.Data.List[0].TypeName)
		require.NotContains(t, list.Body.String(), `"secret"`)
	})
	for _, suffix := range []string{"", "/history"} {
		name := "detail"
		if suffix != "" {
			name = "history"
		}
		t.Run("legacy type "+name+" hides protected metadata", func(t *testing.T) {
			protected := run(http.MethodGet, "/api/v1/admin/types/b3_watch"+suffix, "legacy", "")
			absent := run(http.MethodGet, "/api/v1/admin/types/not_registered"+suffix, "legacy", "")
			require.Equal(t, 404, protected.Code)
			require.Equal(t, 404, absent.Code)
			require.Equal(t, businessResponse(t, absent), businessResponse(t, protected))
			ordinary := run(http.MethodGet, "/api/v1/admin/types/regular"+suffix, "legacy", "")
			require.Equal(t, 200, ordinary.Code)
			require.Contains(t, ordinary.Body.String(), `"name"`)
		})
	}
	require.Equal(t, 200, run(http.MethodGet, "/api/v1/admin/types/rules", "legacy", "").Code)

	t.Run("write target misses have the same response", func(t *testing.T) {
		body := fmt.Sprintf(`{"version":%d,"data":{"name":"new"}}`, hidden.Version)
		outside := run(http.MethodPost, fmt.Sprintf("/api/v1/data/b3_watch/%d/update", hidden.ID), "producer", body)
		absent := run(http.MethodPost, fmt.Sprintf("/api/v1/data/b3_watch/%d/update", hidden.ID+100), "producer", body)
		require.Equal(t, 404, outside.Code)
		require.Equal(t, 404, absent.Code)
		require.Equal(t, businessResponse(t, outside), businessResponse(t, absent))
	})
	t.Run("type-wide uniqueness does not identify the conflicting row", func(t *testing.T) {
		body := `{"data":{"name":"reserved"}}`
		insert := run(http.MethodPost, "/api/v1/data/b3_watch", "producer", body)
		require.Equal(t, 409, insert.Code)
		require.NotContains(t, insert.Body.String(), "existing_id")

		var currentVersion int64
		require.NoError(t, pool.QueryRow(ctx, "SELECT version FROM b3_watch WHERE id=$1", old.ID).Scan(&currentVersion))
		update := run(http.MethodPost, fmt.Sprintf("/api/v1/data/b3_watch/%d/update", old.ID), "producer", fmt.Sprintf(`{"version":%d,"data":{"name":"reserved"}}`, currentVersion))
		require.Equal(t, 409, update.Code)
		require.Equal(t, businessResponse(t, insert), businessResponse(t, update))

		ordinary := run(http.MethodPost, "/api/v1/data/regular", "legacy", `{"data":{"scope":"owned","name":"reserved"}}`)
		require.Equal(t, 409, ordinary.Code)
		require.Contains(t, ordinary.Body.String(), "existing_id")
	})
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM _al_b3_outbox").Scan(&count))
	require.Equal(t, 1, count)
}
