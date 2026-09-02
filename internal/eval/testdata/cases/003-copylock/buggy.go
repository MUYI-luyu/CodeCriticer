package main

import (
	"fmt"
	"sync"
)

func main() {
	m := sync.Mutex{}
	m2 := m // BUG: 按值复制锁会破坏互斥，应传指针
	fmt.Println(m2)
}
