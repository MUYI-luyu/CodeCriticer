








package cockroach13197

import (
	"context"
	"testing"
)

type DB struct{}

func (db *DB) begin(ctx context.Context) *Tx {
	ctx, cancel := context.WithCancel(ctx)
	tx := &Tx{
		cancel: cancel,
		ctx:    ctx,
	}
	go tx.awaitDone()
	return tx
}

type Tx struct {
	cancel context.CancelFunc
	ctx    context.Context
}

func (tx *Tx) awaitDone() {
	<-tx.ctx.Done()
}

func (tx *Tx) Rollback() {
	tx.rollback()
}

func (tx *Tx) rollback() {
	tx.close()
}

func (tx *Tx) close() {
	tx.cancel()
}







func TestCockroach13197(t *testing.T) {
	db := &DB{}
	db.begin(context.Background())
}
