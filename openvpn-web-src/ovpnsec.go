package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

// 本文件集中放置「用户可控内容写入特权配置」前的校验逻辑。
//
// 背景：server.conf 以 root 权限被 openvpn 加载，且本应用为支持 client-connect /
// auth-user-pass-verify 钩子必须开启 script-security 3，即 server.conf 中出现
// up / plugin / client-connect 等指令时 openvpn 会**以 root 执行对应程序**。
// 管理界面的「在线编辑 server.conf」（main.go updateConfig）此前把表单内容原样
// O_TRUNC 写回文件，管理员账号一旦被劫持（或遭 CSRF）即可写入
// `plugin /path/evil.so` 直接拿到 root。genclient 的自定义配置字段同理，
// 可往客户端 .ovpn 注入 redirect-gateway / up 等指令劫持客户端流量。
//
// 策略：白名单 / 黑名单双层——黑名单先剔除一切"可执行"语义指令（无论出现在
// 哪一行、大小写、前置 -- 均可），白名单再限制普通配置允许出现的指令名，
// 两者任一不过即整体拒绝（不做"改写后继续"，避免语义被悄悄改变）。

// dangerousDirectives 是绝对禁止出现在用户可控配置里的指令——
// 它们在 helper 生成的 server.conf 中**原本就不存在**，故一律拒绝不影响正常功能。
// 判定用「首个空白符前的 token」比对，故 `up /bin/sh`、`--up`、`UP\tx` 都无法绕过。
var dangerousDirectives = map[string]bool{
	// 直接以 root 执行外部程序 / 加载模块（本应用 script-security 为 3）
	"up": true, "down": true, "plugin": true, "tls-verify": true,
	"ipchange": true, "client-crresponse": true, "route-up": true,
	"route-pre-down": true,
	// 读取外部文件 / 逃逸面
	"askpass": true, "auth-token-secret-file": true, "chroot": true, "engine": true,
}

// pinnedDirectives 是"基础设施"指令：helper 生成的 server.conf 本身就含它们
// （ca / cert / key / status / management / client-connect / script-security …），
// 用户在编辑器里能看到、**也必须能原样保存**（否则"打开配置 → 不加修改直接保存"
// 会被拒，正常功能被打死）。但它们的值一旦被改写就会把 openvpn 指向攻击者控制的
// 路径（ca /etc/shadow、client-connect /tmp/evil.sh），故要求值与当前文件
// **逐字一致**——只能继承，不能改写。见 validateServerConf。
var pinnedDirectives = map[string]bool{
	// 证书与密钥路径
	"ca": true, "cert": true, "key": true, "tls-crypt": true, "tls-auth": true,
	"crl-verify": true, "secret": true,
	// 钩子脚本（本应用的认证与连接记录依赖它们，改写即 root 代码执行）
	"auth-user-pass-verify": true, "client-connect": true,
	"client-disconnect": true, "learn-address": true, "script-security": true,
	// 输出与控制通道
	"status": true, "client-config-dir": true, "management": true,
	"writepid": true, "log": true, "log-append": true, "daemon": true,
	"setenv": true,
}

// serverConfAllow 是 server.conf 在线编辑允许出现的指令白名单。
// 只列"改了不会导致提权或路径外泄"的常规网络/加密参数。
var serverConfAllow = map[string]bool{
	// 基本
	"port": true, "proto": true, "dev": true, "dev-type": true, "topology": true,
	"server": true, "server-ipv6": true, "ifconfig-pool-persist": true,
	"keepalive": true, "ping": true, "ping-restart": true, "ping-timer-rem": true,
	"persist-key": true, "persist-tun": true, "user": true, "group": true,
	"verb": true, "mute": true, "status-version": true, "machine-readable-output": true,
	// 路由与推送
	"push": true, "route": true, "route-gateway": true, "route-ipv6": true,
	"redirect-gateway": true, "redirect-private": true, "client-to-client": true,
	"duplicate-cn": true, "max-clients": true, "ifconfig-pool": true,
	"client-config-dir-x": true,
	// 加密
	"cipher": true, "data-ciphers": true, "data-ciphers-fallback": true,
	"auth": true, "tls-server": true, "tls-client": true, "tls-version-min": true,
	"tls-version-max": true, "tls-cipher": true, "tls-ciphersuites": true,
	"tls-groups": true, "tls-cert-profile": true, "dh": true, "ecdh-curve": true,
	"reneg-sec": true, "reneg-bytes": true, "reneg-pkts": true, "auth-nocache": true,
	// 网络细节
	"tun-mtu": true, "link-mtu": true, "mssfix": true, "fragment": true,
	"mtu-disc": true, "mtu-test": true, "sndbuf": true, "rcvbuf": true,
	"txqueuelen": true, "comp-lzo": true, "compress": true, "comp-noadapt": true,
	"local": true, "remote": true, "explicit-exit-notify": true,
	"float": true, "multihome": true, "bind": true, "connect-retry": true,
	"resolv-retry": true, "nobind": true, "passtos": true, "socket-flags": true,
	"fast-io": true, "shaper": true, "inactive": true, "remap-usr1": true,
	"ccd-exclusive": true, "opt-verify": true, "disable-occ": true,
	// 客户端侧（.ovpn 复用同一校验器）
	"client": true, "remote-cert-tls": true, "verify-x509-name": true,
	"ns-cert-type": true, "auth-user-pass": true, "pull": true, "resolv-retry-infinite": true,
	"static-challenge": true, "block-outside-dns": true, "remote-random": true,
	"dhcp-option": true, "register-dns": true, "allow-pull-fqdn": true,
	"connect-timeout": true, "server-poll-timeout": true, "key-direction": true,
	"pull-filter": true, "ifconfig": true, "ifconfig-ipv6": true,
}

