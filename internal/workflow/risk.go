package workflow

import (
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/diff"
)

// buildRiskSeeds 从变更行和所属符号生成有限风险入口。
func buildRiskSeeds(changes []diff.Change) []RiskSeed {
	var seeds []RiskSeed
	seen := make(map[string]bool)
	for _, change := range changes {
		for _, line := range change.Adds {
			symbol := symbolAt(change.Symbols, line.No)
			text := strings.ToLower(line.Text)
			categories := seedCategories(text)
			for _, category := range categories {
				key := category + ":" + change.File + ":" + itoa(line.No)
				if seen[key] {
					continue
				}
				seen[key] = true
				seeds = append(seeds, RiskSeed{Category: category, File: change.File, Line: line.No, Symbol: symbol, Trigger: strings.TrimSpace(line.Text)})
			}
		}
	}
	return seeds
}

func symbolAt(symbols []diff.Symbol, line int) string {
	for _, symbol := range symbols {
		if line >= symbol.Line && line <= symbol.EndLine {
			return symbol.Name
		}
	}
	return ""
}

func seedCategories(text string) []string {
	var out []string
	if containsAny(text, ".lock(", ".unlock(", ".rlock(", ".runlock(") {
		out = append(out, "lock")
	}
	if strings.Contains(text, "go ") || strings.Contains(text, "<-") || strings.Contains(text, "select") {
		out = append(out, "goroutine_channel")
	}
	if strings.Contains(text, "context.withcancel") || strings.Contains(text, "context.withtimeout") || strings.Contains(text, ".done()") {
		out = append(out, "context")
	}
	if strings.Contains(text, "error") || strings.Contains(text, ") error") {
		out = append(out, "error")
	}
	if strings.Contains(text, "[") || strings.Contains(text, "nil") || strings.Contains(text, "uint") || strings.Contains(text, "int") || strings.Contains(text, "mutex") {
		out = append(out, "boundary")
	}
	return out
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// buildHypotheses 为每个风险种子生成固定且可观测的调查缺口。
func buildHypotheses(seeds []RiskSeed) []Hypothesis {
	result := make([]Hypothesis, 0, len(seeds))
	for i, seed := range seeds {
		required := requiredFacts(seed.Category)
		result = append(result, Hypothesis{ID: "h" + itoa(i+1), Category: seed.Category, Claim: claimFor(seed.Category), TargetFile: seed.File, TargetSymbol: seed.Symbol, RequiredFacts: required})
	}
	return result
}

func requiredFacts(category string) []string {
	switch category {
	case "lock":
		return []string{"lock_acquire", "lock_release", "call_path"}
	case "goroutine_channel":
		return []string{"goroutine_start", "channel_operation", "exit_path"}
	case "context":
		return []string{"context_create", "cancel_call", "done_wait"}
	case "error":
		return []string{"error_return", "error_use"}
	case "boundary":
		return []string{"dangerous_access", "boundary_guard"}
	default:
		return []string{"call_path"}
	}
}

func claimFor(category string) string {
	switch category {
	case "lock":
		return "变更可能引入锁顺序或锁保护问题"
	case "goroutine_channel":
		return "变更可能引入 goroutine 或 channel 生命周期问题"
	case "context":
		return "变更可能引入 context 取消或资源释放问题"
	case "error":
		return "变更可能丢失或错误传播 error"
	case "boundary":
		return "变更可能引入 nil、索引或类型边界问题"
	default:
		return "变更可能影响调用路径"
	}
}
