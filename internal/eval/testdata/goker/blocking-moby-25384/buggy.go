











package moby25384

import (
	"sync"
	"testing"
)

type plugin struct{}

type Manager struct {
	plugins []*plugin
}

func (pm *Manager) init() {
	var group sync.WaitGroup
	group.Add(len(pm.plugins))
	for _, p := range pm.plugins {
		go func(p *plugin) {
			defer group.Done()
		}(p)
		group.Wait()
	}
}
func TestMoby25384(t *testing.T) {
	p1 := &plugin{}
	p2 := &plugin{}
	pm := &Manager{
		plugins: []*plugin{p1, p2},
	}
	go pm.init()
}
