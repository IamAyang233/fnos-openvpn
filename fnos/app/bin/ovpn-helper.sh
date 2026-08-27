#!/bin/bash
# OpenVPN FPK 证书/客户端生成辅助脚本
# 替代原 docker-entrypoint.sh 的调用，去除 docker 依赖，
# 适配纯 FPK（二进制与 easyrsa/jq 均随包内置在 app/bin）。
set -e

# 脚本自身所在目录 = app/bin
APP_BIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 所有数据都在 OVPN_DATA 下（指向数据卷 etc/）。
# 优先级：显式环境变量 > TRIM_PKGVAR（fnOS 生命周期注入）> 官方 /var/apps/{app}/var symlink > 脚本路径自推断 > 默认。
# 关键：Web 后端以 nobody 降权运行后通过 sudo 调用本脚本，sudo 默认清空环境变量
# （env_reset，TRIM_PKGVAR 不在 env_keep），故必须能自推断 OVPN_DATA，不能只依赖环境变量。
# 官方规范（framework.md）：/var/apps/{app}/var → /vol{n}/@appdata/{app}（卷号 n 由 fnOS 决定，
# 可用 appcenter-cli default-volume 配置，不一定是 vol2！）。此前默认写死 /vol2 是 bug：
# 任何用户默认卷非 vol2（x86/ARM 均可能）时 jq 读 config.json 必然失败。v1.0.68 修复。
if [ -z "${OVPN_DATA:-}" ]; then
    if [ -n "${TRIM_PKGVAR:-}" ]; then
        OVPN_DATA="${TRIM_PKGVAR}/etc"
    elif [ -d "/var/apps/openvpn/var" ]; then
        # 官方数据目录 symlink（sudo 后仍可访问，跨卷正确）
        OVPN_DATA="/var/apps/openvpn/var/etc"
    else
        # sudo 的 env_reset 会清空 TRIM_*；从脚本真实路径推导卷号，兼容 vol1/vol2/vol3…（不再写死 vol2）。
        APP_REAL="$(cd "${APP_BIN_DIR}/.." && pwd -P 2>/dev/null)"
        VOL_NUM="$(echo "${APP_REAL}" | sed -n 's|^/vol\([0-9][0-9]*\)/.*|\1|p')"
        if [ -z "${VOL_NUM}" ]; then
            # 真实路径未落在 /volN/ 下时，扫描所有卷定位本应用数据目录
            for v in /vol*/@appdata/openvpn; do
                [ -d "${v}" ] || continue
                VOL_NUM="$(echo "${v}" | sed -n 's|^/vol\([0-9][0-9]*\)/.*|\1|p')"
                break
            done
        fi
        if [ -n "${VOL_NUM}" ] && [ -d "/vol${VOL_NUM}/@appdata/openvpn" ]; then
            OVPN_DATA="/vol${VOL_NUM}/@appdata/openvpn/etc"
        else
            # 兜底：默认主数据卷 vol1（不再写死 vol2）
            OVPN_DATA="/vol1/@appdata/openvpn/etc"
        fi
    fi
fi
export OVPN_DATA
SYSTEM_CONFIG="$OVPN_DATA/config.json"
export EASYRSA_PKI="$OVPN_DATA/pki"
export EASYRSA="$APP_BIN_DIR"
# PATH 补 /usr/sbin:/sbin：nobody 降权环境默认 PATH 不含系统管理目录，
# 而 ensure_nat() 依赖 nft/iptables（位于 /usr/sbin、/sbin），缺了会导致
# "no nft/iptables" 误判 → 网关模式 NAT/FORWARD 规则永远加载不上。
export PATH="$APP_BIN_DIR:/usr/sbin:/sbin:$PATH"

init_pki() {
	SERVER_NAME=$(jq -r '.system.base.server_name // ""' "$SYSTEM_CONFIG")
	SERVER_CN=$(jq -r '.system.base.server_cn // ""' "$SYSTEM_CONFIG")
	cd "$OVPN_DATA" && easyrsa init-pki

	cat <<EOF >"$EASYRSA_PKI/vars"
set_var EASYRSA $APP_BIN_DIR
set_var EASYRSA_CA_EXPIRE 365
set_var EASYRSA_CERT_EXPIRE 365
set_var EASYRSA_CRL_DAYS 365
set_var EASYRSA_ALGO ec
set_var EASYRSA_CURVE prime256v1
EOF

	easyrsa --batch --req-cn="$SERVER_CN" build-ca nopass
	easyrsa --batch build-server-full "$SERVER_NAME" nopass
	easyrsa gen-crl
	openvpn --genkey secret "$EASYRSA_PKI/tc.key"
	# root 跑完归 nobody：supervisor 的 chown 在 init 之前执行，init 建的 pki 是 root 700，
	# web(nobody) 读证书统计/CRL 会 permission denied（v1.0.55 降权后遗漏）。
	chown -R nobody:nogroup "$EASYRSA_PKI" 2>/dev/null || true
}

