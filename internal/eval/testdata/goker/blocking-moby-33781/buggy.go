package moby33781

import (
	"context"
	"testing"
	"time"
)

func monitor(stop chan bool) {
	probeInterval := 50 * time.Nanosecond
	probeTimeout := 50 * time.Nanosecond
	for {
		select {
		case <-stop:
			return
		case <-time.After(probeInterval):
			results := make(chan bool)
			ctx, cancelProbe := context.WithTimeout(context.Background(), probeTimeout)
			go func() {
				results <- true
				close(results)
			}()
			select {
			case <-stop:

				cancelProbe()
				return
			case <-results:
				cancelProbe()
			case <-ctx.Done():
				cancelProbe()
				<-results
			}
		}
	}
}

func TestMoby33781(t *testing.T) {
	stop := make(chan bool)
	go monitor(stop)
	go func() {
		time.Sleep(50 * time.Nanosecond)
		stop <- true
	}()
}
