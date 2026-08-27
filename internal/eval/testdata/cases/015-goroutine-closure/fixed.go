package main

import "fmt"

func ProcessItems(items []string) {
	results := make(chan string)

	for _, item := range items {
		item := item // 创建循环作用域的副本
		go func() {
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
