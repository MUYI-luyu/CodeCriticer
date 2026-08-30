










package cockroach3710

import (
	"sync"
	"testing"
	"unsafe"
)

type Store struct {
	raftLogQueue *baseQueue
	replicas     map[int]*Replica

	mu struct {
		sync.RWMutex
	}
}

func (s *Store) ForceRaftLogScanAndProcess() {
	s.mu.RLock()
	for _, r := range s.replicas {
		s.raftLogQueue.MaybeAdd(r)
	}
	s.mu.RUnlock()
}

func (s *Store) RaftStatus() {
	s.mu.RLock()
	defer s.mu.RUnlock()
}

func (s *Store) processRaft() {
	go func() {
		for {
			var replicas []*Replica
			s.mu.Lock()
			for _, r := range s.replicas {
				replicas = append(replicas, r)
			}
			s.mu.Unlock()
			break
		}
	}()
}

type Replica struct {
	store *Store
}

type baseQueue struct {
	sync.Mutex
	impl *raftLogQueue
}

func (bq *baseQueue) MaybeAdd(repl *Replica) {
	bq.Lock()
	defer bq.Unlock()
	bq.impl.shouldQueue(repl)
}

type raftLogQueue struct{}

func (*raftLogQueue) shouldQueue(r *Replica) {
	getTruncatableIndexes(r)
}

func getTruncatableIndexes(r *Replica) {
	r.store.RaftStatus()
}

func NewStore() *Store {
	rlq := &raftLogQueue{}
	bq := &baseQueue{impl: rlq}
	store := &Store{
		raftLogQueue: bq,
		replicas:     make(map[int]*Replica),
	}
	r1 := &Replica{store}
	r2 := &Replica{store}

	makeKey := func(r *Replica) int {
		return int((uintptr(unsafe.Pointer(r)) >> 1) % 7)
	}
	store.replicas[makeKey(r1)] = r1
	store.replicas[makeKey(r2)] = r2

	return store
}












func TestCockroach3710(t *testing.T) {
	store := NewStore()
	go store.ForceRaftLogScanAndProcess()
	go store.processRaft()
}