init_config() {
	SERVER_NAME=$(jq -r '.system.base.server_name // ""' "$SYSTEM_CONFIG")
	OVPN_PORT=$(jq -r '.openvpn.ovpn_port // "1194"' "$SYSTEM_CONFIG")
	OVPN_PROTO=$(jq -r '.openvpn.ovpn_proto // "udp"' "$SYSTEM_CONFIG")
	OVPN_MAXCLIENTS=$(jq -r '.openvpn.ovpn_maxclients // "200"' "$SYSTEM_CONFIG")
	OVPN_MANAGEMENT=$(jq -r '.openvpn.ovpn_management // "127.0.0.1:7505"' "$SYSTEM_CONFIG")
	OVPN_IPV6=$(jq -r '.openvpn.ovpn_ipv6 // "false"' "$SYSTEM_CONFIG")
	OVPN_IPV6_LISTEN=$(jq -r '.openvpn.ovpn_ipv6_listen // "false"' "$SYSTEM_CONFIG")
	OVPN_GATEWAY=$(jq -r '.openvpn.ovpn_gateway // "false"' "$SYSTEM_CONFIG")
	OVPN_SUBNET=$(jq -r '.openvpn.ovpn_subnet // "10.8.0.0/24"' "$SYSTEM_CONFIG")
	OVPN_SUBNET6=$(jq -r '.openvpn.ovpn_subnet6 // "fdaf:f178:e916:6dd0::/64"' "$SYSTEM_CONFIG")
	WEB_PORT=$(jq -r '.system.base.web_port // "8833"' "$SYSTEM_CONFIG")
	# 网关模式推送的 DNS（国内默认，避免 Google DNS 不可达导致"连上但无法上网"）
	OVPN_DNS1=$(jq -r '.openvpn.ovpn_push_dns1 // "223.5.5.5"' "$SYSTEM_CONFIG")
	OVPN_DNS2=$(jq -r '.openvpn.ovpn_push_dns2 // "114.114.114.114"' "$SYSTEM_CONFIG")

	# v1.0.69：IPv6 直连监听开启 → proto 加 6 后缀（tcp6/udp6 双栈，bindv6only=0 时同时收 v4+v6）
	if [ "$OVPN_IPV6_LISTEN" = "true" ]; then
		OVPN_PROTO="${OVPN_PROTO}6"
	fi
	cat <<EOF >"$OVPN_DATA/server.conf"
port $OVPN_PORT
proto $OVPN_PROTO
dev tun
persist-key
persist-tun
keepalive 10 60
topology subnet
tun-mtu 1400
mssfix 1360
# v1.0.91: 无条件设置隧道 MTU 与 MSS，避免加密后 UDP 数据报超过物理 MTU(1500) 导致分片/丢包，
# 表现为访问局域网其它设备(SMB/HTTP)时大块 TCP 传输吞吐雪崩。mssfix 改写 TCP MSS 选项从源头抑制分片。
$([[ "$OVPN_IPV6" == "true" ]] && echo -e "server $(getsubnet $OVPN_SUBNET)\nserver-ipv6 $OVPN_SUBNET6" || echo "server $(getsubnet $OVPN_SUBNET)")
$([[ "$OVPN_GATEWAY" == "true" ]] && echo -e "push \"dhcp-option DNS $OVPN_DNS1\"\npush \"dhcp-option DNS $OVPN_DNS2\"\npush \"redirect-gateway def1 ipv6 bypass-dhcp\"" || echo -e "#push \"dhcp-option DNS $OVPN_DNS1\"\n#push \"dhcp-option DNS $OVPN_DNS2\"\n#push \"redirect-gateway def1 ipv6 bypass-dhcp\"")
dh none
tls-groups prime256v1
tls-crypt $EASYRSA_PKI/tc.key
crl-verify $EASYRSA_PKI/crl.pem
ca $EASYRSA_PKI/ca.crt
cert $EASYRSA_PKI/issued/$SERVER_NAME.crt
key $EASYRSA_PKI/private/$SERVER_NAME.key
auth SHA256
cipher AES-128-GCM
data-ciphers AES-128-GCM
tls-server
tls-version-min 1.2
tls-cipher TLS-ECDHE-ECDSA-WITH-AES-128-GCM-SHA256
auth-user-pass-verify $APP_BIN_DIR/openvpn-auth via-env
client-disconnect $APP_BIN_DIR/ovpn-helper.sh
client-connect $APP_BIN_DIR/ovpn-helper.sh
script-security 3
status $OVPN_DATA/openvpn-status.log
client-config-dir $OVPN_DATA/ccd
duplicate-cn
client-to-client
max-clients $OVPN_MAXCLIENTS
management ${OVPN_MANAGEMENT/:/ }
# 权限合规（上架要求）：root 仅用于启动期 TUN 创建/路由/nft 加载等特权准备，
# 数据通道降权到 nobody 运行（OpenVPN 标准做法）。status 文件需预授权给 nobody。
user nobody
group nogroup
verb 2
$([[ "$OVPN_PROTO" =~ "udp" ]] && echo "explicit-exit-notify 1")
setenv ovpn_data ${OVPN_DATA:-/data}
setenv auth_api http://127.0.0.1:$WEB_PORT/login
setenv ovpn_auth_api http://127.0.0.1:$WEB_PORT/ovpn/login
setenv ovpn_history_api http://127.0.0.1:$WEB_PORT/ovpn/history
EOF
}

