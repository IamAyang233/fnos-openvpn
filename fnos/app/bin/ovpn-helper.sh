#!/bin/bash
# OpenVPN FPK 证书/客户端生成辅助脚本
# 替代原 docker-entrypoint.sh 的调用，去除 docker 依赖，
# 适配纯 FPK（二进制与 easyrsa/jq 均随包内置在 app/bin）。
set -e

# 脚本自身所在目录 = app/bin
APP_BIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 所有数据都在 OVPN_DATA 下（由 service-setup 注入，指向数据卷 etc/）
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
}

init_config() {
	SERVER_NAME=$(jq -r '.system.base.server_name // ""' "$SYSTEM_CONFIG")
	OVPN_PORT=$(jq -r '.openvpn.ovpn_port // "1194"' "$SYSTEM_CONFIG")
	OVPN_PROTO=$(jq -r '.openvpn.ovpn_proto // "udp"' "$SYSTEM_CONFIG")
	OVPN_MAXCLIENTS=$(jq -r '.openvpn.ovpn_maxclients // "200"' "$SYSTEM_CONFIG")
	OVPN_MANAGEMENT=$(jq -r '.openvpn.ovpn_management // "127.0.0.1:7505"' "$SYSTEM_CONFIG")
	OVPN_IPV6=$(jq -r '.openvpn.ovpn_ipv6 // "false"' "$SYSTEM_CONFIG")
	OVPN_GATEWAY=$(jq -r '.openvpn.ovpn_gateway // "false"' "$SYSTEM_CONFIG")
	OVPN_SUBNET=$(jq -r '.openvpn.ovpn_subnet // "10.8.0.0/24"' "$SYSTEM_CONFIG")
	OVPN_SUBNET6=$(jq -r '.openvpn.ovpn_subnet6 // "fdaf:f178:e916:6dd0::/64"' "$SYSTEM_CONFIG")
	WEB_PORT=$(jq -r '.system.base.web_port // "8833"' "$SYSTEM_CONFIG")
	# 网关模式推送的 DNS（国内默认，避免 Google DNS 不可达导致"连上但无法上网"）
	OVPN_DNS1=$(jq -r '.openvpn.ovpn_push_dns1 // "223.5.5.5"' "$SYSTEM_CONFIG")
	OVPN_DNS2=$(jq -r '.openvpn.ovpn_push_dns2 // "114.114.114.114"' "$SYSTEM_CONFIG")

	cat <<EOF >"$OVPN_DATA/server.conf"
port $OVPN_PORT
proto $OVPN_PROTO
dev tun
persist-key
persist-tun
keepalive 10 60
topology subnet
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
}

# 网关模式（全局代理）：确保 NAT(MASQUERADE) 与 IP 转发。
# iptables/nft 规则均为内存态，NAS 重启后全部丢失，故每次启动/开关变更时重建。
# 双保险：nft 表 + iptables（兼容 fnOS 底层 legacy/nft 两种防火墙），并放行 FORWARD 转发。
# 关闭网关模式时清理 NAT/放行规则，避免残留转发。
ensure_nat() {
	set +e
	command -v nft >/dev/null 2>&1 && NFT=1 || NFT=0
	command -v iptables >/dev/null 2>&1 && IPT=1 || IPT=0
	[ "$NFT" = "0" ] && [ "$IPT" = "0" ] && { echo "ensure_nat: no nft/iptables" >&2; set -e; return 0; }
	GATEWAY=$(jq -r '.openvpn.ovpn_gateway // "false"' "$SYSTEM_CONFIG")
	SUBNET=$(jq -r '.openvpn.ovpn_subnet // "10.8.0.0/24"' "$SYSTEM_CONFIG")
	SUBNET6=$(jq -r '.openvpn.ovpn_subnet6 // ""' "$SYSTEM_CONFIG")

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

	if [ "$GATEWAY" = "true" ]; then
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
	else
		echo "ensure_nat: gateway off, cleaned" >&2
	fi
	set -e
}

renew_cert() {
	SERVER_NAME=$(jq -r '.system.base.server_name // ""' "$SYSTEM_CONFIG")
	easyrsa --batch --days=$1 renew "$SERVER_NAME"
	easyrsa --batch revoke-renewed "$SERVER_NAME"
	easyrsa --batch --days=$1 gen-crl
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
	echo $ip $mask
}

genclient() {
	SERVER_NAME=$(jq -r '.system.base.server_name // ""' "$SYSTEM_CONFIG")
	OVPN_PROTO=$(jq -r '.openvpn.ovpn_proto // "udp"' "$SYSTEM_CONFIG")
	OVPN_PORT=$(jq -r '.openvpn.ovpn_port // "1194"' "$SYSTEM_CONFIG")
	OVPN_IPV6=$(jq -r '.openvpn.ovpn_ipv6 // "false"' "$SYSTEM_CONFIG")
	SERVER_ADDR=$(jq -r '.system.base.server_addr // ""' "$SYSTEM_CONFIG")

	if [ ! -f "$EASYRSA_PKI/private/$1.key" ]; then
		easyrsa --batch build-client-full "$1" nopass >/dev/null
	fi
	mkdir -p "$OVPN_DATA/clients"
	cat <<EOF >"$OVPN_DATA/clients/$1.ovpn"
client
proto $OVPN_PROTO
remote ${2:-${SERVER_ADDR:-$([[ "$OVPN_IPV6" == "true" ]] && ip -6 route get 2001:4860:4860::8888 | grep -oP 'src \K\S+' || ip -4 route get 8.8.8.8 | grep -oP 'src \K\S+')}} ${3:-$OVPN_PORT}
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
$([[ "$OVPN_IPV6" == "true" ]] && echo -e "tun-mtu 1400\nmssfix 1360")
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
