package diff

import "testing"

func TestParse(t *testing.T) {
	raw := "diff --git a/foo.go b/foo.go\n" +
		"--- a/foo.go\n" +
		"+++ b/foo.go\n" +
		"@@ -1,3 +1,3 @@\n" +
		" package foo\n" +
		" \n" +
		"-func old() {}\n" +
		"+func new() {}\n"

	cs, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 {
		t.Fatalf("期望 1 个文件，得到 %d", len(cs))
	}
	c := cs[0]
	if c.File != "foo.go" || c.Old != "foo.go" {
		t.Errorf("File=%q Old=%q", c.File, c.Old)
	}
	if len(c.Dels) != 1 || c.Dels[0].Text != "func old() {}" || c.Dels[0].No != 3 {
		t.Errorf("Dels=%+v", c.Dels)
	}
	if len(c.Adds) != 1 || c.Adds[0].Text != "func new() {}" || c.Adds[0].No != 3 {
		t.Errorf("Adds=%+v", c.Adds)
	}
}
