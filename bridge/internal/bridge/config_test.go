package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicConfiguration(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	for _, item := range []struct {
		body string
		ok   bool
	}{
		{`{"deepseek_api_key":"synthetic-test-key"}`, true},
		{`{"deepseek_api_key":"YOUR_DEEPSEEK_API_KEY"}`, false},
		{`{"deepseek_api_key":"synthetic-test-key","listen":"0.0.0.0:18181"}`, false},
		{`{"deepseek_api_key":"synthetic-test-key","api_base":"http://example.org"}`, false},
		{`{"deepseek_api_key":"synthetic-test-key","unexpected":true}`, false},
		{`{"deepseek_api_key":"synthetic-test-key"} {}`, false},
	} {
		os.WriteFile(p, []byte(item.body), 0600)
		_, e := LoadConfig(p)
		if (e == nil) != item.ok {
			t.Fatalf("validation outcome: %v", e)
		}
	}
}
func TestSyntheticChemistry(t *testing.T) {
	for _, s := range []string{`\ce{Fe^{3+} + e- -> Fe^{2+}}`, `\ce{NaCl}`} {
		v := normalizeChemistryLatex(s)
		if v == s {
			t.Fatal("mhchem not normalized")
		}
	}
}
