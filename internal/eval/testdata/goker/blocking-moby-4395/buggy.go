










package moby4395

import (
	"errors"
	"testing"
)

func Go(f func() error) chan error {
	ch := make(chan error)
	go func() {
		ch <- f()
	}()
	return ch
}









func TestMoby4395(t *testing.T) {
	Go(func() error {
		return errors.New("")
	})
}
