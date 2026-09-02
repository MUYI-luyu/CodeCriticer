package cockroach10790

import (
	"context"
	"sync"
	"testing"
)

type Stopper struct {
	quiescer chan struct{}
	mu       struct {
		sync.Mutex
		quiescing bool
	}
}

func (s *Stopper) ShouldQuiesce() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.quiescer
}

func (s *Stopper) Quiesce() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.mu.quiescing {
		s.mu.quiescing = true
		close(s.quiescer)
	}
}

func (s *Stopper) Stop() {
	s.Quiesce()
}

type Replica struct {
	chans   []chan bool
	stopper *Stopper
}

func (r *Replica) beginCmds(ctx context.Context) {
	ctxDone := ctx.Done()
	for _, ch := range r.chans {
		select {
		case <-ch:
		case <-ctxDone:
			go func() {
				for _, ch := range r.chans {
					<-ch
				}
			}()
		}
	}
}

func (r *Replica) sendChans(ctx context.Context) {
	for _, ch := range r.chans {
		select {
		case ch <- true:
		case <-ctx.Done():
			return
		}
	}
}

func NewReplica() *Replica {
	r := &Replica{
		stopper: &Stopper{
			quiescer: make(chan struct{}),
		},
	}
	r.chans = append(r.chans, make(chan bool))
	r.chans = append(r.chans, make(chan bool))
	return r
}

func TestCockroach10790(t *testing.T) {
	r := NewReplica()
	ctx, cancel := context.WithCancel(context.Background())
	go r.sendChans(ctx)
	go r.beginCmds(ctx)
	go cancel()
	r.stopper.Stop()
}
