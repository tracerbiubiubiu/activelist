//go:build integration

package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tracerbiubiubiu/activelist/internal/config"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

func TestB3TaskWriteReceiptCASAndScope(t *testing.T) {
	pool, types := setupPG(t)
	ctx := context.Background()
	const name = "b3_reviews"
	_, err := types.Register(ctx, service.RegisterInput{TypeName: name, Fields: []meta.Field{{Name: "scope", Type: "string", Required: true}, {Name: "review_state", Type: "string"}}, UniqueFields: []string{"review_state"}}, "fixture")
	require.NoError(t, err)
	data := newDataSvc(pool)
	hidden, err := data.Insert(ctx, name, service.InsertInput{Data: map[string]any{"scope": "team-b", "review_state": "reserved"}}, "fixture")
	require.NoError(t, err)
	profile := config.B3{TypeName: name, AnchorField: "scope", AnchorValue: "team-a", WriteFields: []string{"review_state"}, ReadFields: []string{"review_state"}, EventFields: []string{"review_state"}, ProducerAK: "producer", CoreAK: "core", GrantRef: "grant-write-fixture1", GrantRevision: "1", ReadGrantRef: "grant-read--fixture1", ReadGrantRevision: "1", ConsumerID: "zhuzhao-b3-consumer", PushURL: "http://localhost/internal/activelist/changes/push", PushAK: "al-push", PushSK: "synthetic"}
	require.NoError(t, data.EnableB3(ctx, profile))
	producer := service.B3ProducerContext(ctx, "producer")
	doc, err := data.Insert(producer, name, service.InsertInput{Data: map[string]any{"review_state": "initial"}}, "fixture")
	require.NoError(t, err)
	var generation int64
	require.NoError(t, pool.QueryRow(ctx, "SELECT generation FROM _al_b3_stream").Scan(&generation))
	wireFor := func(id int64, op string, revision int64, value string, read bool) []byte {
		target := map[string]any{"owner": "activelist", "resource_type": "activelist_document", "resource_id": strconv.FormatInt(id, 10), "type_name": name, "generation": strconv.FormatInt(generation, 10)}
		now := time.Now().UTC().Truncate(time.Millisecond)
		grant, action, subject := "grant-write-fixture1", "update", "human"
		params := map[string]any{"type_name": name, "document_id": target["resource_id"], "generation": target["generation"]}
		kind, claim := "al_execution_request", "admission_claims"
		if read {
			action, subject, grant = "get", "service", "grant-read--fixture1"
			kind, claim = "al_read_request", "read_admission_claims"
			params["fields"] = []string{"review_state"}
		} else {
			params["expected_revision"] = strconv.FormatInt(revision, 10)
			params["patch"] = map[string]any{"review_state": value}
		}
		capID := "activelist.document." + action
		principal := map[string]any{"subject_id": "test-person", "subject_type": subject}
		proj, _ := json.Marshal(map[string]any{"capability_id": capID, "contract_version": "1", "subject": principal, "target": target, "params": params})
		hash := fmt.Sprintf("%x", sha256.Sum256(proj))
		principal["authorization_ref"] = grant
		principal["authorization_revision"] = "1"
		body, _ := json.Marshal(map[string]any{"schema_version": "1", "kind": kind, "command": map[string]any{"schema_version": "1", "kind": "command", "operation_id": op, "invocation_id": "inv-fixture-00000001", "capability_id": capID, "contract_version": "1", "canonical_input_hash": hash, "target": target, "principal": principal, "params": params, "causality": map[string]any{"root_id": "root-fixture-00000001", "root_budget_ref": "budget-fixture-00001", "depth": 0, "rule_path": []string{}}, "submitted_at": now, "expires_at": now.Add(time.Minute), "entrypoint": "internal"}, "admission": map[string]any{"schema_version": "1", "kind": claim, "purpose": "al_document_" + action, "audience": "activelist", "operation_id": op, "canonical_input_hash": hash, "authorization_ref": grant, "authorization_revision": "1", "target": target, "authorized_fields": []string{"review_state"}, "fence": "1", "issued_at": now, "expires_at": now.Add(30 * time.Second)}})
		return body
	}
	wire := func(op string, revision int64, value string, read bool) []byte {
		return wireFor(doc.ID, op, revision, value, read)
	}
	const original = "op-fixture-000000001"
	raw := wire(original, doc.Version, "processed", false)
	first, err := data.B3Execute(ctx, "core", raw, true)
	require.NoError(t, err)
	require.Equal(t, false, first["noop"])
	lookup := func(body []byte) []byte {
		var req map[string]any
		require.NoError(t, json.Unmarshal(body, &req))
		req["kind"] = "al_receipt_request"
		admission := req["admission"].(map[string]any)
		admission["kind"] = "receipt_admission_claims"
		admission["purpose"] = "al_effect_receipt"
		command := req["command"].(map[string]any)
		command["submitted_at"] = time.Now().Add(-time.Hour * 3)
		command["expires_at"] = time.Now().Add(-time.Hour)
		body, _ = json.Marshal(req)
		return body
	}
	originalLookup := lookup(raw)
	recovered, err := data.B3LookupReceipt(ctx, "core", originalLookup)
	require.NoError(t, err)
	require.Equal(t, first, recovered)
	_, err = data.B3LookupReceipt(ctx, "core", lookup(wire("op-fixture-no-effect", doc.Version, "processed", false)))
	require.Equal(t, 404, mustAE(t, err).HTTP)
	restarted := newDataSvc(pool)
	require.NoError(t, restarted.EnableB3(ctx, profile))
	replay, err := restarted.B3Execute(ctx, "core", raw, true)
	require.NoError(t, err)
	require.Equal(t, first, replay)
	_, err = data.B3Execute(ctx, "core", wire(original, doc.Version, "different", false), true)
	require.Equal(t, 409, mustAE(t, err).HTTP)
	current, err := repository.GetDocByID(ctx, pool, name, doc.ID)
	require.NoError(t, err)
	noop, err := data.B3Execute(ctx, "core", wire("op-fixture-000000002", current.Version, "processed", false), true)
	require.NoError(t, err)
	require.Equal(t, true, noop["noop"])
	read, err := data.B3Execute(ctx, "core", wire("op-fixture-000000003", 0, "", true), false)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"review_state": "processed"}, read["values"])
	answers := make(chan error, 2)
	for _, op := range []string{"op-fixture-000000004", "op-fixture-000000005"} {
		req := wire(op, current.Version, op, false)
		go func() { _, e := data.B3Execute(ctx, "core", req, true); answers <- e }()
	}
	e1, e2 := <-answers, <-answers
	if e1 == nil {
		require.Equal(t, 409, mustAE(t, e2).HTTP)
	} else {
		require.NoError(t, e2)
		require.Equal(t, 409, mustAE(t, e1).HTTP)
	}
	var receipts, facts int
	require.NoError(t, pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM _al_b3_operation_receipts),(SELECT count(*) FROM _al_b3_outbox)").Scan(&receipts, &facts))
	require.Equal(t, 3, receipts)
	require.Equal(t, 3, facts)
	current, err = repository.GetDocByID(ctx, pool, name, doc.ID)
	require.NoError(t, err)
	// Fail the final receipt write, after the document and source fact were updated.
	_, err = pool.Exec(ctx, `ALTER TABLE _al_b3_operation_receipts ADD CONSTRAINT fixture_receipt_failure CHECK (operation_id <> 'op-fixture-rollback1' OR document_revision IS NULL)`)
	require.NoError(t, err)
	_, err = data.B3Execute(ctx, "core", wire("op-fixture-rollback1", current.Version, "rollback", false), true)
	require.Error(t, err)
	checked, err := repository.GetDocByID(ctx, pool, name, doc.ID)
	require.NoError(t, err)
	require.Equal(t, current.Version, checked.Version)
	require.Equal(t, current.Data, checked.Data)
	require.NoError(t, pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM _al_b3_operation_receipts),(SELECT count(*) FROM _al_b3_outbox)").Scan(&receipts, &facts))
	require.Equal(t, 3, receipts)
	require.Equal(t, 3, facts)
	_, err = data.B3Execute(ctx, "producer", raw, true)
	require.Equal(t, 403, mustAE(t, err).HTTP)
	t.Run("write target misses have the same response", func(t *testing.T) {
		_, outsideErr := data.B3Execute(ctx, "core", wireFor(hidden.ID, "op-fixture-scope-miss", hidden.Version, "new", false), true)
		_, absentErr := data.B3Execute(ctx, "core", wireFor(hidden.ID+100, "op-fixture-scope-miss", hidden.Version, "new", false), true)
		outside, absent := mustAE(t, outsideErr), mustAE(t, absentErr)
		require.Equal(t, 404, outside.HTTP)
		require.Equal(t, outside, absent)
	})
	t.Run("type-wide uniqueness does not identify the conflicting row", func(t *testing.T) {
		_, err := data.B3Execute(ctx, "core", wire("op-fixture-duplicate", current.Version, "reserved", false), true)
		conflict := mustAE(t, err)
		require.Equal(t, 409, conflict.HTTP)
		require.Empty(t, conflict.Detail)
	})
	require.NoError(t, pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM _al_b3_operation_receipts),(SELECT count(*) FROM _al_b3_outbox)").Scan(&receipts, &facts))
	require.Equal(t, 3, receipts)
	require.Equal(t, 3, facts)
	_, err = pool.Exec(ctx, `UPDATE b3_reviews SET data=jsonb_set(data,'{scope}','"team-b"') WHERE id=$1`, doc.ID)
	require.NoError(t, err)
	_, err = data.B3Execute(ctx, "core", raw, true)
	require.Equal(t, 404, mustAE(t, err).HTTP)
	_, outsideErr := data.B3LookupReceipt(ctx, "core", originalLookup)
	require.Equal(t, 404, mustAE(t, outsideErr).HTTP)
	_, err = pool.Exec(ctx, `DELETE FROM b3_reviews WHERE id=$1`, doc.ID)
	require.NoError(t, err)
	_, absentErr := data.B3LookupReceipt(ctx, "core", originalLookup)
	require.Equal(t, mustAE(t, outsideErr), mustAE(t, absentErr))
}