// directiveToken 取一行配置的指令名：去掉行首空白、可选 `--` 前缀、可选 `<section>` 标记，
// 返回首个空白符之前的 token（小写）。空行与纯注释行返回 ""。
func directiveToken(line string) string {
	t := strings.TrimSpace(line)
	// 内嵌证书块（<ca>/<cert>/<key>/<tls-crypt>）由 helper 生成，不在用户可控范围内；
	// 但为安全起见，用户内容中出现任何 <...> 块标记一律拒绝（防止伪造 CA）。
	if strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">") {
		return strings.ToLower(strings.Trim(t, "<>"))
	}
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
		return ""
	}
	t = strings.TrimPrefix(t, "--")
	if i := strings.IndexAny(t, " \t"); i >= 0 {
		t = t[:i]
	}
	return strings.ToLower(t)
}

// confLine 是解析后的一行配置：原始文本、指令名、以及"指令名 + 参数"归一化串。
type confLine struct {
	raw  string
	tok  string
	full string // 归一化后的 "指令 参数"，用于继承比对（忽略多余空白）
}

// normalizeConfLine 把一行压成 "指令 参数"：指令名小写，内部空白折叠为单空格。
func normalizeConfLine(line string) string {
	return strings.Join(strings.Fields(line), " ")
}

// parseConf 把配置文本解析为带行号的指令列表（跳过空行与纯注释行）。
func parseConf(content string) []confLine {
	var out []confLine
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		tok := directiveToken(line)
		if tok == "" {
			continue
		}
		out = append(out, confLine{raw: line, tok: tok, full: strings.ToLower(normalizeConfLine(line))})
	}
	return out
}

// indexByToken 建立 "指令名+参数" → 出现次数 的索引，用于继承校验。
func indexByToken(lines []confLine) map[string]int {
	m := make(map[string]int, len(lines))
	for _, l := range lines {
		m[l.full]++
	}
	return m
}

// validateServerConf 校验在线编辑提交的 server.conf。
//
// 三层策略（v1.0.98）：
//   ① 危险指令：helper 生成的文件里本就不存在（up/plugin/down/route-up…），一律拒绝；
//   ② 基础设施指令（pinned）：ca/cert/key/status/client-connect/script-security 等
//      原本就存在于 server.conf，必须允许原样保存，但**值必须与当前文件完全一致**
//      ——只能继承、不能改写（改写即把 openvpn 指向攻击者路径 → root 代码执行）；
//   ③ 其余指令：需在常规指令白名单内（只影响网络/加密参数，无提权面）。
//
// current 为磁盘上现有的 server.conf 内容（作为继承基准）；为空表示首次写入。
func validateServerConf(newContent, current string) (badLine int, reason string) {
	base := indexByToken(parseConf(current))

	for i, l := range parseConf(newContent) {
		// ① 危险指令（无论值是否变化）
		if dangerousDirectives[l.tok] {
			return i + 1, fmt.Sprintf("第 %d 行包含被禁用的指令 %q：该指令可导致以 root 执行外部程序", i+1, l.tok)
		}

		// ② 基础设施指令：必须与现有值逐字一致（可继承、不可改写）
		if pinnedDirectives[l.tok] {
			if base[l.full] <= 0 {
				return i + 1, fmt.Sprintf("第 %d 行的 %q 属于系统保留配置（证书/密钥/钩子/输出路径），不允许新增或改写", i+1, l.tok)
			}
			base[l.full]-- // 消费一次，防止删除后重复计数绕过
			continue
		}

		// ③ 常规指令白名单
		if !serverConfAllow[l.tok] {
			return i + 1, fmt.Sprintf("第 %d 行的指令 %q 不在允许列表内", i+1, l.tok)
		}

		// 命令替换符号：openvpn 自身不解 shell，但内容会流经 helper 的
		// `echo -e "$4"` 等 shell 环节，一律拒绝以防解析器差异被利用。
		if strings.Contains(l.raw, "`") || strings.Contains(l.raw, "$(") {
			return i + 1, fmt.Sprintf("第 %d 行包含命令替换符号（` 或 $(），已拒绝", i+1)
		}
	}

	return 0, ""
}

