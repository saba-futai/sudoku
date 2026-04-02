package config

import "testing"

func TestDefaultPACRuleURLs(t *testing.T) {
	urls := DefaultPACRuleURLs()

	for _, url := range urls {
		if url == "https://fastly.jsdelivr.net/gh/blackmatrix7/ios_rule_script@master/rule/Clash/BiliBili/BiliBili.list" {
			t.Fatalf("unexpected bilibili default rule")
		}
		if url == "https://fastly.jsdelivr.net/gh/blackmatrix7/ios_rule_script@master/rule/Clash/WeChat/WeChat.list" {
			t.Fatalf("unexpected wechat default rule")
		}
	}

	foundReject := false
	for _, url := range urls {
		if url == RejectRulePrefix+DefaultPACRejectRuleURL {
			foundReject = true
			break
		}
	}
	if !foundReject {
		t.Fatalf("expected default PAC reject rule")
	}
}

func TestRuntimeRuleURLs(t *testing.T) {
	direct, reject := RuntimeRuleURLs("pac", []string{
		"https://example.com/direct.list",
		"!https://example.com/reject-1.yaml",
		"！https://example.com/reject-2.yaml",
	})

	if len(direct) != 1 || direct[0] != "https://example.com/direct.list" {
		t.Fatalf("unexpected direct rules: %v", direct)
	}
	if len(reject) != 2 || reject[0] != "https://example.com/reject-1.yaml" || reject[1] != "https://example.com/reject-2.yaml" {
		t.Fatalf("unexpected reject rules: %v", reject)
	}

	direct, reject = RuntimeRuleURLs("global", nil)
	if len(direct) != 0 {
		t.Fatalf("global should not add direct rules: %v", direct)
	}
	if len(reject) != 1 || reject[0] != DefaultImplicitRejectRuleURL {
		t.Fatalf("unexpected implicit reject rules: %v", reject)
	}
}
