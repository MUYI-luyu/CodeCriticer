package main

import (
	"fmt"
	"sync"
)

func main() {
	m := &sync.Mutex{}
	fmt.Println(m)
}