ensure_server() {
	SERVER_NAME=$(jq -r '.system.base.server_name // ""' "$SYSTEM_CONFIG")
	if [ ! -f "$EASYRSA_PKI/issued/$SERVER_NAME.crt" ]; then
		easyrsa --batch build-server-full "$SERVER_NAME" nopass
	fi
	# 每次启动确保 nft 表存在（限速/拉黑依赖 openvpn-nft 表；表是内存态，重启即失）
	load_nftconfig
	# 网关模式 NAT/IP 转发也是内存态，重启后必须重建
	ensure_nat
	init_config
	# root 跑完归 nobody：server.conf 需 web(nobody) 手动编辑保存（main.go OpenFile O_TRUNC），
	# pki 需 web 读证书统计（v1.0.55 降权后遗漏，同 init_pki）。
	chown -R nobody:nogroup "$EASYRSA_PKI" 2>/dev/null || true
	chown nobody:nogroup "$OVPN_DATA/server.conf" 2>/dev/null || true
}

# 确保 NAT(MASQUERADE) 与 IP 转发：让 VPN 客户端流量能经服务端出公网/访问内网。
# iptables/nft 规则均为内存态，NAS 重启后全部丢失，故每次启动/开关变更时重建。
# 双保险：nft 表 + iptables（兼容 fnOS 底层 legacy/nft 两种防火墙），并放行 FORWARD 转发。
# NAT 规则按 -s $SUBNET 限定，仅作用于 VPN 客户端出站流量，故始终安装（不再受全局网关开关门控）。
ensure_nat() {
	set +e
	command -v nft >/dev/null 2>&1 && NFT=1 || NFT=0
	command -v iptables >/dev/null 2>&1 && IPT=1 || IPT=0
	[ "$NFT" = "0" ] && [ "$IPT" = "0" ] && { echo "ensure_nat: no nft/iptables" >&2; set -e; return 0; }
	GATEWAY=$(jq -r '.openvpn.ovpn_gateway // "false"' "$SYSTEM_CONFIG")
	IPV6_LISTEN=$(jq -r '.openvpn.ovpn_ipv6_listen // "false"' "$SYSTEM_CONFIG")
	SUBNET=$(jq -r '.openvpn.ovpn_subnet // "10.8.0.0/24"' "$SYSTEM_CONFIG")
	SUBNET6=$(jq -r '.openvpn.ovpn_subnet6 // ""' "$SYSTEM_CONFIG")
	OVPN_PORT=$(jq -r '.openvpn.ovpn_port // "1194"' "$SYSTEM_CONFIG")

	# 幂等清理：先删后建，避免规则翻倍/残留
	if [ "$NFT" = "1" ]; then
		nft delete table ip openvpn-nat 2>/dev/null
		nft delete table ip6 openvpn-nat 2>/dev/null
	fi
	if [ "$IPT" = "1" ]; then
		# 清理 openvpn 相关 iptables 规则：
		#  ① 带 openvpn 注释标记的（v1.0.53+ 新增规则）；
		#  ② 所有 -s 10.0.0.0/8 私有段规则（OpenVPN 专用段，docker 网桥只用 172.x，
		#     不会误伤）——覆盖早期无注释的存量规则与改子网后的旧子网残留。
		iptables -t nat -S POSTROUTING 2>/dev/null | grep -E 'comment.*openvpn|-s 10\.' | while read -r line; do
			rule="${line#-A POSTROUTING }"
			iptables -t nat -D POSTROUTING $rule 2>/dev/null
		done
		iptables -S FORWARD 2>/dev/null | grep -E 'comment.*openvpn|-s 10\.' | while read -r line; do
			rule="${line#-A FORWARD }"
			iptables -D FORWARD $rule 2>/dev/null
		done
		# RELATED,ESTABLISHED 放行：仅清带 openvpn 注释的（docker 的在 DOCKER 链，不在此层）
		iptables -S FORWARD 2>/dev/null | grep 'comment.*openvpn' | grep 'RELATED,ESTABLISHED' | while read -r line; do
			rule="${line#-A FORWARD }"
			iptables -D FORWARD $rule 2>/dev/null
		done
	fi

	# IPv6 直连监听（v1.0.69）：放行 IPv6 1194 INPUT（公网 IPv6 客户端连入）
	# + IPv6 隧道段 FORWARD（客户端 v6 互访/上网）。ip6tables 双保险，幂等添加。
	if [ "$IPV6_LISTEN" = "true" ]; then
		ip6tables -C INPUT -p tcp --dport "$OVPN_PORT" -j ACCEPT 2>/dev/null || ip6tables -A INPUT -p tcp --dport "$OVPN_PORT" -j ACCEPT 2>/dev/null
		ip6tables -C INPUT -p udp --dport "$OVPN_PORT" -j ACCEPT 2>/dev/null || ip6tables -A INPUT -p udp --dport "$OVPN_PORT" -j ACCEPT 2>/dev/null
		if [ -n "$SUBNET6" ]; then
			ip6tables -C FORWARD -s "$SUBNET6" -j ACCEPT 2>/dev/null || ip6tables -I FORWARD -s "$SUBNET6" -j ACCEPT 2>/dev/null
		fi
		ip6tables -C FORWARD -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || ip6tables -I FORWARD -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null
		echo "ensure_nat: ipv6 listen firewall ok" >&2
	fi

	# NAT / MASQUERADE 始终安装（不再受全局网关开关门控）：
	# 规则按 -s $SUBNET 限定，仅作用于 VPN 客户端出站流量，常驻安全、无副作用。
	# 修复 v1.0.89：此前仅全局网关(ovpn_gateway=true)时安装 NAT，
	# 导致「全局网关关 + 分组勾选全局代理(redirect-gateway)」时服务端缺 NAT，
	# 分组全隧道只能访问局域网、无法访问互联网；同时「访问内网网段(route)」
	# 的 LAN 回包也依赖该 MASQUERADE，一并受益。
		if [ "$NFT" = "1" ]; then
			{
				echo "table ip openvpn-nat {"
				echo "	chain postrouting {"
				echo "		type nat hook postrouting priority srcnat; policy accept;"
				echo "		ip saddr $SUBNET masquerade"
				echo "	}"
				echo "}"
				if [ -n "$SUBNET6" ]; then
					echo "table ip6 openvpn-nat {"
					echo "	chain postrouting {"
					echo "		type nat hook postrouting priority srcnat; policy accept;"
					echo "		ip6 saddr $SUBNET6 masquerade"
					echo "	}"
					echo "}"
				fi
			} >"$OVPN_DATA/openvpn-nat.nft"
			if nft -f "$OVPN_DATA/openvpn-nat.nft"; then
				echo "ensure_nat: nft NAT loaded for $SUBNET" >&2
			else
				echo "ensure_nat: nft load failed" >&2
			fi
		fi
		if [ "$IPT" = "1" ]; then
			# iptables 双保险（fnOS 防火墙可能走 legacy 链路）。
			# 规则带 -m comment --comment openvpn 标记，ensure_nat 清理时按标记精确删除，
			# 绝不误伤 docker 网桥的 172.x 规则（改子网后旧规则可被彻底清除）。
			iptables -t nat -C POSTROUTING -s "$SUBNET" -j MASQUERADE -m comment --comment openvpn 2>/dev/null || iptables -t nat -A POSTROUTING -s "$SUBNET" -j MASQUERADE -m comment --comment openvpn
			# 放行 VPN 子网转发（插最前，绕过系统防火墙 FORWARD drop）
			iptables -I FORWARD -s "$SUBNET" -j ACCEPT -m comment --comment openvpn 2>/dev/null
			iptables -I FORWARD -m state --state RELATED,ESTABLISHED -j ACCEPT -m comment --comment openvpn 2>/dev/null
			echo "ensure_nat: iptables NAT+FORWARD ok" >&2
		fi
		# IP 转发（sysctl 与 /proc 双路兜底，OpenVPN 启动时也会自行开启）
		echo 1 >/proc/sys/net/ipv4/ip_forward 2>/dev/null
		sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1
		echo 1 >/proc/sys/net/ipv6/conf/all/forwarding 2>/dev/null
		sysctl -w net.ipv6.conf.all.forwarding=1 >/dev/null 2>&1
	set -e
}

