package grpc862

import (
	"context"
	"testing"
	"time"
)

type ClientConn struct {
	ctx    context.Context
	cancel context.CancelFunc
	conns  []*addrConn
}

func (cc *ClientConn) Close() {
	cc.cancel()
	conns := cc.conns
	cc.conns = nil
	for _, ac := range conns {
		ac.tearDown()
	}
}

func (cc *ClientConn) resetAddrConn() {
	ac := &addrConn{
		cc: cc,
	}
	cc.conns = append(cc.conns, ac)
	ac.ctx, ac.cancel = context.WithCancel(cc.ctx)
	ac.resetTransport()
}

type addrConn struct {
	cc     *ClientConn
	ctx    context.Context
	cancel context.CancelFunc
}

func (ac *addrConn) resetTransport() {
	for retries := 1; ; retries++ {
		sleepTime := 2 * time.Nanosecond * time.Duration(retries)
		timeout := 10 * time.Nanosecond
		_, cancel := context.WithTimeout(ac.ctx, timeout)
		connectTime := time.Now()
		cancel()
		select {
		case <-time.After(sleepTime - time.Since(connectTime)):
		case <-ac.ctx.Done():
			return
		}
	}
}

func (ac *addrConn) tearDown() {
	ac.cancel()
}

func DialContext(ctx context.Context) (conn *ClientConn) {
	cc := &ClientConn{}
	cc.ctx, cc.cancel = context.WithCancel(context.Background())
	defer func() {
		select {
		case <-ctx.Done():
			if conn != nil {
				conn.Close()
			}
			conn = nil
		default:
		}

	}()
	go func() {
		cc.resetAddrConn()
	}()
	return conn
}

func TestGrpc862(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go DialContext(ctx)
	go cancel()
}
