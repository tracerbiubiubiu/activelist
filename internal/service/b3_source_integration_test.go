//go:build integration

package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/activelist/internal/config"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

func TestAL_MonitoredCommitAndRollback(t *testing.T) {
	pool, types := setupPG(t)
	ctx := context.Background()
	const typeName = "b3_documents"
	_, err := types.Register(ctx, service.RegisterInput{TypeName: typeName, Fields: []meta.Field{
		{Name: "scope", Type: "string", Required: true},
		{Name: "name", Type: "string", Required: true},
		{Name: "secret", Type: "string", Sensitive: true},
	}}, "test")
	require.NoError(t, err)
	data := newDataSvc(pool)
	require.NoError(t, data.EnableB3(ctx, config.B3{
		TypeName: typeName, AnchorField: "scope", AnchorValue: "fixture",
		WriteFields: []string{"name"}, ReadFields: []string{"name"}, EventFields: []string{"name"},
		ProducerAK: "b3-producer", CoreAK: "b3-core",
		ConsumerID: "zhuzhao-b3-consumer", PushAK: "b3-push", PushSK: "synthetic-test", PushURL: "http://localhost/internal/activelist/changes/push",
		GrantRef: "grant-b3-0000001", GrantRevision: "1", ReadGrantRef: "grant-b3-read-0001", ReadGrantRevision: "1",
	}))
	producer := service.B3ProducerContext(ctx, "b3-producer")
	_, err = data.Insert(ctx, typeName, service.InsertInput{Data: map[string]any{"name": "rejected"}}, "test")
	require.Equal(t, 403, mustAE(t, err).HTTP)
	created, err := data.Insert(producer, typeName, service.InsertInput{Data: map[string]any{"name": "first"}}, "test")
	require.NoError(t, err)
	require.Equal(t, "fixture", created.Data["scope"])

	var event []byte
	require.NoError(t, pool.QueryRow(ctx, "SELECT event FROM _al_b3_outbox WHERE cursor=1").Scan(&event))
	var fact struct {
		Type string `json:"type"`
		Data struct {
			DocumentID  string         `json:"document_id"`
			EventValues map[string]any `json:"event_values"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(event, &fact))
	require.Equal(t, "al.document.created.v1", fact.Type)
	require.Equal(t, map[string]any{"name": "first"}, fact.Data.EventValues)
	require.NotEmpty(t, fact.Data.DocumentID)

	_, err = pool.Exec(ctx, "ALTER TABLE _al_b3_outbox ADD CONSTRAINT reject_following_event CHECK (cursor=1)")
	require.NoError(t, err)
	_, err = data.Update(producer, typeName, created.ID, service.UpdateInput{Version: created.Version, Data: map[string]any{"name": "second"}}, "test")
	require.Error(t, err)
	current, err := repository.GetDocByID(ctx, pool, typeName, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.Version, current.Version)
	require.Equal(t, "first", current.Data["name"])
	var head int64
	require.NoError(t, pool.QueryRow(ctx, "SELECT cursor FROM _al_b3_stream").Scan(&head))
	require.EqualValues(t, 1, head)
}

func TestAL_PushRequiresBoundAckAndRecoversInOrder(t *testing.T) {
	pool, types := setupPG(t)
	ctx := context.Background()
	const typeName = "b3_push"
	_, err := types.Register(ctx, service.RegisterInput{TypeName: typeName, Fields: []meta.Field{
		{Name: "scope", Type: "string", Required: true},
		{Name: "name", Type: "string", Required: true},
	}}, "test")
	require.NoError(t, err)
	var seen []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var delivery struct {
			DeliveryID string          `json:"delivery_id"`
			Event      json.RawMessage `json:"event"`
		}
		if err := json.NewDecoder(r.Body).Decode(&delivery); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var identity struct {
			Source string `json:"source"`
			ID     string `json:"id"`
			Data   struct {
				Cursor string `json:"cursor"`
			} `json:"data"`
		}
		if err := json.Unmarshal(delivery.Event, &identity); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, identity.Data.Cursor)
		sum := sha256.Sum256(delivery.Event)
		ack := map[string]string{
			"schema_version": "1", "delivery_id": delivery.DeliveryID, "source": identity.Source,
			"event_id": identity.ID, "payload_sha256": hex.EncodeToString(sum[:]),
			"stage_commit_ref": "core-stage-0000001", "state": "durably_staged",
		}
		if len(seen) == 1 {
			ack["event_id"] = "different-event-id"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ack)
	}))
	defer server.Close()
	b3 := config.B3{
		TypeName: typeName, AnchorField: "scope", AnchorValue: "fixture",
		WriteFields: []string{"name"}, ReadFields: []string{"name"}, EventFields: []string{"name"},
		ProducerAK: "b3-producer", CoreAK: "b3-core", ConsumerID: "zhuzhao-b3-consumer",
		PushAK: "b3-al-push", PushSK: "synthetic-push-key", PushURL: server.URL + "/internal/activelist/changes/push",
		GrantRef: "grant-b3-0000001", GrantRevision: "1", ReadGrantRef: "grant-b3-read-0001", ReadGrantRevision: "1",
	}
	svc := newDataSvc(pool)
	require.NoError(t, svc.EnableB3(ctx, b3))
	producer := service.B3ProducerContext(ctx, "b3-producer")
	for _, name := range []string{"one", "two"} {
		_, err := svc.Insert(producer, typeName, service.InsertInput{Data: map[string]any{"name": name}}, "test")
		require.NoError(t, err)
	}
	require.Error(t, svc.PushNextB3(ctx))
	require.NoError(t, svc.PushNextB3(ctx))
	var ackCursor int64
	require.NoError(t, pool.QueryRow(ctx, "SELECT ack_cursor FROM _al_b3_stream").Scan(&ackCursor))
	require.Zero(t, ackCursor)
	_, err = pool.Exec(ctx, "UPDATE _al_b3_outbox SET next_attempt_at=clock_timestamp() WHERE cursor=1")
	require.NoError(t, err)
	restarted := newDataSvc(pool)
	require.NoError(t, restarted.EnableB3(ctx, b3))
	require.NoError(t, restarted.PushNextB3(ctx))
	require.NoError(t, restarted.PushNextB3(ctx))
	require.NoError(t, pool.QueryRow(ctx, "SELECT ack_cursor FROM _al_b3_stream").Scan(&ackCursor))
	require.EqualValues(t, 2, ackCursor)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"1", "1", "2"}, seen)
}
