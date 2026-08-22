package main

import "sync"

type Config struct {
	mu sync.Mutex
	v  int
}

func clone(c *Config) *Config {
	return &Config{v: c.v}
}

func main() {}
