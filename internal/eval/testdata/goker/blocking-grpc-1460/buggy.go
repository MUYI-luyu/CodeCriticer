













package grpc1460

import (
	"sync"
	"testing"
)

type Stream struct{}

type http2Client struct {
	mu              sync.Mutex
	awakenKeepalive chan struct{}
	activeStream    []*Stream
}

func (t *http2Client) keepalive() {
	t.mu.Lock()
	if len(t.activeStream) < 1 {
		<-t.awakenKeepalive
		t.mu.Unlock()
	} else {
		t.mu.Unlock()
	}
}

func (t *http2Client) NewStream() {
	t.mu.Lock()
	t.activeStream = append(t.activeStream, &Stream{})
	if len(t.activeStream) == 1 {
		select {
		case t.awakenKeepalive <- struct{}{}:

		default:
		}
	}
	t.mu.Unlock()
}










func TestGrpc1460(t *testing.T) {
	client := &http2Client{
		awakenKeepalive: make(chan struct{}),
	}
	go client.keepalive()
	go client.NewStream()
}
