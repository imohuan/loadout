package fieldfilter

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestApplyFieldRules(t *testing.T) {
	must := func(out []byte) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("输出非法 JSON: %v\n%s", err, out)
		}
		return m
	}
	t.Run("strip 顶层 key", func(t *testing.T) {
		out := applyFieldRules([]byte(`{"model":"x","client_metadata":{"a":1}}`), nil, []string{"client_metadata"})
		m := must(out)
		if _, ok := m["client_metadata"]; ok {
			t.Fatalf("client_metadata 未剔除: %s", out)
		}
		if _, ok := m["model"]; !ok {
			t.Fatalf("model 被误删: %s", out)
		}
	})
	t.Run("strip 嵌套点路径", func(t *testing.T) {
		out := applyFieldRules([]byte(`{"foo":{"bar":1,"baz":2}}`), nil, []string{"foo.bar"})
		m := must(out)
		foo, ok := m["foo"].(map[string]any)
		if !ok {
			t.Fatalf("foo 缺失: %s", out)
		}
		if _, ok := foo["bar"]; ok {
			t.Fatalf("foo.bar 未剔除: %s", out)
		}
		if _, ok := foo["baz"]; !ok {
			t.Fatalf("foo.baz 被误删: %s", out)
		}
	})
	t.Run("strip 不存在路径原字节返回", func(t *testing.T) {
		body := []byte(`{"model":"x"}`)
		if out := applyFieldRules(body, nil, []string{"nope", "a.b.c"}); !bytes.Equal(out, body) {
			t.Fatalf("无命中应返回原字节: %s", out)
		}
	})
	t.Run("strip 中间节点非对象跳过", func(t *testing.T) {
		body := []byte(`{"foo":42}`)
		if out := applyFieldRules(body, nil, []string{"foo.bar"}); !bytes.Equal(out, body) {
			t.Fatalf("中间节点非对象应原字节返回: %s", out)
		}
	})
	t.Run("keep 白名单", func(t *testing.T) {
		out := applyFieldRules([]byte(`{"model":"x","messages":[],"client_metadata":{}}`), []string{"model"}, nil)
		m := must(out)
		if _, ok := m["model"]; !ok {
			t.Fatal("model 被删")
		}
		if _, ok := m["messages"]; ok {
			t.Fatal("messages 应被白名单删掉")
		}
		if _, ok := m["client_metadata"]; ok {
			t.Fatal("client_metadata 应被白名单删掉")
		}
	})
	t.Run("keep 与 strip 同配 keep 优先", func(t *testing.T) {
		out := applyFieldRules([]byte(`{"model":"x","keep_me":1,"strip_me":2}`), []string{"model", "keep_me"}, []string{"strip_me"})
		m := must(out)
		if _, ok := m["keep_me"]; !ok {
			t.Fatal("keep_me 应保留")
		}
		if _, ok := m["strip_me"]; ok {
			t.Fatal("strip_me 应被白名单删掉")
		}
	})
	t.Run("keep 无命中原字节返回", func(t *testing.T) {
		body := []byte(`{"model":"x"}`)
		if out := applyFieldRules(body, []string{"model"}, nil); !bytes.Equal(out, body) {
			t.Fatalf("keep 无命中应原字节返回: %s", out)
		}
	})
	t.Run("非 JSON 原样", func(t *testing.T) {
		body := []byte(`not json`)
		if out := applyFieldRules(body, nil, []string{"x"}); !bytes.Equal(out, body) {
			t.Fatalf("非 JSON 应原样: %s", out)
		}
	})
	t.Run("顶层数组原样", func(t *testing.T) {
		body := []byte(`[{"a":1}]`)
		if out := applyFieldRules(body, nil, []string{"a"}); !bytes.Equal(out, body) {
			t.Fatalf("顶层数组应原样: %s", out)
		}
	})
	t.Run("空 body 原样", func(t *testing.T) {
		var body []byte
		if out := applyFieldRules(body, nil, []string{"x"}); !bytes.Equal(out, body) {
			t.Fatalf("空 body 应原样: %s", out)
		}
	})
	t.Run("数字精度无损", func(t *testing.T) {
		out := applyFieldRules([]byte(`{"n":12345678901234567890,"keep":1}`), nil, []string{"drop"})
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["n"]; !ok {
			t.Fatal("n 被误删")
		}
	})
}
