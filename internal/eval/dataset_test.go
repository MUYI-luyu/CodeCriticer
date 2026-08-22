package eval

import "testing"

func TestLoadCases(t *testing.T) {
	cases, err := Load("testdata/cases")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 3 {
		t.Fatalf("期望至少 3 个用例，得到 %d", len(cases))
	}

	// 001-printf 应有 1 个 BUG 标记
	var printf *Case
	for _, c := range cases {
		if c.Name == "printf" {
			printf = c
			break
		}
	}
	if printf == nil {
		t.Fatal("未找到 printf 用例")
	}
	if len(printf.Bugs) != 1 {
		t.Fatalf("printf 应有 1 个 bug，得到 %d", len(printf.Bugs))
	}
	if printf.Bugs[0].File != "main.go" || printf.Bugs[0].Line != 7 {
		t.Fatalf("printf bug 定位错误: %+v", printf.Bugs[0])
	}
	if len(printf.Diff) == 0 {
		t.Fatal("printf diff 为空")
	}
}
