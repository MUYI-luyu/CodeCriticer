package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// StaticRulesTool 运行 go vet 系列确定性静态规则，作为审查的早期检测工具。
//
// 这些规则误报率低、不消耗 LLM token，是审查结果的最低保障：
// LLM 至少应该找到静态规则已经命中的确定性问题。
type StaticRulesTool struct{}

func (StaticRulesTool) Name() string { return "static_rules" }

func (StaticRulesTool) Description() string {
	return "运行 go vet 系列静态规则（printf、copylock、loopclosure 等 18 类确定性检查）"
}

// StaticFinding 是静态规则发现的结构化表示，附带命中的符号上下文。
type StaticFinding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Symbol   string `json:"symbol,omitempty"`
	Context  string `json:"context,omitempty"`
	Hint     string `json:"hint,omitempty"`
	InDiff   bool   `json:"in_diff,omitempty"`
}

// StaticRuleResult 是 static_rules 工具的返回结构。
type StaticRuleResult struct {
	Count    int             `json:"count"`
	Findings []StaticFinding `json:"findings"`
}

// Execute 运行静态规则，把确定性发现并入 State.Findings，并返回结构化摘要。
func (StaticRulesTool) Execute(ctx context.Context, st *State, args map[string]any) (string, error) {
	_ = ctx
	if st == nil {
		return "", errors.New("state 为空")
	}
	if st.Repo == "" {
		return "", errors.New("缺少 repo 路径")
	}

	out, err := runStaticRules(st, staticScope(args))
	if err != nil {
		return "", err
	}
	if out.Count == 0 {
		return "count=0", nil
	}

	text := formatStaticResult(out)
	st.AppendEvidence(NameOf(StaticRulesTool{}), text)
	return text, nil
}

// runStaticRules 执行规则并返回结构化结果；确定性发现会写入 st.Findings。
//
// scope 为 "diff" 时只保留落在 diff 内文件上的发现，其余值（含空串）等价于 "all"。
func runStaticRules(st *State, scope string) (StaticRuleResult, error) {
	findings, err := review.Rules(st.Repo)
	if err != nil {
		return StaticRuleResult{}, err
	}
	if scope == "diff" {
		findings = filterByDiff(findings, st.RawDiff)
	}

	out := StaticRuleResult{Count: len(findings)}
	diffSyms := diffSymbolRanges(st.Repo, st.RawDiff)
	seen := map[string]bool{}
	for i := range findings {
		f := findings[i]
		sf := StaticFinding{
			File:     f.File,
			Line:     f.Line,
			Rule:     f.Symbol, // rule.go 把命中的规则名放在 Symbol 字段
			Severity: staticSeverity(f.Severity),
			Message:  f.Msg,
			Hint:     ruleHint(f.Symbol),
			InDiff:   inDiffSpan(diffSyms, f.File, f.Line),
		}
		sf.Symbol, sf.Context = staticSymbol(st.Repo, f.File, f.Line)
		if sf.Context != "" {
			f.Evidence = sf.Context
		}
		out.Findings = append(out.Findings, sf)

		// 确定性发现并入 State，作为最终结果的最低保障（按 文件:行:规则 去重）。
		key := fmt.Sprintf("%s:%d:%s", f.File, f.Line, f.Symbol)
		if !seen[key] {
			seen[key] = true
			st.AddFindings(f)
		}
	}
	sortStaticFindings(out.Findings)
	return out, nil
}

// staticScope 解析 scope 参数：diff 只保留 diff 内文件的发现，默认 all。
func staticScope(args map[string]any) string {
	v, ok := args["scope"]
	if !ok {
		return "all"
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(v))) {
	case "diff":
		return "diff"
	default:
		return "all"
	}
}

// staticSeverity 归一化 severity，空值默认 warning。
func staticSeverity(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "warning"
	}
	return s
}

// filterByDiff 只保留变更文件上的静态发现；diff 解析失败时原样返回。
func filterByDiff(findings []review.Finding, rawDiff string) []review.Finding {
	changes, err := diff.Parse([]byte(rawDiff))
	if err != nil {
		return findings
	}
	files := make(map[string]bool, len(changes))
	for _, c := range changes {
		files[c.File] = true
	}
	var out []review.Finding
	for _, f := range findings {
		if files[f.File] {
			out = append(out, f)
		}
	}
	return out
}

// staticSymbol 读取命中位置并提取所属符号名与签名。
func staticSymbol(repo, file string, line int) (string, string) {
	src, err := os.ReadFile(filepath.Join(repo, file))
	if err != nil {
		return "", ""
	}
	sym, ok := diff.Locate(src, line)
	if !ok {
		return "", ""
	}
	return sym.Name, sym.Signature
}

