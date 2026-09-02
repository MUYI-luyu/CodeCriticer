package etcd7902

import (
	"sync"
	"testing"
)

type roundClient struct {
	progress int
	acquire  func()
	validate func()
	release  func()
}

func runElectionFunc() {
	rcs := make([]roundClient, 3)
	nextc := make(chan bool)
	for i := range rcs {
		var rcNextc chan bool
		setRcNextc := func() {
			rcNextc = nextc
		}
		rcs[i].acquire = func() {}
		rcs[i].validate = func() {
			setRcNextc()
		}
		rcs[i].release = func() {
			if i == 0 {
				close(nextc)
				nextc = make(chan bool)
			}
			<-rcNextc
		}
	}
	doRounds(rcs, 100)
}
func doRounds(rcs []roundClient, rounds int) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(rcs))
	for i := range rcs {
		go func(rc *roundClient) {
			defer wg.Done()
			for rc.progress < rounds || rounds <= 0 {
				rc.acquire()
				mu.Lock()
				rc.validate()
				mu.Unlock()
				rc.progress++
				mu.Lock()
				rc.release()
				mu.Unlock()
			}
		}(&rcs[i])
	}
	wg.Wait()
}

func TestEtcd7902(t *testing.T) {
	go runElectionFunc()
}
