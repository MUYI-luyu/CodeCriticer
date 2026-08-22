package main

import "sync"

type Config struct {
	mu sync.Mutex
	v  int
}

func clone(c Config) Config { // BUG: 值传递拷贝了 sync.Mutex
	return c
}

func main() {}
