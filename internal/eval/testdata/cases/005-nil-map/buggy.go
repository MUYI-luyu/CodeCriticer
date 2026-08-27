package main

import "fmt"

func main() {
	var m map[string]int
	m["k"] = 1 // BUG: 向 nil map 写入会 panic
	fmt.Println(m)
}
