package main

import (
	"os"
	"path/filepath"
	"testing"
)

// v1.0.98 安全修复的行为验证。核心目标有两个，缺一不可：
//   A. 真实攻击 payload 必须被拦下（否则修复无效）；
//   B. 正常配置必须能保存（否则修复把功能打死）。
// B 常被忽略，因此这里用 helper 生成的真实 server.conf 做"原样保存"回归。

func TestServerConf_BlocksDangerous(t *testing.T) {
	// 基准文件：只含基础设施行，模拟真实 server.conf
	base := "ca /pki/ca.crt\ncert /pki/s.crt\nkey /pki/s.key\nstatus /data/st.log\n" +
		"client-connect /bin/ovpn-helper.sh\nscript-security 3\n"

	cases := []struct{ name, content string }{
		{"plugin 加载 so", base + "plugin /usr/lib/evil.so"},
		{"up 执行 shell", base + "up \"/bin/sh -c id\""},
		{"down 执行 shell", base + "down /tmp/x.sh"},
		{"route-up", base + "route-up /tmp/x.sh"},
		{"tls-verify", base + "tls-verify /tmp/x.sh"},
		{"askpass 读口令文件", base + "askpass /etc/shadow"},
		{"chroot 逃逸", base + "chroot /tmp"},
		{"双横线前缀绕过", base + "--up /bin/sh"},
		{"大写绕过", base + "UP /bin/sh"},
		{"tab 分隔绕过", base + "up\t/bin/sh"},
		{"命令替换反引号", base + "push \"a `id`\""},
		{"命令替换 $()", base + "push \"a $(id)\""},
		// pinned 行改写
		{"改写 ca 指向 /etc/shadow", "ca /etc/shadow\n"},
		{"改写 client-connect 提权", base + "client-connect /tmp/evil.sh"},
		{"改写 script-security", base + "script-security 2"},
		{"改写 key 路径", base + "key /tmp/evil.key"},
		{"新增 status 输出", base + "status /tmp/evil.log"},
		{"新增 setenv", base + "setenv LD_PRELOAD /tmp/x.so"},
		// 未知指令
		{"未知指令", base + "totally-unknown-directive 1"},
	}

	for _, tc := range cases {
		if bad, _ := validateServerConf(tc.content, base); bad == 0 {
			t.Errorf("[%s] 未被拒绝，期望拦截: %q", tc.name, tc.content)
		}
	}
}

// TestServerConf_AllowsUnchangedSave 是最关键的回归：
// helper 生成的真实 server.conf 打开后不做任何修改直接保存，必须放行。
func TestServerConf_AllowsUnchangedSave(t *testing.T) {
	real, err := os.ReadFile(filepath.Join("..", "testdata", "real_server.conf"))
	if err != nil {
		t.Skipf("跳过（缺少 testdata/real_server.conf）: %v", err)
	}
	if bad, reason := validateServerConf(string(real), string(real)); bad != 0 {
		t.Errorf("未修改的原配置被拒，功能会被打死: 第 %d 行 %s", bad, reason)
	}
}

// TestServerConf_AllowsCommonEdits 覆盖用户真正会做的修改。
func TestServerConf_AllowsCommonEdits(t *testing.T) {
	base := "ca /pki/ca.crt\ncert /pki/s.crt\nkey /pki/s.key\nstatus /data/st.log\n" +
		"client-connect /bin/ovpn-helper.sh\nscript-security 3\nport 1194\nproto udp\n"

	edits := []string{
		base + "push \"dhcp-option DNS 223.5.5.5\"\n",
		base + "tun-mtu 1400\nmssfix 1360\n",
		base + "# 我加的注释\nverb 3\n",
		base + "max-clients 200\nclient-to-client\nduplicate-cn\n",
		base + "cipher AES-256-GCM\ndata-ciphers AES-256-GCM\n",
		base + "tls-version-min 1.2\ntls-cipher TLS-ECDHE-ECDSA-WITH-AES-128-GCM-SHA256\n",
		base + "keepalive 10 60\npersist-key\npersist-tun\n",
		base + "route 192.168.1.0 255.255.255.0\n",
		base + "explicit-exit-notify 1\n",
		base + "compress lz4-v2\n",
	}

	for i, e := range edits {
		if bad, reason := validateServerConf(e, base); bad != 0 {
			t.Errorf("编辑 #%d 被误拒: %s\n%s", i, reason, e)
		}
	}
}

