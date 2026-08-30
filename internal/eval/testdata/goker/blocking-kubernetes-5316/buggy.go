











package kubernetes5316

import (
	"errors"
	"math/rand"
	"testing"
	"time"
)

func finishRequest(timeout time.Duration, fn func() error) {
	ch := make(chan bool)
	errCh := make(chan error)
	go func() {
		if err := fn(); err != nil {
			errCh <- err
		} else {
			ch <- true
		}
	}()

	select {
	case <-ch:
	case <-errCh:
	case <-time.After(timeout):
	}
}










func TestKubernetes5316(t *testing.T) {
	fn := func() error {
		time.Sleep(2 * time.Millisecond)
		if rand.Intn(10) > 5 {
			return errors.New("Error")
		}
		return nil
	}
	go finishRequest(time.Millisecond, fn)
}
