package deps

import "testing"

func TestParseNpmLsJSON(t *testing.T) {
	// 已装
	ver, inst, err := parseNpmLsJSON(`{"name":"npm","dependencies":{"unifyai":{"version":"1.4.0","overridden":false}}}`, "unifyai")
	if err != nil || !inst || ver != "1.4.0" {
		t.Errorf("已装解析失败: ver=%q inst=%v err=%v", ver, inst, err)
	}

	// 未装（只有 name，无 dependencies）
	_, inst2, err2 := parseNpmLsJSON(`{"name":"npm"}`, "unifyai")
	if err2 != nil || inst2 {
		t.Errorf("未装应返回 inst=false, got inst=%v err=%v", inst2, err2)
	}

	// 空输出
	_, inst3, err3 := parseNpmLsJSON("", "unifyai")
	if err3 != nil || inst3 {
		t.Errorf("空输出应返回 inst=false, got inst=%v err=%v", inst3, err3)
	}

	// 坏 JSON
	if _, _, err := parseNpmLsJSON(`{bad`, "unifyai"); err == nil {
		t.Error("坏 JSON 应报错")
	}
}

func TestParseDistTagsJSON(t *testing.T) {
	latest, err := parseDistTagsJSON(`{"latest":"1.4.0"}`)
	if err != nil || latest != "1.4.0" {
		t.Errorf("解析 latest 失败: latest=%q err=%v", latest, err)
	}

	if _, err := parseDistTagsJSON(`{}`); err == nil {
		t.Error("无 latest 应报错")
	}

	if _, err := parseDistTagsJSON(`{bad`); err == nil {
		t.Error("坏 JSON 应报错")
	}
}

func TestStatusComputation(t *testing.T) {
	st := Status{Name: "unifyai", Installed: true, Current: "1.0.0", Latest: "1.4.0"}
	st.NeedUpdate = st.Installed && st.Latest != "" && st.Current != st.Latest
	if !st.NeedUpdate {
		t.Error("1.0.0 vs 1.4.0 应判定为需更新")
	}

	st2 := Status{Name: "unifyai", Installed: true, Current: "1.4.0", Latest: "1.4.0"}
	st2.NeedUpdate = st2.Installed && st2.Latest != "" && st2.Current != st2.Latest
	if st2.NeedUpdate {
		t.Error("1.4.0 vs 1.4.0 不应判定为需更新")
	}

	st3 := Status{Name: "skills", Installed: false, Latest: "1.5.23"}
	st3.NeedUpdate = st3.Installed && st3.Latest != "" && st3.Current != st3.Latest
	if st3.NeedUpdate {
		t.Error("未安装不应判定为需更新")
	}
}
