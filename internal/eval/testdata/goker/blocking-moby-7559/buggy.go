package moby7559

import (
	"net"
	"sync"
	"testing"
)

type UDPProxy struct {
	connTrackLock sync.Mutex
}

func (proxy *UDPProxy) Run() {
	for i := 0; i < 2; i++ {
		proxy.connTrackLock.Lock()
		_, err := net.DialUDP("udp", nil, nil)
		if err != nil {

			continue
		}
		if i == 0 {
			break
		}
	}
	proxy.connTrackLock.Unlock()
}
func TestMoby7559(t *testing.T) {
	proxy := &UDPProxy{}
	go proxy.Run()
}