renew_cert() {
	SERVER_NAME=$(jq -r '.system.base.server_name // ""' "$SYSTEM_CONFIG")
	easyrsa --batch --days=$1 renew "$SERVER_NAME"
	easyrsa --batch revoke-renewed "$SERVER_NAME"
	easyrsa --batch --days=$1 gen-crl
	# root 跑完归 nobody（同 genclient，见上）
	chown -R nobody:nogroup "$EASYRSA_PKI" 2>/dev/null || true
}

auth() {
	if [ "$1" = "true" ]; then
		sed -i 's/^#auth-user-pass-verify/auth-user-pass-verify/' "$OVPN_DATA/server.conf"
	else
		sed -i 's/^auth-user-pass-verify/#&/' "$OVPN_DATA/server.conf"
	fi
}

getsubnet() {
	ip=$(echo $1 | cut -d'/' -f1)
	prefix=$(echo $1 | cut -d'/' -f2)

	mask=""
	for i in {1..4}; do
		if [ $prefix -ge 8 ]; then
			mask+="255"
			prefix=$((prefix - 8))
		else
			mask+=$((256 - 2 ** (8 - prefix)))
			prefix=0
		fi

		if [ $i -lt 4 ]; then
			mask+="."
		fi
	done

	# v1.0.78：OpenVPN --server 第一个参数必须是网段地址（主机位全 0）。
	# 用户可能在设置里填网关 IP（如 192.168.31.1/24），直接 echo $ip 会生成
	# `server 192.168.31.1 255.255.255.0` → Options error: network/netmask combination is invalid
	# → openvpn 启动即退出（7505 永不监听 → 框架报启动失败）。
	# 这里把 IP 与掩码按位与，归零主机位：192.168.31.1 & 255.255.255.0 = 192.168.31.0。
	a=$(echo "$ip" | cut -d. -f1); b=$(echo "$ip" | cut -d. -f2)
	c=$(echo "$ip" | cut -d. -f3); d=$(echo "$ip" | cut -d. -f4)
	m1=$(echo "$mask" | cut -d. -f1); m2=$(echo "$mask" | cut -d. -f2)
	m3=$(echo "$mask" | cut -d. -f3); m4=$(echo "$mask" | cut -d. -f4)
	echo "$((a & m1)).$((b & m2)).$((c & m3)).$((d & m4)) $mask"
}

