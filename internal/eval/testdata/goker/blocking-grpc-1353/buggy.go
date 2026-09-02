package grpc1353

import (
	"sync"
	"testing"
)

var HelpCh chan struct{}

type Balancer interface {
	Start()
	Up() func()
	Notify() <-chan bool
	Close()
}

type roundRobin struct {
	mu     sync.Mutex
	addrCh chan bool
}

func (rr *roundRobin) Start() {
	rr.addrCh = make(chan bool)
	go func() {
		for i := 0; i < 100; i++ {
			rr.watchAddrUpdates()
		}
		close(HelpCh)
	}()
}

func (rr *roundRobin) Up() func() {
	return func() {
		rr.down()
	}
}

func (rr *roundRobin) Notify() <-chan bool {
	return rr.addrCh
}

func (rr *roundRobin) Close() {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.addrCh != nil {
		close(rr.addrCh)
	}
}

func (rr *roundRobin) watchAddrUpdates() {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	rr.addrCh <- true
}

func (rr *roundRobin) down() {
	rr.mu.Lock()
	defer rr.mu.Unlock()
}

type addrConn struct {
	mu   sync.Mutex
	down func()
}

func (ac *addrConn) tearDown() {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if ac.down != nil {
		ac.down()
	}
}

type dialOptions struct {
	balancer Balancer
}

type ClientConn struct {
	dopts dialOptions
	conns []*addrConn
}

func (cc *ClientConn) lbWatcher() {
	for addr := range cc.dopts.balancer.Notify() {
		if addr {

		}
		var del []*addrConn
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
			&roundRobin{},
		},
	}
	ac1 := &addrConn{
		down: cc.dopts.balancer.Up(),
	}
	ac2 := &addrConn{
		down: cc.dopts.balancer.Up(),
	}
	cc.conns = append(cc.conns, ac1, ac2)
	return cc
}

func TestGrpc1353(t *testing.T) {
	HelpCh = make(chan struct{})
	cc := NewClientConn()
	cc.dopts.balancer.Start()
	go cc.lbWatcher()
	go func() {
		<-HelpCh
		cc.dopts.balancer.Close()
	}()
}
