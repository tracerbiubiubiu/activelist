package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/validation"
)

// B3Execute resolves the caller from the route's verified service identity.
// Read observations are persisted by Core. Only a mutation reserves an AL receipt.
func (s *DataService) B3Execute(ctx context.Context, caller string, raw []byte, write bool) (map[string]any, error) {
	if caller == "" || caller != s.b3.CoreAK {
		return nil, b3Forbidden()
	}
	req, err := s.decodeB3Command(raw, write, time.Now())
	if err != nil {
		return nil, err
	}
	c := req.Command
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, b3Dependency()
	}
	defer tx.Rollback(ctx)
	if err = setLockTimeout(ctx, tx); err != nil {
		return nil, err
	}
	var original b3StoredReceipt
	if write {
		original, err = reserveB3Receipt(ctx, tx, c)
		if err != nil {
			return nil, err
		}
	}
	generation, err := s.lockB3Stream(ctx, tx, c.Target.TypeName)
	if err != nil {
		return nil, err
	}
	if strconv.FormatInt(generation, 10) != c.Target.Generation {
		return nil, b3Conflict()
	}
	documentID, _ := strconv.ParseInt(c.Target.ResourceID, 10, 64)
	doc, err := s.b3Document(ctx, tx, c.Target.TypeName, documentID, true)
	if err != nil {
		return nil, err
	}
	if doc.Status != repository.RowStatusActive {
		return nil, b3Invisible()
	}
	if !req.live(time.Now()) {
		return nil, b3Forbidden()
	}
	result := map[string]any{"type_name": c.Target.TypeName, "generation": c.Target.Generation, "document_id": c.Target.ResourceID, "document_revision": strconv.FormatInt(doc.Version, 10)}
	switch {
	case !write:
		values := map[string]any{}
		for _, key := range c.Params.Fields {
			if v, ok := doc.Data[key]; ok {
				if !validB3EventValue(v) {
					return nil, b3BadCommand()
				}
				values[key] = v
			}
		}
		raw, err := canonicalB3JSON(values)
		if err != nil || len(raw) > 32<<10 {
			return nil, b3BadCommand()
		}
		result["values"] = values
		result["queried_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	case original.revision != nil:
		// Do not confirm another object's receipt using a grant issued for this row.
		if original.typeName != c.Target.TypeName || original.documentID != doc.ID || original.generation != generation {
			return nil, b3Invisible()
		}
		if original.binding != b3ReceiptBinding(c) {
			return nil, b3Conflict()
		}
		result["document_revision"] = strconv.FormatInt(*original.revision, 10)
		result["noop"] = *original.noop
	default:
		version, noop, err := s.applyB3Patch(ctx, tx, doc, c)
		if err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, `UPDATE _al_b3_operation_receipts SET document_revision=$2,noop=$3 WHERE operation_id=$1 AND document_revision IS NULL`, c.OperationID, version, noop)
		if err != nil || tag.RowsAffected() != 1 {
			return nil, b3Dependency()
		}
		result["document_revision"] = strconv.FormatInt(version, 10)
		result["noop"] = noop
	}
	// Lock waits and trigger/constraint work consume the short admission window.
	if !req.live(time.Now()) {
		return nil, b3Forbidden()
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, b3Dependency()
	}
	return result, nil
}

func b3Invisible() error { return apperr.New(404, apperr.CodeDataNotFound, "document unavailable") }

func (s *DataService) b3Document(ctx context.Context, tx pgx.Tx, typeName string, id int64, forUpdate bool) (*repository.Document, error) {
	doc, err := repository.GetScopedDoc(ctx, tx, typeName, id, s.b3.AnchorField, s.b3.AnchorValue, forUpdate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, b3Invisible()
	}
	return doc, err
}

type b3StoredReceipt struct {
	binding, typeName      string
	generation, documentID int64
	revision               *int64
	noop                   *bool
}

func b3ReceiptBinding(c b3WireCommand) string {
	return b3Hash(map[string]any{"input_hash": c.Hash, "invocation_id": c.InvocationID, "principal": b3AsMap(c.Principal), "causality": b3AsMap(c.Cause)})
}