// validateConfigLines 校验客户端自定义配置（.ovpn 片段 / ccd 片段）。
//
// 与 serverConf 不同：这里没有"现有文件"作继承基准，故只做两层——
// 危险指令拒绝 + 命令替换符号拒绝。客户端侧允许 hub 不常见指令（如 client、
// remote-cert-tls），因此不套 serverConfAllow 白名单。
//
// 注意：客户端配置里出现 ca/cert/key 之类是**内联证书块**（<ca>…</ca>），
// 由 directiveToken 走 <...> 分支判定，会被 dangerousDirectives 里的
// 同名项拦截前先命中 pinned 检查——这里统一按"伪造证书块"拒绝，避免受害者
// 被下发攻击者自签 CA。
func validateConfigLines(content string, allowWhite bool) (badLine int, reason string) {
	lines := parseConf(content)
	for i, l := range lines {
		if dangerousDirectives[l.tok] {
			return i + 1, fmt.Sprintf("第 %d 行包含被禁止的指令 %q：该指令可导致执行外部程序或读取敏感文件", i+1, l.tok)
		}
		if pinnedDirectives[l.tok] {
			return i + 1, fmt.Sprintf("第 %d 行的 %q 不允许在客户端配置中出现", i+1, l.tok)
		}
		if allowWhite && !serverConfAllow[l.tok] {
			return i + 1, fmt.Sprintf("第 %d 行的指令 %q 不在允许列表内", i+1, l.tok)
		}
		if strings.Contains(l.raw, "`") || strings.Contains(l.raw, "$(") {
			return i + 1, fmt.Sprintf("第 %d 行包含命令替换符号（` 或 $(），已拒绝", i+1)
		}
	}
	return 0, ""
}

// validateIPList 校验逗号分隔的 IP 地址列表，全部必须是合法 IP（v4 或 v6）。
// 用于 nft 规则拼接前的输入校验——nft 走 stdin 而非 shell 故无命令注入，
// 但攻击者可用 `1.2.3.4 } add rule ... #` 闭合语法块注入任意 nft 规则
// （删掉自己的限速/黑名单规则，或 drop 他人流量）。
func validateIPList(ips string) error {
	for ip := range strings.SplitSeq(ips, ",") {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		if net.ParseIP(ip) == nil {
			return fmt.Errorf("非法 IP 地址: %q", ip)
		}
	}
	return nil
}

// validateRateValue 校验限速数值：必须是非负整数，避免往 nft 命令行塞入非预期 token。
func validateRateValue(v string) error {
	if v == "" {
		return fmt.Errorf("速率不能为空")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fmt.Errorf("非法速率值: %q", v)
	}
	return nil
}

// validateRateUnit 校验限速单位，只接受 nft 支持的三个值。
func validateRateUnit(u string) error {
	switch u {
	case "packets/second", "kbytes/second", "mbytes/second":
		return nil
	}
	return fmt.Errorf("非法速率单位: %q", u)
}

// csrfSafeMethods 不需要 CSRF 校验的方法：读操作与 O-Token 特权通道。
var csrfSafeMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodOptions: true,
}

