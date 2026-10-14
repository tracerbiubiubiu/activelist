// InsertImportBatch 单测（2026-10-14 契约审计 B2 护栏）：Data nil 归一空对象——
// 假 dbtx 捕获批队列参数断言落库形态，纯内存无需 PG。
package repository

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// captureTx dbtx 桩：仅 SendBatch 被消费——捕获批队列供断言。
type captureTx struct{ batch *pgx.Batch }

func (t *captureTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (t *captureTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (t *captureTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (t *captureTx) SendBatch(_ context.Context, b *pgx.Batch) pgx.BatchResults {
	t.batch = b
	return batchResultsFake{}
}

type batchResultsFake struct{}

func (batchResultsFake) Exec() (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil }
func (batchResultsFake) Query() (pgx.Rows, error)         { return nil, nil }
func (batchResultsFake) QueryRow() pgx.Row                { return nil }
func (batchResultsFake) Close() error                     { return nil }

func TestInsertImportBatch_NilDataNormalized(t *testing.T) {
	tx := &captureTx{}
	docs := []Document{
		{ID: 1, Status: RowStatusActive, Data: nil},
		{ID: 2, Status: RowStatusActive, Data: map[string]any{"name": "x"}},
	}
	require.NoError(t, InsertImportBatch(context.Background(), tx, "t_cap", docs))
	require.NotNil(t, tx.batch)
	require.Len(t, tx.batch.QueuedQueries, 2)
	// data 形参位（$3）= Arguments[2]：nil 行归一 {}，非 nil 行原样序列化
	require.Equal(t, []byte("{}"), tx.batch.QueuedQueries[0].Arguments[2])
	require.Contains(t, string(tx.batch.QueuedQueries[1].Arguments[2].([]byte)), `"name":"x"`)
}
