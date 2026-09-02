package cockroach7504

import (
	"sync"
	"testing"
)

const tableSize = 1

func MakeCacheKey(lease *LeaseState) int {
	return lease.id
}

type LeaseState struct {
	mu sync.Mutex
	id int
}
type LeaseSet struct {
	data []*LeaseState
}

func (l *LeaseSet) insert(s *LeaseState) {
	s.id = len(l.data)
	l.data = append(l.data, s)
}
func (l *LeaseSet) find(id int) *LeaseState {
	return l.data[id]
}
func (l *LeaseSet) remove(s *LeaseState) {
	for i := 0; i < len(l.data); i++ {
		if s == l.data[i] {
			l.data = append(l.data[:i], l.data[i+1:]...)
			break
		}
	}
}

type tableState struct {
	tableNameCache *tableNameCache
	mu             sync.Mutex
	active         *LeaseSet
}

func (t *tableState) release(lease *LeaseState) {
	t.mu.Lock()
	defer t.mu.Unlock()

	s := t.active.find(MakeCacheKey(lease))
	s.mu.Lock()
	defer s.mu.Unlock()

	t.removeLease(s)
}
func (t *tableState) removeLease(lease *LeaseState) {
	t.active.remove(lease)
	t.tableNameCache.remove(lease)
}

type tableNameCache struct {
	mu     sync.Mutex
	tables map[int]*LeaseState
}

func (c *tableNameCache) get(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lease, ok := c.tables[id]
	if !ok {
		return
	}
	if lease == nil {
		panic("nil lease in name cache")
	}

	lease.mu.Lock()
	defer lease.mu.Unlock()

}

func (c *tableNameCache) remove(lease *LeaseState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := MakeCacheKey(lease)
	existing, ok := c.tables[key]
	if !ok {
		return
	}
	if existing == lease {
		delete(c.tables, key)
	}

}

type LeaseManager struct {
	_          [64]byte
	mu         sync.Mutex
	tableNames *tableNameCache
	tables     map[int]*tableState
}

func (m *LeaseManager) AcquireByName(id int) {
	m.tableNames.get(id)
}

func (m *LeaseManager) findTableState(lease *LeaseState) *tableState {
	existing, ok := m.tables[0]
	if !ok {
		return nil
	}
	return existing
}

func (m *LeaseManager) Release(lease *LeaseState) {
	t := m.findTableState(lease)
	t.release(lease)
}
func NewLeaseManager(tname *tableNameCache, ts *tableState) *LeaseManager {
	mgr := &LeaseManager{
		tableNames: tname,
		tables:     make(map[int]*tableState),
	}
	mgr.tables[0] = ts
	return mgr
}
func NewLeaseSet(n int) *LeaseSet {
	lset := &LeaseSet{}
	for i := 0; i < n; i++ {
		lease := new(LeaseState)
		lset.data = append(lset.data, lease)
	}
	return lset
}

func TestCockroach7504(t *testing.T) {
	leaseNum := 2
	lset := NewLeaseSet(leaseNum)

	nc := &tableNameCache{
		tables: make(map[int]*LeaseState),
	}
	for i := 0; i < leaseNum; i++ {
		nc.tables[i] = lset.find(i)
	}

	ts := &tableState{
		tableNameCache: nc,
		active:         lset,
	}

	mgr := NewLeaseManager(nc, ts)
	var wg sync.WaitGroup

	wg.Add(2)

	go func() {

		mgr.AcquireByName(0)
		wg.Done()
	}()

	go func() {

		mgr.Release(lset.find(0))
		wg.Done()
	}()

	wg.Wait()
}
