package main

import (
	"context"
	"fmt"
	"time"
)

func Process(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	result := make(chan string, 1)
	go func() {
		time.Sleep(1 * time.Second)
		result <- "done"
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case msg := <-result:
		fmt.Println(msg)
		return nil
	}
}

func main() {
	if err := Process(context.Background()); err != nil {
		fmt.Println("Error:", err)
	}
}
