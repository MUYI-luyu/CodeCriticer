package kubernetes62464

import (
	"math/rand"
	"sync"
	"testing"
)

type State interface {
	GetCPUSetOrDefault()
	GetCPUSet() bool
	GetDefaultCPUSet()
	SetDefaultCPUSet()
}

type stateMemory struct {
	sync.RWMutex
}

func (s *stateMemory) GetCPUSetOrDefault() {
	s.RLock()
	defer s.RUnlock()
	if ok := s.GetCPUSet(); ok {
		return
	}
	s.GetDefaultCPUSet()
}

func (s *stateMemory) GetCPUSet() bool {
	s.RLock()
	defer s.RUnlock()

	if rand.Intn(10) > 5 {
		return true
	}
	return false
}

func (s *stateMemory) GetDefaultCPUSet() {
	s.RLock()
	defer s.RUnlock()
}

func (s *stateMemory) SetDefaultCPUSet() {
	s.Lock()
	defer s.Unlock()
}

type staticPolicy struct{}

func (p *staticPolicy) RemoveContainer(s State) {
	s.GetDefaultCPUSet()
	s.SetDefaultCPUSet()
}

type manager struct {
	state *stateMemory
}

func (m *manager) reconcileState() {
	m.state.GetCPUSetOrDefault()
}

func NewPolicyAndManager() (*staticPolicy, *manager) {
	s := &stateMemory{}
	m := &manager{s}
	p := &staticPolicy{}
	return p, m
}

func TestKubernetes62464(t *testing.T) {
	p, m := NewPolicyAndManager()
	go m.reconcileState()
	go p.RemoveContainer(m.state)
}