func reserveB3Receipt(ctx context.Context, tx pgx.Tx, c b3WireCommand) (b3StoredReceipt, error) {
	var old b3StoredReceipt
	documentID, _ := strconv.ParseInt(c.Target.ResourceID, 10, 64)
	generation, _ := strconv.ParseInt(c.Target.Generation, 10, 64)
	_, err := tx.Exec(ctx, `INSERT INTO _al_b3_operation_receipts(operation_id,binding_sha256,target_type,document_id,generation) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (operation_id) DO NOTHING`, c.OperationID, b3ReceiptBinding(c), c.Target.TypeName, documentID, generation)
	if err != nil {
		return old, b3Dependency()
	}
	err = tx.QueryRow(ctx, `SELECT binding_sha256,target_type,document_id,generation,document_revision,noop FROM _al_b3_operation_receipts WHERE operation_id=$1 FOR UPDATE`, c.OperationID).Scan(&old.binding, &old.typeName, &old.documentID, &old.generation, &old.revision, &old.noop)
	if err != nil {
		return old, b3Dependency()
	}
	return old, nil
}

func (s *DataService) applyB3Patch(ctx context.Context, tx pgx.Tx, doc *repository.Document, c b3WireCommand) (int64, bool, error) {
	expected, _ := strconv.ParseInt(c.Params.ExpectedRevision, 10, 64)
	if doc.Version != expected {
		return 0, false, errVersionMismatch(expected, doc.Version)
	}
	def, err := meta.GetByName(ctx, tx, c.Target.TypeName)
	if err != nil {
		return 0, false, err
	}
	if def.Status != meta.StatusActive {
		return 0, false, b3Conflict()
	}
	merged := mergeData(doc.Data, c.Params.Patch)
	if e := validation.ValidateData(def.Fields, merged); e != nil {
		return 0, false, mapDataValidationError(ctx, tx, e, def, true)
	}
	changed := b3Changed(doc.Data, merged, s.b3.WriteFields)
	if len(changed) == 0 {
		return doc.Version, true, nil
	}
	if err := s.checkUniqueDup(ctx, tx, def, merged, doc.ID); err != nil {
		return 0, false, err
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return 0, false, b3BadCommand()
	}
	updated, err := repository.UpdateDocData(ctx, tx, c.Target.TypeName, doc.ID, expected, raw, c.Principal.ID)
	if err != nil {
		return 0, false, err
	}
	if err := s.appendB3Fact(ctx, tx, updated, c.Target.TypeName, "al.document.updated.v1", changed, c.OperationID, b3AsMap(c.Cause)); err != nil {
		return 0, false, err
	}
	return updated.Version, false, nil
}

// B3LookupReceipt runs in a read-only snapshot. It cannot reserve, retry or
// change a document, including when the original effect never committed.
func (s *DataService) B3LookupReceipt(ctx context.Context, caller string, raw []byte) (map[string]any, error) {
	if caller == "" || caller != s.b3.CoreAK {
		return nil, b3Forbidden()
	}
	req, err := s.decodeB3Lookup(raw, time.Now())
	if err != nil {
		return nil, err
	}
	c := req.Command
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, b3Dependency()
	}
	defer tx.Rollback(ctx)
	var old b3StoredReceipt
	err = tx.QueryRow(ctx, `SELECT binding_sha256,target_type,document_id,generation,document_revision,noop FROM _al_b3_operation_receipts WHERE operation_id=$1 AND document_revision IS NOT NULL`, c.OperationID).Scan(&old.binding, &old.typeName, &old.documentID, &old.generation, &old.revision, &old.noop)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, b3Invisible()
	}
	if err != nil {
		return nil, b3Dependency()
	}
	if c.Target.ResourceID != strconv.FormatInt(old.documentID, 10) || c.Target.TypeName != old.typeName || c.Target.Generation != strconv.FormatInt(old.generation, 10) {
		return nil, b3Invisible()
	}
	doc, err := s.b3Document(ctx, tx, s.b3.TypeName, old.documentID, false)
	if err != nil {
		return nil, err
	}
	if doc.Status != repository.RowStatusActive {
		return nil, b3Invisible()
	}
	if b3ReceiptBinding(c) != old.binding {
		return nil, b3Conflict()
	}
	if !req.live(time.Now()) {
		return nil, b3Forbidden()
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, b3Dependency()
	}
	return map[string]any{"type_name": old.typeName, "document_id": c.Target.ResourceID, "generation": c.Target.Generation, "document_revision": strconv.FormatInt(*old.revision, 10), "noop": *old.noop}, nil
}