// sortStaticFindings 按 severity 权重（error < warning < info）再按 文件/行 稳定排序。
func sortStaticFindings(fs []StaticFinding) {
	rank := func(s string) int {
		switch s {
		case "error":
			return 0
		case "warning":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(fs, func(i, j int) bool {
		ri, rj := rank(fs[i].Severity), rank(fs[j].Severity)
		if ri != rj {
			return ri < rj
		}
		if fs[i].File != fs[j].File {
			return fs[i].File < fs[j].File
		}
		return fs[i].Line < fs[j].Line
	})
}

// formatStaticResult 把结构化结果格式化为可读文本。
func formatStaticResult(r StaticRuleResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "count=%d (%s)\n", r.Count, groupStaticByRule(r.Findings))
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "%s:%d [%s] %s: %s", f.File, f.Line, f.Rule, f.Severity, f.Message)
		if f.InDiff {
			b.WriteString(" [diff内]")
		}
		if f.Symbol != "" {
			fmt.Fprintf(&b, " (符号 %s", f.Symbol)
			if f.Context != "" {
				fmt.Fprintf(&b, ": %s", f.Context)
			}
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// groupStaticByRule 统计每个规则命中的次数，用于快速了解问题分布。
func groupStaticByRule(fs []StaticFinding) string {
	counts := map[string]int{}
	for _, f := range fs {
		counts[f.Rule]++
	}
	rules := make([]string, 0, len(counts))
	for r := range counts {
		rules = append(rules, r)
	}
	sort.Strings(rules)
	var b strings.Builder
	for i, r := range rules {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%d", r, counts[r])
	}
	return b.String()
}

// ruleHints 给每个确定性规则配一句中文说明，供决策器与最终报告解释命中的规则。
var ruleHints = map[string]string{
	"atomic":          "sync/atomic 原子操作误用（值拷贝或非原子访问）",
	"bools":           "布尔表达式恒真/恒假或可简化",
	"copylock":        "值传递复制了含锁的结构体，导致锁失效",
	"errorsas":        "errors.As 的第二个参数类型错误",
	"loopclosure":     "循环变量被闭包捕获，可能读到错误的值",
	"lostcancel":      "context.WithCancel 返回的 cancel 未被调用",
	"nilfunc":         "nil 函数与 nil 比较（不可能为 nil）",
	"printf":          "格式化字符串与实参不匹配（动词、数量）",
	"sigchanyzer":     "signal 通知 channel 未使用缓冲",
	"stdmethods":      "标准库方法（MarshalJSON 等）签名错误",
	"stringintconv":   "有符号整数转字符串产生意外字符",
	"structtag":       "struct tag 格式非法",
	"testinggoroutine": "testing.T 在非测试 goroutine 中使用",
	"timeformat":      "time.Format 使用了错误的参照时间",
	"unmarshal":       "json.Unmarshal 目标类型错误",
	"unreachable":     "存在不可达代码",
	"unsafeptr":       "unsafe.Pointer 规则误用",
	"unusedresult":    "错误或返回值未使用",
}

// ruleHint 返回规则的说明；未知规则给通用提示。
func ruleHint(name string) string {
	if h, ok := ruleHints[name]; ok {
		return h
	}
	if name == "inspect" || name == "ctrlflow" {
		return "" // 被依赖的辅助分析器，通常不直接产生诊断
	}
	return "确定性静态规则命中"
}

// lineRange 是一个闭区间行号范围。
type lineRange struct {
	start int
	end   int
}

// diffSymbolRanges 解析 diff 并返回 文件 -> 变更符号行区间 的映射。
//
// 与 Step2 的 AST diff 协同：静态规则命中的 file/line 可对应到变更符号边界，
// 从而区分“本次 diff 直接引入的问题”与“仓库既有问题”。
func diffSymbolRanges(repo, rawDiff string) map[string][]lineRange {
	if repo == "" || rawDiff == "" {
		return nil
	}
	changes, err := diff.ParseWithRepo([]byte(rawDiff), repo)
	if err != nil {
		return nil
	}
	out := make(map[string][]lineRange)
	for _, c := range changes {
		for _, sym := range c.Symbols {
			if sym.Line <= 0 {
				continue
			}
			end := sym.EndLine
			if end <= 0 {
				end = sym.Line
			}
			out[c.File] = append(out[c.File], lineRange{start: sym.Line, end: end})
		}
	}
	return out
}

// inDiffSpan 判断某文件的某行是否落在 diff 变更符号区间内。
func inDiffSpan(ranges map[string][]lineRange, file string, line int) bool {
	for _, r := range ranges[file] {
		if line >= r.start && line <= r.end {
			return true
		}
	}
	return false
}
