package moby36114

import (
	"sync"
	"testing"
)

type serviceVM struct {
	sync.Mutex
}

func (svm *serviceVM) hotAddVHDsAtStart() {
	svm.Lock()
	defer svm.Unlock()
	svm.hotRemoveVHDsAtStart()
}

func (svm *serviceVM) hotRemoveVHDsAtStart() {
	svm.Lock()
	defer svm.Unlock()
}

func TestMoby36114(t *testing.T) {
	s := &serviceVM{}
	go s.hotAddVHDsAtStart()
}
