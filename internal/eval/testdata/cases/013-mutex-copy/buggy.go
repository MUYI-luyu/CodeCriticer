package main

import "sync"

type Cache struct {
	mu    sync.Mutex
	items map[string]string
}

func (c *Cache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	val, ok := c.items[key]
	return val, ok
}

// BUG: 按值返回包含 Mutex 的结构体，会复制锁
func (c Cache) Clone() Cache {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c
}

func main() {
	cache := Cache{items: make(map[string]string)}
	cache.items["key"] = "value"

	clone := cache.Clone()
	clone.Get("key") // 死锁或 panic
}
