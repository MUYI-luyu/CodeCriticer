package grpc1424

import (
	"sync"
	"testing"
)

type Balancer interface {
	Notify() <-chan bool
}

type roundRobin struct {
	mu     sync.Mutex
	addrCh chan bool
}

func (rr *roundRobin) Notify() <-chan bool {
	return rr.addrCh
}

type addrConn struct {
	mu sync.Mutex
}

func (ac *addrConn) tearDown() {
	ac.mu.Lock()
	defer ac.mu.Unlock()
}

type dialOptions struct {
	balancer Balancer
}

type ClientConn struct {
	dopts dialOptions
	conns []*addrConn
}

func (cc *ClientConn) lbWatcher(doneChan chan bool) {
	for addr := range cc.dopts.balancer.Notify() {
		if addr {

		}
		var (
			del []*addrConn
		)
		for _, a := range cc.conns {
			del = append(del, a)
		}
		for _, c := range del {
			c.tearDown()
		}

	}
}

func NewClientConn() *ClientConn {
	cc := &ClientConn{
		dopts: dialOptions{
			&roundRobin{addrCh: make(chan bool)},
		},
	}
	return cc
}

func DialContext() {
	cc := NewClientConn()
	waitC := make(chan error, 1)
	go func() {
		defer close(waitC)
		ch := cc.dopts.balancer.Notify()
		if ch != nil {
			doneChan := make(chan bool)
			go cc.lbWatcher(doneChan)
			<-doneChan
		}
	}()

	close(cc.dopts.balancer.(*roundRobin).addrCh)
}

func TestGrpc1424(t *testing.T) {
	go DialContext()
}
