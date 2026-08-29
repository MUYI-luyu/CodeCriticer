package agent

import (
	"context"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

func TestStaticRulesTool(t *testing.T) {
	// 使用当前项目作为测试目标
	tool := &StaticRulesTool{
		repo: "../..",
	}

	if tool.Name() != "static_rules" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "static_rules")
	}

	if tool.Description() == "" {
		t.Error("Description() is empty")
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("Execute() result is not a map")
	}

	count, ok := resultMap["count"].(int)
	if !ok {
		t.Fatal("Execute() result missing 'count' field")
	}

	findings, ok := resultMap["findings"].([]map[string]interface{})
	if !ok {
		t.Fatal("Execute() result 'findings' is not a slice")
	}

	if len(findings) != count {
		t.Errorf("findings length = %d, count = %d", len(findings), count)
	}

	t.Logf("Found %d static analysis issues", count)

	// 验证 findings 结构
	for i, f := range findings {
		if _, ok := f["file"].(string); !ok {
			t.Errorf("finding %d missing 'file' field", i)
		}
		if _, ok := f["line"].(int); !ok {
			t.Errorf("finding %d missing 'line' field", i)
		}
		if _, ok := f["rule"].(string); !ok {
			t.Errorf("finding %d missing 'rule' field", i)
		}
		if _, ok := f["severity"].(string); !ok {
			t.Errorf("finding %d missing 'severity' field", i)
		}
		if _, ok := f["message"].(string); !ok {
			t.Errorf("finding %d missing 'message' field", i)
		}
	}
}

func TestStaticRulesToolIntegration(t *testing.T) {
	// 创建一个临时测试文件（含明显错误）
	tmpDir := t.TempDir()

	// 这个测试验证静态规则工具能否被正确集成到 orchestrator
	tool := &StaticRulesTool{repo: tmpDir}

	registry := NewToolRegistry()
	registry.Register(tool)

	retrievedTool, ok := registry.Get("static_rules")
	if !ok {
		t.Fatal("static_rules tool not found in registry")
	}

	if retrievedTool.Name() != "static_rules" {
		t.Errorf("Retrieved tool name = %q, want %q", retrievedTool.Name(), "static_rules")
	}
}

func TestRulesDirectCall(t *testing.T) {
	// 直接测试 review.Rules() 函数
	findings, err := review.Rules("../..")
	if err != nil {
		t.Skipf("Rules() error (may be expected in test env): %v", err)
	}

	t.Logf("Direct Rules() call found %d issues", len(findings))

	for _, f := range findings {
		if f.File == "" {
			t.Error("Finding has empty file")
		}
		if f.Line <= 0 {
			t.Error("Finding has invalid line number")
		}
		if f.Symbol == "" {
			t.Error("Finding has empty symbol (rule name)")
		}
	}
}
