








package grpc660

import (
	"math/rand"
	"testing"
)

type benchmarkClient struct {
	stop chan bool
}

func (bc *benchmarkClient) doCloseLoopUnary() {
	for {
		done := make(chan bool)
		go func() {
			if rand.Intn(10) > 7 {
				done <- false
				return
			}
			done <- true
		}()
		select {
		case <-bc.stop:
			return
		case <-done:
		}
	}
}











func TestGrpc660(t *testing.T) {
	bc := &benchmarkClient{
		stop: make(chan bool),
	}
	go bc.doCloseLoopUnary()
	go func() {
		bc.stop <- true
	}()
}