genclient() {
	SERVER_NAME=$(jq -r '.system.base.server_name // ""' "$SYSTEM_CONFIG")
	OVPN_PROTO=$(jq -r '.openvpn.ovpn_proto // "udp"' "$SYSTEM_CONFIG")
	OVPN_PORT=$(jq -r '.openvpn.ovpn_port // "1194"' "$SYSTEM_CONFIG")
	OVPN_IPV6=$(jq -r '.openvpn.ovpn_ipv6 // "false"' "$SYSTEM_CONFIG")
	OVPN_IPV6_LISTEN=$(jq -r '.openvpn.ovpn_ipv6_listen // "false"' "$SYSTEM_CONFIG")
	SERVER_ADDR=$(jq -r '.system.base.server_addr // ""' "$SYSTEM_CONFIG")

	if [ ! -f "$EASYRSA_PKI/private/$1.key" ]; then
		easyrsa --batch build-client-full "$1" nopass || exit 1
	fi
	mkdir -p "$OVPN_DATA/clients"
	# v1.0.69：remote 地址处理放在 heredoc 外（函数体），heredoc 内只引用结果。
	REMOTE_ADDR="${2:-${SERVER_ADDR:-}}"
	if [ -z "$REMOTE_ADDR" ]; then
		# 自动检测：IPv6 直连监听开启时优先公网 IPv6，否则 IPv4
		if [ "$OVPN_IPV6_LISTEN" = "true" ]; then
			REMOTE_ADDR=$(ip -6 route get 2001:4860:4860::8888 2>/dev/null | grep -oP 'src \K\S+')
		else
			REMOTE_ADDR=$(ip -4 route get 8.8.8.8 2>/dev/null | grep -oP 'src \K\S+')
		fi
	fi
	# IPv6 地址必须加 []（OpenVPN 客户端语法），IPv4 不受影响
	case "$REMOTE_ADDR" in
		*:*) REMOTE_ADDR="[$REMOTE_ADDR]" ;;
	esac
	cat <<EOF >"$OVPN_DATA/clients/$1.ovpn"
