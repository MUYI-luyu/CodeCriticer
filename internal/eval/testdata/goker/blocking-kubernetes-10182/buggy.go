










package kubernetes10182

import (
	"sync"
	"testing"
)

type statusManager struct {
	podStatusesLock  sync.RWMutex
	podStatusChannel chan bool
}

func (s *statusManager) Start() {
	go func() {
		for i := 0; i < 2; i++ {
			s.syncBatch()
		}
	}()
}

func (s *statusManager) syncBatch() {
	<-s.podStatusChannel
	s.DeletePodStatus()
}

func (s *statusManager) DeletePodStatus() {
	s.podStatusesLock.Lock()
	defer s.podStatusesLock.Unlock()
}

func (s *statusManager) SetPodStatus() {
	s.podStatusesLock.Lock()
	defer s.podStatusesLock.Unlock()
	s.podStatusChannel <- true
}

func NewStatusManager() *statusManager {
	return &statusManager{
		podStatusChannel: make(chan bool),
	}
}















func TestKubernetes10182(t *testing.T) {
	s := NewStatusManager()
	go s.Start()
	go s.SetPodStatus()
	go s.SetPodStatus()
}
