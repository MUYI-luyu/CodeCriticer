package review

import (
	"path/filepath"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/analysis/passes/atomic"
	"golang.org/x/tools/go/analysis/passes/bools"
	"golang.org/x/tools/go/analysis/passes/copylock"
	"golang.org/x/tools/go/analysis/passes/ctrlflow"
	"golang.org/x/tools/go/analysis/passes/errorsas"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/analysis/passes/loopclosure"
	"golang.org/x/tools/go/analysis/passes/lostcancel"
	"golang.org/x/tools/go/analysis/passes/nilfunc"
	"golang.org/x/tools/go/analysis/passes/printf"
	"golang.org/x/tools/go/analysis/passes/sigchanyzer"
	"golang.org/x/tools/go/analysis/passes/stdmethods"
	"golang.org/x/tools/go/analysis/passes/stringintconv"
	"golang.org/x/tools/go/analysis/passes/structtag"
	"golang.org/x/tools/go/analysis/passes/testinggoroutine"
	"golang.org/x/tools/go/analysis/passes/timeformat"
	"golang.org/x/tools/go/analysis/passes/unmarshal"
	"golang.org/x/tools/go/analysis/passes/unreachable"
	"golang.org/x/tools/go/analysis/passes/unsafeptr"
	"golang.org/x/tools/go/analysis/passes/unusedresult"
	"golang.org/x/tools/go/packages"
)

// ruleAnalyzers 是 run_static_rules 工具调用的 Go analyzers。
var ruleAnalyzers = []*analysis.Analyzer{
	atomic.Analyzer,
	bools.Analyzer,
	copylock.Analyzer,
	errorsas.Analyzer,
	loopclosure.Analyzer,
	lostcancel.Analyzer,
	nilfunc.Analyzer,
	printf.Analyzer,
	sigchanyzer.Analyzer,
	stdmethods.Analyzer,
	stringintconv.Analyzer,
	structtag.Analyzer,
	testinggoroutine.Analyzer,
	timeformat.Analyzer,
	unmarshal.Analyzer,
	unreachable.Analyzer,
	unsafeptr.Analyzer,
	unusedresult.Analyzer,
	inspect.Analyzer,  // 被上述规则依赖
	ctrlflow.Analyzer, // 被 lostcancel 依赖
}

// StaticDiagnostic 是静态分析工具的原始输出，不代表 Agent Claim。
type StaticDiagnostic struct {
	File     string
	Line     int
	Analyzer string
	Message  string
}

// Rules 跑静态规则，返回可供 Agent 引用的工具结果。
func Rules(repo string) ([]StaticDiagnostic, error) {
	cfg := &packages.Config{
		Mode: packages.LoadAllSyntax,
		Dir:  repo,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}

	g, err := checker.Analyze(ruleAnalyzers, pkgs, nil)
	if err != nil {
		return nil, err
	}

	var out []StaticDiagnostic
	for _, act := range g.Roots {
		for _, d := range act.Diagnostics {
			p := act.Package.Fset.Position(d.Pos)
			out = append(out, StaticDiagnostic{
				File:     filepath.ToSlash(relPath(repo, p.Filename)),
				Line:     p.Line,
				Analyzer: act.Analyzer.Name,
				Message:  d.Message,
			})
		}
	}
	return out, nil
}

// relPath 把绝对路径转成相对 repo 的路径，失败则原样返回。
func relPath(repo, abs string) string {
	rel, err := filepath.Rel(repo, abs)
	if err != nil {
		return abs
	}
	return rel
}