// TestServerConf_PinnedMustBeInherited 验证 pinned 行"只能继承不能改写"的语义：
// 值与基准一致 → 放行；值不同 → 拒绝。
func TestServerConf_PinnedMustBeInherited(t *testing.T) {
	base := "ca /pki/ca.crt\nstatus /data/st.log\n"

	// 原样继承（含多余空白差异）应放行
	if bad, reason := validateServerConf("ca   /pki/ca.crt\nstatus /data/st.log\n", base); bad != 0 {
		t.Errorf("多余空白的继承被误拒: %s", reason)
	}

	// 改写应拒绝
	if bad, _ := validateServerConf("ca /tmp/evil.crt\nstatus /data/st.log\n", base); bad == 0 {
		t.Errorf("改写 ca 未被拒")
	}

	// 删除 pinned 行后再新增同值：仍是同值继承，放行（不强制必须保留）
	if bad, _ := validateServerConf("status /data/st.log\n", base); bad != 0 {
		t.Errorf("仅删除 ca 应放行: 第 %d 行", bad)
	}
}

func TestClientConfig_BlocksDangerous(t *testing.T) {
	for _, s := range []string{
		"up /bin/sh", "plugin /tmp/e.so", "script-security 3",
		"--up /bin/sh", "UP /bin/sh", "route-up /tmp/x",
		"ca /etc/passwd", "status /tmp/x", "setenv X Y",
		"push \"a `id`\"", "push \"a $(id)\"",
	} {
		if bad, _ := validateConfigLines(s, false); bad == 0 {
			t.Errorf("客户端配置未拦截: %q", s)
		}
	}
}

// TestClientConfig_AllowsNormal 客户端自定义配置必须能正常下发常用指令
// （分组路由/DNS 下发是这个功能的全部意义，误拒等于功能报废）。
func TestClientConfig_AllowsNormal(t *testing.T) {
	for _, s := range []string{
		"push \"route 192.168.1.0 255.255.255.0\"",
		"push \"dhcp-option DNS 223.5.5.5\"",
		"push \"redirect-gateway def1\"",
		"push \"block-outside-dns\"",
		"comp-lzo yes",
		"# 注释",
		"",
	} {
		if bad, reason := validateConfigLines(s, false); bad != 0 {
			t.Errorf("客户端常规配置被误拒: %q -> %s", s, reason)
		}
	}
}

func TestValidateIPList(t *testing.T) {
	bad := []string{
		"1.2.3.4 } add rule inet openvpn-nft forward accept #",
		"not-an-ip",
		"1.2.3.4; rm -rf /",
		"999.999.999.999",
		"1.2.3.4, evil",
	}
	for _, s := range bad {
		if err := validateIPList(s); err == nil {
			t.Errorf("非法 IP 未被拒: %q", s)
		}
	}

	good := []string{"10.8.0.5", "10.8.0.5, 10.8.0.6", "fdaf::1", "", "1.2.3.4, "}
	for _, s := range good {
		if err := validateIPList(s); err != nil {
			t.Errorf("合法 IP 被误拒: %q (%v)", s, err)
		}
	}
}

func TestValidServerAddr(t *testing.T) {
	bad := []string{
		"1.2.3.4 1194\nup /bin/sh",
		"example.com; id",
		"a b",
		"[::1] extra",
		"$(id)",
		"a`b`",
	}
	for _, s := range bad {
		if validServerAddr(s) {
			t.Errorf("非法地址未被拒: %q", s)
		}
	}

	good := []string{"vpn.example.com", "1.2.3.4", "[2001:db8::1]", "2001:db8::1", "a"}
	for _, s := range good {
		if !validServerAddr(s) {
			t.Errorf("合法地址被误拒: %q", s)
		}
	}
}

func TestRateValidation(t *testing.T) {
	if err := validateRateValue("100"); err != nil {
		t.Errorf("合法速率被拒: %v", err)
	}
	for _, s := range []string{"-1", "abc", "10; rm", ""} {
		if err := validateRateValue(s); err == nil {
			t.Errorf("非法速率未被拒: %q", s)
		}
	}
	if err := validateRateUnit("kbytes/second"); err != nil {
		t.Errorf("合法单位被拒: %v", err)
	}
	if err := validateRateUnit("kbytes/second } add rule"); err == nil {
		t.Errorf("非法单位未被拒")
	}
}
