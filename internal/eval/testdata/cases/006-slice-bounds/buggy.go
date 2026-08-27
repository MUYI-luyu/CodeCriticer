package main

import "fmt"

func main() {
	s := []int{1, 2, 3}
	fmt.Println(s[3]) // BUG: 索引越界，len 为 3 时最大下标为 2
}
