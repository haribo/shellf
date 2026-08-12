package lang

import (
	"strings"
	"testing"
)

func TestParseTripleQuotedString(t *testing.T) {
	src := `
on server {
  file.write("/etc/app.conf", """
port=8080
workers=4
""")
}
`
	plan, err := ParsePlan(src)
	if err != nil {
		t.Fatal(err)
	}
	content := plan[0].Steps[0].Args["content"]
	if !strings.Contains(content, "port=8080\n") || !strings.Contains(content, "workers=4") {
		t.Fatalf("multi-line content not preserved: %q", content)
	}
	if !strings.Contains(content, "\n") {
		t.Fatal("newlines were lost")
	}
}