client
proto $OVPN_PROTO
remote $REMOTE_ADDR ${3:-$OVPN_PORT}
dev tun
resolv-retry infinite
nobind
persist-key
persist-tun
remote-cert-tls server
verify-x509-name $SERVER_NAME name
auth SHA256
$(grep -q '^auth-user-pass-verify' "$OVPN_DATA/server.conf" && echo 'auth-user-pass' || echo '#auth-user-pass')
cipher AES-128-GCM
tls-client
tls-version-min 1.2
tls-cipher TLS-ECDHE-ECDSA-WITH-AES-128-GCM-SHA256
verb 3
tun-mtu 1400
mssfix 1360   # v1.0.91: 与 server.conf 一致，确保隧道两端 MTU 协商一致，消除跨隧道路径分片
$([[ "$OVPN_PROTO" =~ "udp" ]] && echo "explicit-exit-notify")
$([[ "$5" == "true" ]] && echo 'static-challenge "Enter MFA code" 1')

## Custom configuration ##
$(echo -e "$4")
## end ##

<ca>
$(cat "$EASYRSA_PKI/ca.crt")
</ca>
<cert>
$(openssl x509 -in "$EASYRSA_PKI/issued/$1.crt")
</cert>
<key>
$(cat "$EASYRSA_PKI/private/$1.key")
</key>
<tls-crypt>
$(cat "$EASYRSA_PKI/tc.key")
</tls-crypt>
EOF

    # root 跑完证书操作后归 nobody：web 后端以 nobody 降权运行，要读 pki
    # 统计证书/CRL（main.go 证书列表、添加客户端后刷新）。不 chown 则产物 root 700，
    # nobody 读不到 → 添加客户端后列表加载 permission denied（v1.0.55 降权后 bug）。
    chown -R nobody:nogroup "$EASYRSA_PKI" 2>/dev/null || true
    chown nobody:nogroup "$OVPN_DATA/clients/$1.ovpn" 2>/dev/null || true
}

# ---- 以下为 openvpn 脚本钩子（client-connect / client-disconnect / learn-address）----
set_ovip() {
	cc_file="$1"
	ip_file="$ovpn_data/.ovip"
	if [ -f "$ip_file" ]; then
		ipaddr=$(cat "$ip_file")
		if [ -n "$ipaddr" ]; then
			echo "ifconfig-push $ipaddr $ifconfig_netmask" >"$cc_file"
			rm -rf "$ip_file"
		fi
	fi
}

