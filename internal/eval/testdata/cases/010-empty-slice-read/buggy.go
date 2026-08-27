package main

import "fmt"

// Read 把指标写入 m；空切片应为 no-op。
func Read(m []int) {
	fmt.Println(m[0])
}

func main() {
	Read(nil)
}
