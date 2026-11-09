package workflow

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ProjectMemory is deliberately a small, auditable long-term memory format.
// Entries are authored or explicitly confirmed by a developer; review output
// is never promoted automatically, avoiding self-reinforcing hallucinations.
type ProjectMemory struct {
	Version int           `json:"version"`
	Entries []MemoryEntry `json:"entries"`
}

func LoadProjectMemory(path string) ([]MemoryEntry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read project memory: %w", err)
	}
	var store ProjectMemory
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store); err != nil {
		return nil, fmt.Errorf("decode project memory: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode project memory: multiple JSON values")
		}
		return nil, fmt.Errorf("decode project memory trailing data: %w", err)
	}
	if store.Version != 1 {
		return nil, fmt.Errorf("unsupported project memory version %d", store.Version)
	}
	seen := make(map[string]bool, len(store.Entries))
	for i := range store.Entries {
		entry := &store.Entries[i]
		entry.ID = strings.TrimSpace(entry.ID)
		entry.PathPrefix = strings.Trim(strings.TrimSpace(entry.PathPrefix), "/")
		entry.Content = strings.TrimSpace(entry.Content)
		if entry.ID == "" || seen[entry.ID] {
			return nil, fmt.Errorf("memory entry %d has empty or duplicate id", i)
		}
		seen[entry.ID] = true
		if entry.PathPrefix == ".." || strings.HasPrefix(entry.PathPrefix, "../") || filepath.IsAbs(entry.PathPrefix) {
			return nil, fmt.Errorf("memory entry %s has invalid path_prefix", entry.ID)
		}
		if entry.Source != "human_confirmed" {
			return nil, fmt.Errorf("memory entry %s must be human_confirmed", entry.ID)
		}
		if entry.Content == "" || len(entry.Content) > 1200 {
			return nil, fmt.Errorf("memory entry %s has invalid content length", entry.ID)
		}
	}
	return store.Entries, nil
}

func selectProjectMemory(entries []MemoryEntry, changedFiles []string) []MemoryEntry {
	selected := make([]MemoryEntry, 0, len(entries))
	for _, entry := range entries {
		for _, file := range changedFiles {
			cleanFile := strings.Trim(strings.TrimSpace(file), "/")
			prefix := strings.Trim(entry.PathPrefix, "/")
			if prefix == "" || cleanFile == prefix || strings.HasPrefix(cleanFile, prefix+"/") {
				selected = append(selected, entry)
				break
			}
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if len(selected[i].PathPrefix) != len(selected[j].PathPrefix) {
			return len(selected[i].PathPrefix) > len(selected[j].PathPrefix)
		}
		return selected[i].ID < selected[j].ID
	})
	if len(selected) > 6 {
		selected = selected[:6]
	}
	return selected
}
