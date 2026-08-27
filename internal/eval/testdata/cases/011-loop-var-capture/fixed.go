package main

import (
	"fmt"
	"time"
)

func main() {
	names := []string{"a", "b", "c"}
	for _, n := range names {
		n := n
		go func() {
			fmt.Println(n)
		}()
	}
	time.Sleep(100 * time.Millisecond)
}
