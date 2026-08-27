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

// 正确：返回指针，避免值复制
func (c *Cache) Clone() *Cache {
	c.mu.Lock()
	defer c.mu.Unlock()
	newItems := make(map[string]string, len(c.items))
	for k, v := range c.items {
		newItems[k] = v
	}
	return &Cache{mu: sync.Mutex{}, items: newItems}
}

func main() {
	cache := &Cache{items: make(map[string]string)}
	cache.items["key"] = "value"

	clone := cache.Clone()
	clone.Get("key")
}