// CSRFMiddleWare 对所有状态变更请求做同源校验。
//
// 背景（v1.0.98）：本应用是管理 root 级 VPN 服务的面板，全部写接口
// （改管理员密码、写 server.conf、吊销证书、nft 规则、重启服务）此前
// 没有任何 CSRF 防护，恶意页面可借管理员浏览器会话静默改配置。
//
// 校验顺序（任一通过即放行）：
//   ① 带有效 O-Token 的本机请求（openvpn 钩子走这条，无浏览器 cookie 语义）；
//   ② 自定义头 X-Requested-With（跨站表单无法设置自定义头，除非 CORS 显式放行，
//      而本应用不返回任何 CORS 头，故跨站 fetch 也会被浏览器拦下）；
//   ③ Origin / Referer 同源校验（兜底表单直接提交场景）。
//
// 说明：这里不引入 token 存储态，避免改动前端所有表单；X-Requested-With +
// Origin 双重校验对现代浏览器已等价于 token 方案，且无需改前端即可生效。
func CSRFMiddleWare() gin.HandlerFunc {
	isSafe := func(r *http.Request) bool {
		if csrfSafeMethods[r.Method] {
			return true
		}
		// O-Token 特权通道（openvpn 钩子）：本机回环 + 有效 token，非浏览器场景
		if tok := viper.GetString("system.base.token"); tok != "" {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("O-Token")), []byte(tok)) == 1 {
				if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
					if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
						return true
					}
				}
			}
		}
		return false
	}

	return func(c *gin.Context) {
		if isSafe(c.Request) {
			c.Next()
			return
		}

		// 自定义头：跨站脚本无法在无 CORS 许可时设置
		if c.GetHeader("X-Requested-With") == "XMLHttpRequest" {
			c.Next()
			return
		}

		// Origin（优先）或 Referer 必须与本站同源
		if sameOrigin(c) {
			c.Next()
			return
		}

		logger.Error(context.Background(),
			fmt.Sprintf("CSRF 拒绝: %s %s origin=%q referer=%q remote=%q",
				c.Request.Method, c.Request.URL.Path,
				c.GetHeader("Origin"), c.GetHeader("Referer"), c.ClientIP()))
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": "跨站请求被拒绝（CSRF 校验失败）"})
	}
}

// sameOrigin 判断 Origin / Referer 是否与当前请求的 Host 同源。
// 两者都缺失时按不信任处理（直接表单提交一定带 Origin 或 Referer 之一）。
func sameOrigin(c *gin.Context) bool {
	host := c.Request.Host
	if host == "" {
		return false
	}

	check := func(v string) bool {
		if v == "" {
			return false
		}
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			return strings.EqualFold(u.Host, host)
		}
		return false
	}

	return check(c.GetHeader("Origin")) || check(c.GetHeader("Referer"))
}

// SecurityHeaders 给管理后台补上缺失的响应头加固。
// 缺 X-Frame-Options 时任意站点可 iframe 嵌入本面板做点击劫持；
// CSP 同时收紧脚本与框架来源（内嵌资源全部同源，无需外部域）。
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; connect-src 'self'; frame-ancestors 'self'; object-src 'none'; base-uri 'self'")
		c.Next()
	}
}

// isSecureRequest 判断当前请求是否走 HTTPS。
// 只有明确证据（请求自带 TLS 或前置网关注入 X-Forwarded-Proto: https）才认定 https；
// 统一网关 unix socket 通道 RemoteAddr 为空但协议未知——若浏览器经 http 访问 NAS，
// 盲设 Secure 会让浏览器丢弃 session cookie，表现为登录成功后每个 API 302、
// 页面无限刷新循环（v1.0.98 回归）。无法确认协议时不设 Secure：明文风险由
// 管理口不对外监听（仅回环/网关 socket）兜底。
func isSecureRequest(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	if strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

// hostRe 允许的服务器地址形态：域名、IPv4、IPv6（方括号可选）。
// 不含空白与 shell 元字符，故写进 .ovpn 的 remote 行无法被追加额外参数。
var hostRe = regexp.MustCompile(`^\[?[A-Za-z0-9]([A-Za-z0-9._:-]*[A-Za-z0-9])?\]?$`)

// validServerAddr 校验 genclient 传入的服务器地址。
// 拒绝空白、引号、分号、反引号等，防止 remote 行注入（如 `1.2.3.4 1194\nup /bin/sh`）。
func validServerAddr(addr string) bool {
	if len(addr) > 255 {
		return false
	}
	if !hostRe.MatchString(addr) {
		return false
	}
	// 方括号必须成对（IPv6 语法）
	return strings.Count(addr, "[") == strings.Count(addr, "]") &&
		strings.Count(addr, "[") <= 1
}
