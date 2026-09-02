package cockroach6181

import (
	"fmt"
	"sync"
	"testing"
)

type testDescriptorDB struct {
	cache *rangeDescriptorCache
}

func initTestDescriptorDB() *testDescriptorDB {
	return &testDescriptorDB{&rangeDescriptorCache{}}
}

type rangeDescriptorCache struct {
	rangeCacheMu sync.RWMutex
}

func (rdc *rangeDescriptorCache) LookupRangeDescriptor() {
	rdc.rangeCacheMu.RLock()
	fmt.Printf("lookup range descriptor: %s", rdc)
	rdc.rangeCacheMu.RUnlock()
	rdc.rangeCacheMu.Lock()
	rdc.rangeCacheMu.Unlock()
}

func (rdc *rangeDescriptorCache) String() string {
	rdc.rangeCacheMu.RLock()
	defer rdc.rangeCacheMu.RUnlock()
	return rdc.stringLocked()
}

func (rdc *rangeDescriptorCache) stringLocked() string {
	return "something here"
}

func doLookupWithToken(rc *rangeDescriptorCache) {
	rc.LookupRangeDescriptor()
}

func testRangeCacheCoalescedRquests() {
	db := initTestDescriptorDB()
	pauseLookupResumeAndAssert := func() {
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				doLookupWithToken(db.cache)
				wg.Done()
			}()
		}
		wg.Wait()
	}
	pauseLookupResumeAndAssert()
}

func TestCockroach6181(t *testing.T) {
	go testRangeCacheCoalescedRquests()
}
