package diff

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Reference 是一个符号引用点。
type Reference struct {
	File     string // 文件路径
	Line     int    // 行号
	Col      int    // 列号
	Context  string // 代码上下文（所在行）
	IsWrite  bool   // 是否写操作
	Function string // 所在函数
}

// FindReferences 在仓库中查找符号的所有引用。
func FindReferences(repo, symbolName string) []Reference {
	var refs []Reference
	walkGoFiles(repo, func(path string, f *ast.File, fs *token.FileSet) {
		refs = append(refs, refsInFile(f, fs, path, symbolName)...)
	})
	return refs
}

// FindCallSites 查找函数/方法的所有调用点。
func FindCallSites(repo, funcName string) []Reference {
	var refs []Reference
	walkGoFiles(repo, func(path string, f *ast.File, fs *token.FileSet) {
		refs = append(refs, callSitesInFile(f, fs, path, funcName)...)
	})
	return refs
}

// walkGoFiles 遍历仓库中所有非测试 .go 文件并解析。
func walkGoFiles(repo string, fn func(path string, f *ast.File, fs *token.FileSet)) {
	filepath.Walk(repo, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == "vendor" || name == ".git" || (name != "." && strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fs := token.NewFileSet()
		f, err := parser.ParseFile(fs, path, nil, 0)
		if err != nil {
			return nil
		}
		fn(path, f, fs)
		return nil
	})
}

// refsInFile 收集文件中某个标识符的所有引用点。
func refsInFile(f *ast.File, fs *token.FileSet, path, name string) []Reference {
	writes := map[token.Pos]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for _, l := range x.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == name {
					writes[id.Pos()] = true
				}
			}
		case *ast.IncDecStmt:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == name {
				writes[id.Pos()] = true
			}
		case *ast.ValueSpec:
			for _, id := range x.Names {
				if id.Name == name {
					writes[id.Pos()] = true
				}
			}
		}
		return true
	})

	var refs []Reference
	curFunc := ""
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			curFunc = x.Name.Name
		case *ast.Ident:
			if x.Name != name {
				return true
			}
			pos := fs.Position(x.Pos())
			refs = append(refs, Reference{
				File:     path,
				Line:     pos.Line,
				Col:      pos.Column,
				Context:  lineAt(path, pos.Line),
				IsWrite:  writes[x.Pos()],
				Function: curFunc,
			})
		}
		return true
	})
	return refs
}

// callSitesInFile 收集文件中某个函数的所有调用点。
func callSitesInFile(f *ast.File, fs *token.FileSet, path, name string) []Reference {
	var refs []Reference
	curFunc := ""
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			curFunc = x.Name.Name
		case *ast.CallExpr:
			if !callMatches(x.Fun, name) {
				return true
			}
			pos := fs.Position(x.Pos())
			refs = append(refs, Reference{
				File:     path,
				Line:     pos.Line,
				Col:      pos.Column,
				Context:  lineAt(path, pos.Line),
				Function: curFunc,
			})
		}
		return true
	})
	return refs
}

// callMatches 判断调用表达式是否匹配目标函数名（支持 pkg.Func 形式）。
func callMatches(expr ast.Expr, name string) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == name
	case *ast.SelectorExpr:
		if e.Sel.Name == name {
			return true
		}
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name+"."+e.Sel.Name == name
		}
	}
	return false
}

// lineAt 读取文件第 line 行的文本（1-based）。
func lineAt(path string, line int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	if line >= 1 && line <= len(lines) {
		return strings.TrimSpace(lines[line-1])
	}
	return ""
}
