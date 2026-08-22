package fieldfilter

import "testing"

func TestManifest(t *testing.T) {
	p := New()
	m := p.Manifest()
	if m.Name != "field-filter" {
		t.Fatalf("插件名 = %s, 期望 field-filter", m.Name)
	}
	if m.Version == "" {
		t.Fatal("Version 为空")
	}
	if len(m.Inject) == 0 || len(m.Provide) == 0 {
		t.Fatalf("Inject/Provide 不应为空: %+v", m)
	}
}
