package main

import "fmt"

func main() {
	fmt.Errorf("write failed: %s", "disk") // BUG: error 返回值被丢弃未处理
}
