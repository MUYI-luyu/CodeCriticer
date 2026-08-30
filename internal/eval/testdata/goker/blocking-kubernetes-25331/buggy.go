











package kubernetes25331

import (
	"context"
	"errors"
	"testing"
)

type watchChan struct {
	ctx        context.Context
	cancel     context.CancelFunc
	resultChan chan bool
	errChan    chan error
}

func (wc *watchChan) Stop() {
	wc.errChan <- errors.New("Error")
	wc.cancel()
}

func (wc *watchChan) run() {
	select {
	case err := <-wc.errChan:
		errResult := len(err.Error()) != 0
		wc.cancel()
		wc.resultChan <- errResult
	case <-wc.ctx.Done():
	}
}

func NewWatchChan() *watchChan {
	ctx, cancel := context.WithCancel(context.Background())
	return &watchChan{
		ctx:        ctx,
		cancel:     cancel,
		resultChan: make(chan bool),
		errChan:    make(chan error),
	}
}













func TestKubernetes25331(t *testing.T) {
	wc := NewWatchChan()
	go wc.run()
	go wc.Stop()
}
