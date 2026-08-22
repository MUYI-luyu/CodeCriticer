package main

import "fmt"

func main() {
	name := "world"
	fmt.Printf("%d\n", name) // BUG: 格式化动词 %d 用于字符串，应为 %s
}
