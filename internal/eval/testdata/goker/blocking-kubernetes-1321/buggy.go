package kubernetes1321

import (
	"sync"
	"testing"
)

var globalMtx sync.Mutex

type muxWatcher struct {
	result chan struct{}
	m      *Mux
	id     int64
}

func (mw *muxWatcher) Stop() {
	mw.m.stopWatching(mw.id)
}

type Mux struct {
	lock     sync.Mutex
	watchers map[int64]*muxWatcher
}

func NewMux() *Mux {
	m := &Mux{
		watchers: map[int64]*muxWatcher{},
	}
	go m.loop()
	return m
}

func (m *Mux) Watch() *muxWatcher {
	mw := &muxWatcher{
		result: make(chan struct{}),
		m:      m,
		id:     int64(len(m.watchers)),
	}
	globalMtx.Lock()
	m.watchers[mw.id] = mw
	globalMtx.Unlock()
	return mw
}

func (m *Mux) loop() {
	for i := 0; i < 100; i++ {
		m.distribute()
	}
}

func (m *Mux) distribute() {
	m.lock.Lock()
	defer m.lock.Unlock()
	globalMtx.Lock()
	for _, w := range m.watchers {
		w.result <- struct{}{}
	}
	globalMtx.Unlock()
}

func (m *Mux) stopWatching(id int64) {
	m.lock.Lock()
	defer m.lock.Unlock()
	w, ok := m.watchers[id]
	if !ok {
		return
	}
	delete(m.watchers, id)
	close(w.result)
}

func testMuxWatcherClose() {
	m := NewMux()
	w := m.Watch()
	w.Stop()
}

func TestKubernetes1321(t *testing.T) {
	go testMuxWatcherClose()
}
