package emailcontent

import (
	"strings"
	"testing"
)

func TestSanitizeHTMLRemovesActiveAndRemoteContent(t *testing.T) {
	got := SanitizeHTML(`<p onclick="steal()">Hello <strong>there</strong></p><img src="https://tracker.example/pixel"><script>alert(1)</script><a href="javascript:alert(1)">bad</a>`)
	for _, forbidden := range []string{"script", "onclick", "img", "tracker.example", "javascript:"} {
		if strings.Contains(strings.ToLower(got), forbidden) {
			t.Fatalf("SanitizeHTML() retained %q in %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "<strong>there</strong>") {
		t.Fatalf("SanitizeHTML() removed safe markup: %q", got)
	}
}