set_ovconfig() {
	cc_file="$1"
	ovc_file="$ovpn_data/.ovc"
	if [ -f "$ovc_file" ]; then
		ovconfig=$(cat "$ovc_file")
		if [ -n "$ovconfig" ]; then
			# v1.0.84：分组配置含 DNS/路由等 push 时，先 push-reset 清空 server.conf
			# 的全局 push（含网关模式默认 DNS 223.5.5.5/114.114.114.114），再下发分组配置，
			# 使分组 DNS 优先于全局默认 DNS 生效（否则客户端优先采用全局首个 DNS）。
			if echo "$ovconfig" | grep -qi "dhcp-option DNS\|push \"route\|^route "; then
				echo "push-reset" >>"$cc_file"
				# v1.0.87：push-reset 会清空全局 push 列表（含 server 模式自动下推的
				# topology subnet），导致 ovpn3 移动端客户端收不到拓扑、回退 net30，
				# 报 "ifconfig addresses are not in the same /30 subnet"。复位后重新下发拓扑。
				echo 'push "topology subnet"' >>"$cc_file"
				# v1.0.92(补)：push-reset 同时清空 server 自动下推的 route-gateway 与 tun-ipv6。
				# route-gateway 缺失 → 客户端隧道网关变 UNSPEC → OpenVPN Connect 报
				# "error parsing tunnel_addresses[0].gateway IP address 'UNSPEC'" → 400 Bad Request 连上；tun-ipv6 缺失 → IPv6 隧道不通。
				# 两者必须补回（服务端隧道 IP = server 网段 +1）。
				srv_net=$(grep -m1 '^server ' "$ovpn_data/server.conf" 2>/dev/null | awk '{print $2}')
				if [ -n "$srv_net" ]; then
					gw_ip=$(echo "$srv_net" | awk -F. '{printf "%d.%d.%d.%d", $1,$2,$3,$4+1}')
					echo "push \"route-gateway $gw_ip\"" >>"$cc_file"
				fi
				if grep -q '^server-ipv6 ' "$ovpn_data/server.conf" 2>/dev/null; then
					echo 'push "tun-ipv6"' >>"$cc_file"
				fi

				# v1.0.92：push-reset 同时会清空 server.conf 全局下推的 redirect-gateway
				# 默认路由与全局 DNS。若这些由全局（网关模式）下发、且本分组未自带覆盖，
				# 则必须补回，否则「全局网关开 + 分组只配 route」时客户端丢掉默认路由/DNS，
				# 上了 VPN 却上不了网（本次 issue 的根因）。
				if echo "$ovconfig" | grep -qi "redirect-gateway"; then
					: # 分组自带全隧道（含 redirect-gateway），无需补全局默认路由
				else
					gw=$(grep -m1 '^push "redirect-gateway' "$ovpn_data/server.conf" 2>/dev/null) || true
					[ -n "$gw" ] && echo "$gw" >>"$cc_file"
				fi
				if echo "$ovconfig" | grep -qi "dhcp-option DNS"; then
					: # 分组自带 DNS，保持分组优先、不补全局 DNS（v1.0.84 初衷）
				else
					grep '^push "dhcp-option DNS' "$ovpn_data/server.conf" 2>/dev/null >>"$cc_file" || true
				fi
			fi
			echo "$ovconfig" >>"$cc_file"
			rm -rf "$ovc_file"
		fi
	fi
}

client_connect() {
	set_ovip "$1"
	set_ovconfig "$1"
	add_history connect
}

set_firewall() {
	set +e
	WEB_PORT=$(jq -r '.system.base.web_port // "8833"' "$ovpn_data/config.json")
	TOKEN=$(jq -r '.system.base.token // ""' "$ovpn_data/config.json")
	ovpn_firewall_api="http://127.0.0.1:$WEB_PORT/ovpn/firewall?a=add_ovips"
	data="vip=$ifconfig_pool_remote_ip&vip6=$ifconfig_pool_remote_ip6&username=$username"
	curl -w "%{http_code}" --connect-timeout 5 -s -X POST -o /dev/null -d "$data" "$ovpn_firewall_api" -H "O-Token: $TOKEN" >/dev/null
	set -e
}

delete_firewall() {
	set +e
	WEB_PORT=$(jq -r '.system.base.web_port // "8833"' "$ovpn_data/config.json")
	TOKEN=$(jq -r '.system.base.token // ""' "$ovpn_data/config.json")
	ovpn_firewall_api="http://127.0.0.1:$WEB_PORT/ovpn/firewall?a=delete_ovips"
	data="vip=$ifconfig_pool_remote_ip&vip6=$ifconfig_pool_remote_ip6&username=$username"
	curl -w "%{http_code}" --connect-timeout 5 -s -X POST -o /dev/null -d "$data" "$ovpn_firewall_api" -H "O-Token: $TOKEN" >/dev/null
	set -e
}

client_disconnect() {
	delete_firewall
	add_history disconnect
}

add_history() {
	set +e
	local action="${1:-disconnect}"
	# connect 时 OpenVPN 未必下发 time_unix（为空→存 0）；兜底为当前时间戳，避免连接记录时间列显示「—」
	if [ "$action" = "connect" ]; then
		time_unix="${time_unix:-$(date +%s)}"
	else
		time_unix="${time_unix:-0}"
	fi
	TOKEN=$(jq -r '.system.base.token // ""' "$ovpn_data/config.json")
	data="action=$action&vip=$ifconfig_pool_remote_ip&vip6=$ifconfig_pool_remote_ip6&rip=$trusted_ip&rip6=$trusted_ip6&common_name=$common_name&username=$username&bytes_received=$bytes_received&bytes_sent=$bytes_sent&time_unix=$time_unix&time_duration=${time_duration:-0}"
	status=$(curl -w "%{http_code}" --connect-timeout 5 -s -X POST -o /dev/null -d "$data" "$ovpn_history_api" -H "O-Token: $TOKEN")
	set -e
}

