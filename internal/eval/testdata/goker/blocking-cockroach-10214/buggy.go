package cockroach10214

import (
	"sync"
	"testing"
	"unsafe"
)

type Store struct {
	coalescedMu struct {
		sync.Mutex
		heartbeatResponses []int
	}
	mu struct {
		replicas map[int]*Replica
	}
}

func (s *Store) sendQueuedHeartbeats() {
	s.coalescedMu.Lock()
	defer s.coalescedMu.Unlock()
	for i := 0; i < len(s.coalescedMu.heartbeatResponses); i++ {
		s.sendQueuedHeartbeatsToNode()
	}
}

func (s *Store) sendQueuedHeartbeatsToNode() {
	for i := 0; i < len(s.mu.replicas); i++ {
		r := s.mu.replicas[i]
		r.reportUnreachable()
	}
}

type Replica struct {
	raftMu sync.Mutex
	mu     sync.Mutex
	store  *Store
}

func (r *Replica) reportUnreachable() {
	r.raftMu.Lock()

	defer r.raftMu.Unlock()

}

func (r *Replica) tick() {
	r.raftMu.Lock()
	defer r.raftMu.Unlock()
	r.tickRaftMuLocked()

}

func (r *Replica) tickRaftMuLocked() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maybeQuiesceLocked() {
		return
	}
}
func (r *Replica) maybeQuiesceLocked() bool {
	for i := 0; i < 2; i++ {
		if !r.maybeCoalesceHeartbeat() {
			return true
		}
	}
	return false
}
func (r *Replica) maybeCoalesceHeartbeat() bool {
	msgtype := uintptr(unsafe.Pointer(r)) % 3
	switch msgtype {
	case 0, 1, 2:
		r.store.coalescedMu.Lock()
	default:
		return false
	}
	r.store.coalescedMu.Unlock()
	return true
}

func TestCockroach10214(t *testing.T) {
	store := &Store{}
	responses := &store.coalescedMu.heartbeatResponses
	*responses = append(*responses, 1, 2)
	store.mu.replicas = make(map[int]*Replica)

	rp1 := &Replica{
		store: store,
	}
	rp2 := &Replica{
		store: store,
	}
	store.mu.replicas[0] = rp1
	store.mu.replicas[1] = rp2

	go func() {
		store.sendQueuedHeartbeats()
	}()

	go func() {
		rp1.tick()
	}()
}
