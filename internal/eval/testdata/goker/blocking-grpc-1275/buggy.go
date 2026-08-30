
















package grpc1275

import (
	"io"
	"testing"
	"time"
)

type recvBuffer struct {
	c chan bool
}

func (b *recvBuffer) get() <-chan bool {
	return b.c
}

type recvBufferReader struct {
	recv *recvBuffer
}

func (r *recvBufferReader) Read(p []byte) (int, error) {
	select {
	case <-r.recv.get():
	}
	return 0, nil
}

type Stream struct {
	trReader io.Reader
}

func (s *Stream) Read(p []byte) (int, error) {
	return io.ReadFull(s.trReader, p)
}

type http2Client struct{}

func (t *http2Client) CloseStream(s *Stream) {



}

func (t *http2Client) NewStream() *Stream {
	return &Stream{
		trReader: &recvBufferReader{
			recv: &recvBuffer{
				c: make(chan bool),
			},
		},
	}
}

func testInflightStreamClosing() {
	client := &http2Client{}
	stream := client.NewStream()
	donec := make(chan bool)
	go func() {
		defer close(donec)
		stream.Read([]byte{1})
	}()

	client.CloseStream(stream)

	timeout := time.NewTimer(300 * time.Nanosecond)
	select {
	case <-donec:
		if !timeout.Stop() {
			<-timeout.C
		}
	case <-timeout.C:
	}
}












func TestGrpc1293(t *testing.T) {
	testInflightStreamClosing()
}
