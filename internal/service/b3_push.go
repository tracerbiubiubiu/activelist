package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/tracerbiubiubiu/zhuzhao-utils/aksk"
)

var errB3Push = errors.New("ActiveList B3 receiver did not confirm the leased event")

var b3HTTPClient = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

// RunB3Push delivers one committed event at a time and stops with the app.
func (s *DataService) RunB3Push(ctx context.Context, logger *slog.Logger) {
	if s.b3.TypeName == "" {
		return
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := s.PushNextB3(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("ActiveList B3 delivery pending", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// PushNextB3 claims just the next unacknowledged cursor. An interrupted lease
// expires in PostgreSQL and the same immutable source fact is sent again.
func (s *DataService) PushNextB3(ctx context.Context) error {
	if s.b3.TypeName == "" {
		return nil
	}
	var cursor, lease int64
	var eventID, hash string
	var event []byte
	err := s.pool.QueryRow(ctx, `
		UPDATE _al_b3_outbox SET attempts=attempts+1,lease_until=clock_timestamp()+INTERVAL '15 seconds'
		WHERE cursor=(SELECT ack_cursor+1 FROM _al_b3_stream)
		  AND stage_commit_ref IS NULL AND next_attempt_at <= clock_timestamp()
		  AND (lease_until IS NULL OR lease_until <= clock_timestamp())
		RETURNING cursor, attempts, event_id, event, payload_sha256`).Scan(&cursor, &lease, &eventID, &event, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return b3Dependency()
	}
	stage, err := s.deliverB3(ctx, eventID, hash, event)
	if err != nil {
		if _, retryErr := s.pool.Exec(ctx, `
			UPDATE _al_b3_outbox SET lease_until=NULL,
			    next_attempt_at=clock_timestamp()+make_interval(secs=>LEAST(attempts*2,60)::double precision)
			WHERE cursor=$1 AND attempts=$2 AND stage_commit_ref IS NULL`, cursor, lease); retryErr != nil {
			return b3Dependency()
		}
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return b3Dependency()
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `
		UPDATE _al_b3_outbox SET stage_commit_ref=$3, lease_until=NULL
		WHERE cursor=$1 AND attempts=$2 AND stage_commit_ref IS NULL AND lease_until>clock_timestamp()`, cursor, lease, stage)
	if err != nil || result.RowsAffected() != 1 {
		return b3Dependency()
	}
	result, err = tx.Exec(ctx, "UPDATE _al_b3_stream SET ack_cursor=$1 WHERE ack_cursor=$1-1", cursor)
	if err != nil || result.RowsAffected() != 1 {
		return b3Dependency()
	}
	if err := tx.Commit(ctx); err != nil {
		return b3Dependency()
	}
	return nil
}

func (s *DataService) deliverB3(ctx context.Context, eventID, hash string, event []byte) (string, error) {
	deliveryID := b3RandomID("al-delivery-")
	body, err := canonicalB3JSON(map[string]any{
		"schema_version": "1", "consumer_id": s.b3.ConsumerID, "delivery_id": deliveryID,
		"event": json.RawMessage(event),
	})
	if err != nil {
		return "", errB3Push
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.b3.PushURL, bytes.NewReader(body))
	if err != nil {
		return "", errB3Push
	}
	req.Header.Set("Content-Type", "application/json")
	aksk.Sign(req, body, aksk.SignOptions{AK: s.b3.PushAK, SK: []byte(s.b3.PushSK), RequestID: deliveryID})
	resp, err := b3HTTPClient.Do(req)
	if err != nil {
		return "", errB3Push
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errB3Push
	}
	ack, err := readB3Ack(resp.Body)
	if err != nil || ack["schema_version"] != "1" || ack["state"] != "durably_staged" ||
		ack["delivery_id"] != deliveryID || ack["event_id"] != eventID ||
		ack["source"] != "urn:zhuzhao:activelist:"+s.b3.TypeName || ack["payload_sha256"] != hash ||
		!b3Identifier.MatchString(ack["stage_commit_ref"]) {
		return "", errB3Push
	}
	return ack["stage_commit_ref"], nil
}

func readB3Ack(r io.Reader) (map[string]string, error) {
	body, err := io.ReadAll(io.LimitReader(r, 4097))
	if err != nil || len(body) > 4096 {
		return nil, errB3Push
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errB3Push
	}
	values := make(map[string]string, 7)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, errB3Push
		}
		name, ok := key.(string)
		if !ok || len(values) >= 7 {
			return nil, errB3Push
		}
		if _, duplicate := values[name]; duplicate {
			return nil, errB3Push
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, errB3Push
		}
		values[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errB3Push
	}
	if _, err := decoder.Token(); err != io.EOF || len(values) != 7 {
		return nil, errB3Push
	}
	return values, nil
}
