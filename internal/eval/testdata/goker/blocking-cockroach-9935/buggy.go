








package cockroach9935

import (
	"errors"
	"math/rand"
	"sync"
	"testing"
)

type loggingT struct {
	mu sync.Mutex
}

func (l *loggingT) outputLogEntry() {
	l.mu.Lock()
	if err := l.createFile(); err != nil {
		l.exit(err)
	}
	l.mu.Unlock()
}
func (l *loggingT) createFile() error {
	if rand.Intn(8)%4 > 0 {
		return errors.New("")
	}
	return nil
}
func (l *loggingT) exit(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
}
func TestCockroach9935(t *testing.T) {
	l := &loggingT{}
	go l.outputLogEntry()
}
