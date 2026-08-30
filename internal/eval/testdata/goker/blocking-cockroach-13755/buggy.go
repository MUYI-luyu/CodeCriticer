










package cockroach13755

import (
	"context"
	"testing"
)

type Rows struct {
	cancel context.CancelFunc
}

func (rs *Rows) initContextClose(ctx context.Context) {
	ctx, rs.cancel = context.WithCancel(ctx)
	go rs.awaitDone(ctx)
}

func (rs *Rows) awaitDone(ctx context.Context) {
	<-ctx.Done()
	rs.close(ctx.Err())
}

func (rs *Rows) close(err error) {

}







func TestCockroach13755(t *testing.T) {
	rs := &Rows{}
	rs.initContextClose(context.Background())

}
