package moby17176

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type DeviceSet struct {
	sync.Mutex
	nrDeletedDevices int
}

func (devices *DeviceSet) cleanupDeletedDevices() error {
	devices.Lock()
	if devices.nrDeletedDevices == 0 {

		return nil
	}
	devices.Unlock()
	return errors.New("Error")
}

func testDevmapperLockReleasedDeviceDeletion() {
	ds := &DeviceSet{
		nrDeletedDevices: 0,
	}
	ds.cleanupDeletedDevices()
	doneChan := make(chan bool)
	go func() {
		ds.Lock()
		defer ds.Unlock()
		doneChan <- true
	}()

	select {
	case <-time.After(time.Millisecond):
	case <-doneChan:
	}
}
func TestMoby17176(t *testing.T) {
	testDevmapperLockReleasedDeviceDeletion()
}
