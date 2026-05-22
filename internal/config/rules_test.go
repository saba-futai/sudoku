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
	direct, proxy, reject := RuntimeRuleURLs("pac", []string{
		"https://example.com/direct.list",
		"-https://example.com/proxy-1.yaml",
		"_https://example.com/proxy-2.yaml",
		"!https://example.com/reject-1.yaml",
		"！https://example.com/reject-2.yaml",
		"?https://example.com/reject-3.yaml",
	})

	if len(direct) != 1 || direct[0] != "https://example.com/direct.list" {
		t.Fatalf("unexpected direct rules: %v", direct)
	}
	if len(proxy) != 2 || proxy[0] != "https://example.com/proxy-1.yaml" || proxy[1] != "https://example.com/proxy-2.yaml" {
		t.Fatalf("unexpected proxy rules: %v", proxy)
	}
	if len(reject) != 3 || reject[0] != "https://example.com/reject-1.yaml" || reject[1] != "https://example.com/reject-2.yaml" || reject[2] != "https://example.com/reject-3.yaml" {
		t.Fatalf("unexpected reject rules: %v", reject)
	}

	direct, proxy, reject = RuntimeRuleURLs("global", nil)
	if len(direct) != 0 {
		t.Fatalf("global should not add direct rules: %v", direct)
	}
	if len(proxy) != 0 {
		t.Fatalf("global should not add proxy rules: %v", proxy)
	}
	if len(reject) != 1 || reject[0] != DefaultImplicitRejectRuleURL {
		t.Fatalf("unexpected implicit reject rules: %v", reject)
	}
}
