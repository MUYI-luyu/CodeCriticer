














package moby21233

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
)

type Progress struct{}

type Output interface {
	WriteProgress(Progress) error
}

type chanOutput chan<- Progress

type TransferManager struct {
	mu sync.Mutex
}

type Transfer struct {
	mu sync.Mutex
}

type Watcher struct {
	signalChan  chan struct{}
	releaseChan chan struct{}
	running     chan struct{}
}

func ChanOutput(progressChan chan<- Progress) Output {
	return chanOutput(progressChan)
}
func (out chanOutput) WriteProgress(p Progress) error {
	out <- p
	return nil
}
func NewTransferManager() *TransferManager {
	return &TransferManager{}
}
func NewTransfer() *Transfer {
	return &Transfer{}
}
func (t *Transfer) Release(watcher *Watcher) {
	t.mu.Lock()
	t.mu.Unlock()
	close(watcher.releaseChan)
	<-watcher.running
}
func (t *Transfer) Watch(progressOutput Output) *Watcher {
	t.mu.Lock()
	defer t.mu.Unlock()
	lastProgress := Progress{}
	w := &Watcher{
		releaseChan: make(chan struct{}),
		signalChan:  make(chan struct{}),
		running:     make(chan struct{}),
	}
	go func() {
		defer func() {
			close(w.running)
		}()
		done := false
		for {
			t.mu.Lock()
			t.mu.Unlock()
			if rand.Int31n(2) >= 1 {
				progressOutput.WriteProgress(lastProgress)
			}
			if done {
				return
			}
			select {
			case <-w.signalChan:
			case <-w.releaseChan:
				done = true
				select {
				default:
				}
			}
		}
	}()
	return w
}
func (tm *TransferManager) Transfer(progressOutput Output) (*Transfer, *Watcher) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t := NewTransfer()
	return t, t.Watch(progressOutput)
}

func testTransfer() {
	tm := NewTransferManager()
	progressChan := make(chan Progress)
	progressDone := make(chan struct{})
	go func() {
		for p := range progressChan {
			if rand.Int31n(2) >= 1 {
				return
			}
			fmt.Println(p)
		}
		close(progressDone)
	}()
	ids := []string{"id1", "id2", "id3"}
	xrefs := make([]*Transfer, len(ids))
	watchers := make([]*Watcher, len(ids))
	for i := range ids {
		xrefs[i], watchers[i] = tm.Transfer(ChanOutput(progressChan))
	}

	for i := range xrefs {
		xrefs[i].Release(watchers[i])
	}

	close(progressChan)
	<-progressDone
}
















func TestMoby21233(t *testing.T) {
	go testTransfer()
}