load_nftconfig() {
	command -v nft >/dev/null 2>&1 || return 0
	NFT_CONFIG="$OVPN_DATA/openvpn.nft"
	TABLE=$(jq -r '.system.base.nft_table_name // "openvpn-nft"' "$SYSTEM_CONFIG")

	[ ! -f "$NFT_CONFIG" ] && cat <<EOF >"$NFT_CONFIG"
table inet $TABLE {
	set blacklist_v4 {
		type ipv4_addr
	}
	set blacklist_v6 {
		type ipv6_addr
	}
	chain forward {
		type filter hook forward priority filter; policy accept;
		ip saddr @blacklist_v4 drop
		ip6 saddr @blacklist_v6 drop
	}
	chain upload {
		type filter hook postrouting priority filter; policy accept;
	}
	chain download {
		type filter hook prerouting priority filter; policy accept;
	}
}
EOF
	# 幂等加载：nft -f 对已存在的表是「追加规则」语义（不是替换）。
	# openvpn-web 重启（升级/自愈）时 openvpn 主进程可能未重启、表还在，
	# 直接 -f 会让 forward 规则翻倍累积（曾实测累积到 48 条）。先删表再加载。
	nft delete table inet "$TABLE" 2>/dev/null || true
	nft -f "$NFT_CONFIG"
}

check_config() {
	config="$OVPN_DATA/server.conf"
	grep -q "^client-connect" "$config" || echo "client-connect $APP_BIN_DIR/ovpn-helper.sh" >>"$config"
	grep -q "^client-disconnect" "$config" || echo "client-disconnect $APP_BIN_DIR/ovpn-helper.sh" >>"$config"
	grep -q "^learn-address" "$config" || echo "learn-address $APP_BIN_DIR/ovpn-helper.sh" >>"$config"
}

# ---- 入口 ----
case $1 in
"init")
	mkdir -p "$OVPN_DATA/ccd"
	init_pki
	init_config
	load_nftconfig
	ensure_nat
	check_config
	exit 0
	;;
"genclient")
	if [ -z $2 ]; then
		echo "请输入生成客户端名称！"
		exit 1
	fi
	if [ -n "$6" ]; then
		echo -e "$6" >"$OVPN_DATA/ccd/$2"
	fi
	genclient "$2" "$3" "$4" "$5" "$7"
	exit 0
	;;
"auth")
	auth $2
	exit 0
	;;
"ensure_server")
	ensure_server
	exit 0
	;;
"ensure_nat")
	ensure_nat
	exit 0
	;;
"revoke")
	# 吊销客户端证书 + 重建 CRL + pki 归 nobody（v1.0.69）。
	# 此前 web 直接 sudo easyrsa revoke，但 sudoers 白名单只授权 helper/iptables/chown，
	# easyrsa 不被授权 → revoke 静默失败 → 证书残留 → 客户端计数虚高。
	# 统一走 helper（sudoers 已授权），helper 内部以 root 调 easyrsa 并 chown 回 nobody。
	if [ -z $2 ]; then
		echo "请输入吊销客户端名称！"
		exit 1
	fi
	easyrsa --batch revoke "$2" || exit 1
	easyrsa gen-crl || exit 1
	# v1.0.94: 清理残留 req（easyrsa 吊销不删 reqs/<name>.req，残留会导致
	# 同名客户端重建时 build-client-full 报 "Request file already exists" 直接 abort）
	rm -f "${EASYRSA_PKI}/reqs/$2.req"
	chown -R nobody:nogroup "${OVPN_DATA}/pki" 2>/dev/null || true
	exit 0
	;;
"renewcert")
	renew_cert $2
	exit 0
	;;
esac

case "$script_type" in
client-connect)
	client_connect "$@"
	exit 0
	;;
client-disconnect)
	client_disconnect "$@"
	exit 0
	;;
learn-address)
	[ "$1" == "add" ] && set_firewall "$@"
	exit 0
	;;
esac

exec "$@"
