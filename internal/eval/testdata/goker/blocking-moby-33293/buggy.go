package moby33293

import (
	"errors"
	"math/rand"
	"testing"
)

func MayReturnError() error {
	if rand.Int31n(2) >= 1 {
		return errors.New("Error")
	}
	return nil
}
func containerWait() <-chan error {
	errC := make(chan error)
	err := MayReturnError()
	if err != nil {
		errC <- err
		return errC
	}
	return errC
}

func TestMoby33293(t *testing.T) {
	go func() {
		err := containerWait()
		if err != nil {
			return
		}
	}()
}
