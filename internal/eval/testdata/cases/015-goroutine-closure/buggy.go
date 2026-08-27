package main

import "fmt"

func ProcessItems(items []string) {
	results := make(chan string)

	for _, item := range items {
		go func() {
			// BUG: 闭包捕获循环变量 item，所有 goroutine 看到的是同一个变量
			results <- fmt.Sprintf("processed: %s", item)
		}()
	}

	for i := 0; i < len(items); i++ {
		fmt.Println(<-results)
	}
}

func main() {
	ProcessItems([]string{"a", "b", "c"})
}
