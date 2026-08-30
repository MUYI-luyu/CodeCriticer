






package etcd6857

import (
	"testing"
)

type Status struct{}

type node struct {
	status chan chan Status
	stop   chan struct{}
	done   chan struct{}
}

func (n *node) Status() Status {
	c := make(chan Status)
	n.status <- c
	return <-c
}

func (n *node) run() {
	for {
		select {
		case c := <-n.status:
			c <- Status{}
		case <-n.stop:
			close(n.done)
			return
		}
	}
}

func (n *node) Stop() {
	select {
	case n.stop <- struct{}{}:
	case <-n.done:
		return
	}
	<-n.done
}

func NewNode() *node {
	return &node{
		status: make(chan chan Status),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}
















func TestEtcd6857(t *testing.T) {
	n := NewNode()
	go n.run()
	go n.Status()
	go n.Stop()
}
