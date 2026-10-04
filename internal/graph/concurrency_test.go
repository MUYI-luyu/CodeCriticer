package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectConcurrencyTracksIntraproceduralMayMustAndDefer(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/concurrency\n\ngo 1.22\n")
	write(t, dir, "concurrency.go", `package concurrency

import "sync"

var mu sync.Mutex

func inspect(ch chan int, conditional bool) {
	mu.Lock()
	defer mu.Unlock()
	defer close(ch)
	ch <- 1
	if conditional {
		mu.Unlock()
	}
	ch <- 2
}
`)
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := idx.InspectConcurrency(SymbolRef{Name: "inspect", File: "concurrency.go"})
	if err != nil {
		t.Fatal(err)
	}
	assertFact(t, facts, "lock_acquire", "resource=global:", "path_held_must=[]")
	assertFact(t, facts, "deferred_lock_release", "effect=at_function_exit")
	assertFact(t, facts, "deferred_channel_close", "effect=at_function_exit", "exit_lockset=not_computed")
	assertFact(t, facts, "channel_send", "path_held_must=[exclusive global:")
	assertFact(t, facts, "channel_send", "path_held_may_only=[exclusive global:")
}

func TestInspectConcurrencyEtcd6708Facts(t *testing.T) {
	facts := inspectFixture(t, "blocking-etcd-6708", "TestEtcd6708")
	t.Logf("etcd facts:\n%s", formatFacts(facts))
	assertFact(t, facts, "lock_acquire", "method=RWMutex.Lock")
	assertFact(t, facts, "lock_acquire", "method=RWMutex.RLock", "path_held_must=[write ")
	assertFact(t, facts, "call_edge", "getLeaderEndpoint")
	assertFact(t, facts, "analysis_uncertainty", "dynamic calls")
}

func TestInspectConcurrencyGrpc1353Facts(t *testing.T) {
	facts := inspectFixture(t, "blocking-grpc-1353", "TestGrpc1353")
	t.Logf("grpc facts:\n%s", formatFacts(facts))
	assertFact(t, facts, "goroutine_start", "lbWatcher")
	assertFact(t, facts, "channel_send", "addrCh", "capacity=0", "path_held_must=[exclusive ")
	assertFact(t, facts, "channel_receive", "addrCh", "capacity=unknown")
	assertFact(t, facts, "call_edge", "tearDown", "path_held_must=[]")
	assertFact(t, facts, "call_edge", "roundRobin).down", "path_held_must=[exclusive ")
	assertFact(t, facts, "analysis_uncertainty", "channel_origin_or_capacity_unknown")
	assertFact(t, facts, "analysis_uncertainty", "lock_resource_alias_not_exact")
}

func inspectFixture(t *testing.T, fixture, symbol string) []ConcurrencyFact {
	t.Helper()
	sourcePath := filepath.Join("..", "eval", "testdata", "goker", fixture, "buggy.go")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/"+strings.ReplaceAll(fixture, "-", "")+"\n\ngo 1.22\n")
	write(t, dir, "buggy_test.go", string(source))
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := idx.InspectConcurrency(SymbolRef{Name: symbol, File: "buggy_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func assertFact(t *testing.T, facts []ConcurrencyFact, kind string, fragments ...string) {
	t.Helper()
	for _, fact := range facts {
		if fact.Kind != kind {
			continue
		}
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(fact.Detail, fragment) {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}
	t.Fatalf("missing fact kind=%s fragments=%q\n%s", kind, fragments, formatFacts(facts))
}

func formatFacts(facts []ConcurrencyFact) string {
	var out strings.Builder
	for _, fact := range facts {
		out.WriteString(fact.Kind)
		out.WriteString(" ")
		out.WriteString(filepath.Base(fact.File))
		out.WriteString(":")
		out.WriteString(strings.TrimSpace(strings.Join([]string{fact.Detail, "precision=" + fact.Precision}, "; ")))
		out.WriteString("\n")
	}
	return out.String()
}
