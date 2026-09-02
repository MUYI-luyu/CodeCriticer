package kubernetes58107

import (
	"sync"
	"testing"
	"time"
)

type RateLimitingInterface interface {
	Get()
	Put()
}

type Type struct {
	cond *sync.Cond
}

func (q *Type) Get() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	q.cond.Wait()
}

func (q *Type) Put() {
	q.cond.Signal()
}

type ResourceQuotaController struct {
	workerLock        sync.RWMutex
	queue             RateLimitingInterface
	missingUsageQueue RateLimitingInterface
}

func (rq *ResourceQuotaController) worker(queue RateLimitingInterface, name string) func() {
	workFunc := func() bool {
		rq.workerLock.RLock()
		defer rq.workerLock.RUnlock()
		queue.Get()
		return true
	}
	return func() {
		for {
			if quit := workFunc(); quit {
				return
			}
		}
	}
}

func (rq *ResourceQuotaController) Run() {
	go rq.worker(rq.queue, "G1")()
	go rq.worker(rq.missingUsageQueue, "G2")()
}

func (rq *ResourceQuotaController) Sync() {
	for i := 0; i < 100000; i++ {
		rq.workerLock.Lock()
		time.Sleep(time.Nanosecond)
		rq.workerLock.Unlock()
	}
}

func (rq *ResourceQuotaController) HelperSignals() {

	for i := 0; i < 100000; i++ {
		rq.queue.Put()
		rq.missingUsageQueue.Put()
	}
}

func startResourceQuotaController() {
	resourceQuotaController := &ResourceQuotaController{
		queue:             &Type{sync.NewCond(&sync.Mutex{})},
		missingUsageQueue: &Type{sync.NewCond(&sync.Mutex{})},
	}

	go resourceQuotaController.Run()
	go resourceQuotaController.Sync()
	resourceQuotaController.HelperSignals()
}

func TestKubernetes58107(t *testing.T) {
	startResourceQuotaController()
}
