#!/bin/bash
# 诊断版 supervisor —— 每个 marker 都携带 RUN_ID，以便区分多次运行，
# 彻底规避"上一轮残留 marker 误导当前观测"的悖论。
RUN_ID="$(date +%s)-$$"
mark() {
    touch "${TRIM_PKGVAR:-/vol2/@appdata/openvpn}/${1}.${RUN_ID}.marker" 2>/dev/null || true
}
mark "RUN_START"
# 注意：不再在启动时 rm 旧 marker。
# 原因：(1) RUN_ID 已能区分各次运行；(2) rm 在 fnOS noacl 卷上可能挂起(seccomp 类)，
# 会导致脚本卡在启动早期、进程长期存活而被框架记为 "start" 不前进。
mark "AFTER_RM"

: "${TRIM_APPNAME:=openvpn}"
# v1.0.76：TRIM_PKGVAR 兜底自推断（防框架未注入时落死 /vol2，导致非 vol2 部署机器用不了）。
# 优先级：保持框架注入值 > 脚本真实路径推导（pwd -P 取卷号）→ 最后才回退 /vol2。
# 正常由 fnOS 框架注入 TRIM_PKGVAR（= /vol{n}/@appdata/openvpn），此分支仅作防御性兜底。
if [ -z "${TRIM_PKGVAR:-}" ]; then
    _SB="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P 2>/dev/null)"
    _VOL="$(echo "${_SB}" | sed -n 's|^/vol\([0-9][0-9]*\)/.*|\1|p')"
    if [ -n "${_VOL}" ] && [ -d "/vol${_VOL}/@appdata/openvpn" ]; then
        TRIM_PKGVAR="/vol${_VOL}/@appdata/openvpn"
    else
        TRIM_PKGVAR="/vol2/@appdata/openvpn"
    fi
fi
: "${TRIM_APPDEST:=/var/apps/openvpn}"

ETC="${TRIM_PKGVAR}/etc"
CONF="${ETC}/server.conf"
LOG="${TRIM_PKGVAR}/${TRIM_APPNAME}.log"
# v1.0.68：不再写死 /vol2（数据装 vol1 的机器 ROOT_DIR 指向不存在路径）。
# TRIM_PKGVAR 即 fnOS 注入的运行时数据目录（= /vol{n}/@appdata/openvpn）。
ROOT_DIR="${TRIM_PKGVAR}"

mark "BOOT"
trap 'ec=$?; mark "DIED_${ec}"' EXIT

APP_BIN_DIR="${TRIM_APPDEST}/bin"
APP_LIB_DIR="${TRIM_APPDEST}/lib"
mark "PATHS"

export OVPN_DATA="${ETC}"
export EASYRSA_PKI="${ETC}/pki"
export PATH="${APP_BIN_DIR}:${PATH}"
export LD_LIBRARY_PATH="${APP_LIB_DIR}${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
# 双模式（端口 + 统一网关）：openvpn-web 同时监听 TCP 8833 与 ${TRIM_APPDEST}/app.sock。
# 网关 socket 放 target 根目录（官方规范 gatewaySocket 只填文件名 app.sock）。
export SOCKET_PATH="${TRIM_APPDEST}/app.sock"
# OVPN_HELPER 绝对路径：web(nobody) 经 sudo 执行 helper 时，sudo secure_path 不含 app/bin，
# 相对路径 "ovpn-helper.sh" 会 sudoers 不匹配 → 添加客户端 500（v1.0.56 坑）。
# v1.0.69：固定用 /var/apps/openvpn/target/bin（框架必建 symlink target -> /vol{n}/@appcenter/openvpn，
# target/bin/ovpn-helper.sh 必然存在；/var/apps/openvpn/bin 本身不存在！）。
# 避免 TRIM_APPDEST 注入形式（symlink/物理）机器间不一致导致 sudoers 不匹配（revoke 报 password required）。
# sudoers 白名单已同时包含 target 路径与 /vol*/@appcenter/openvpn/bin 通配。
export OVPN_HELPER="/var/apps/openvpn/target/bin/ovpn-helper.sh"
export GATEWAY_PREFIX="/app/openvpn"
mark "ENV"

mkdir -p "${ETC}" "${ETC}/ccd" "${ETC}/clients"
# 权限合规（上架要求）：server.conf 已配置 user nobody/group nogroup 降权数据通道。
# openvpn(nobody) 需要可写 status 文件；openvpn-auth / 钩子(nobody) 需要读取 config.json。
# 幂等执行（每次启动都跑，防升级/重装后属主变化）。
touch "${ETC}/openvpn-status.log" 2>/dev/null || true
chown nobody:nogroup "${ETC}/openvpn-status.log" 2>/dev/null || true
# config.json 600：含 token(O-Token)/secret_key(AES 密钥)，644 会被其他系统用户读取（v1.0.61 安全审计）；
# openvpn-auth 钩子以 nobody 运行，600(nobody owner) 即可读取。
chmod 600 "${ETC}/config.json" 2>/dev/null || true
# Web 后端（nobody 降权运行，上架合规）需要在 target 根目录创建统一网关 socket（app.sock），
# 故 TRIM_APPDEST 根目录需 nobody 可写；socket 文件本身权限 0666 由 umask 控制，网关可连接。
chown nobody:nogroup "${TRIM_APPDEST}" 2>/dev/null || true
mark "MKDIR"

# 清理上一轮残留的孤儿进程。
# 背景：保活循环用 `cmd &` 起子进程，框架 stop 时只杀掉循环子 shell，
# 真正的 openvpn-web / openvpn 会变孤儿继续占用 8833 / 1194，
# 导致新版本二进制起不来（端口被占 → 反复退出），页面还是旧版。
# 这里在启动最早期用纯 /proc 扫描（不依赖 pkill/procps）清干净。
kill_stale() {
    local self=$$ pid cmd
    for p in /proc/[0-9]*; do
        pid="${p#/proc/}"
        [ "$pid" = "$self" ] && continue
        cmd=$(tr '\0' ' ' < "$p/cmdline" 2>/dev/null) || continue
        [ -z "$cmd" ] && continue
        case "$cmd" in
            *openvpn-supervisor.sh*) continue ;;
            */bin/openvpn-web*|*openvpn-web\ *|*openvpn-web)
                mark "KILL_STALE_WEB_${pid}"
                kill -9 "$pid" 2>/dev/null
                ;;
            */bin/openvpn\ --config*)
                mark "KILL_STALE_VPN_${pid}"
                kill -9 "$pid" 2>/dev/null
                ;;
        esac
    done
    # 兜底：cmdline 没匹配到的残留（旧版本/路径差异）按监听端口强杀
    kill_by_port 1194 7505
}
kill_stale
mark "STALE_CLEANED"

# 日志防爆：web_out.log 是追加模式永不截断，长期运行撑爆磁盘 → sqlite 写失败
# （"web 改东西失败"的真正来源）。vpn_out.log 虽每次重启截断，也统一保护。
# 超过 10MB 截断；截断后属主不变，运行中的进程 fd 仍可继续写入。
trim_log() {
    local f="$1" max="${2:-10485760}"
    [ -f "$f" ] || return 0
    [ "$(stat -c %s "$f" 2>/dev/null || echo 0)" -gt "$max" ] && : > "$f" 2>/dev/null || true
}

# 关键自检：openvpn-web 二进制是否可执行存在（缺失会触发 code 127）
if [ -x "${APP_BIN_DIR}/openvpn-web" ]; then
    mark "BIN_OK"
else
    mark "BIN_MISSING"
fi
if [ -f "${ETC}/config.json" ]; then
    mark "CONFIG_PRESENT"
else
    mark "CONFIG_ABSENT"
fi
if [ -f "${EASYRSA_PKI}/ca.crt" ]; then
    mark "CA_PRESENT"
else
    mark "CA_ABSENT"
fi

last_line_marker() {
    tail -n1 "$1" 2>/dev/null | tr -c 'A-Za-z0-9_=.:/-' '_' | cut -c1-120
}

# 1) Web 管理后端保活循环
run_web() {
    # 权限合规（上架要求）：Web 管理后端以 nobody 降权运行（root 仅用于特权准备）。
    # 数据目录先归 nobody：web 要写 config.json(pki/证书/客户端配置/ovpn.db)，读取 openvpn-auth 的 token。
    # 特权操作（iptables/nft → ovpn-helper.sh）通过 service_postinst 配置的 sudoers 白名单以 root 执行。
    chown -R nobody:nogroup "${ETC}" 2>/dev/null || true
    chmod 600 "${ETC}/config.json" 2>/dev/null || true
    # web_out.log 需 nobody 可写（重定向目标）
    touch "${ETC}/web_out.log" 2>/dev/null || true
    chown nobody:nogroup "${ETC}/web_out.log" "${ETC}/web.pid" 2>/dev/null || true
    while true; do
        mark "WEBLOOP_TICK"
        trim_log "${ETC}/web_out.log"
        runuser -u nobody --preserve-environment -- "${APP_BIN_DIR}/openvpn-web" >>"${ETC}/web_out.log" 2>&1 &
        local pid=$!
        echo "$pid" > "${ETC}/web.pid"
        local i
        for i in $(seq 1 30); do
            if ! kill -0 "$pid" 2>/dev/null; then break; fi
            sleep 1
        done
        wait "$pid"; local code=$?
        local last; last=$(last_line_marker "${ETC}/web_out.log")
        mark "WEB_EXIT_${code}"
        [ -n "$last" ] && mark "WEBERR_${code}_${last}"
        sleep 3
    done
}

# 3) OpenVPN 守护进程保活循环
run_vpn() {
    while true; do
        mark "VPNLOOP_TICK"
        trim_log "${ETC}/vpn_out.log"
        "${APP_BIN_DIR}/openvpn" --config "${CONF}" >"${ETC}/vpn_out.log" 2>&1 &
        local pid=$!
        echo "$pid" > "${ETC}/vpn.pid"
        local i
        for i in $(seq 1 30); do
            if ! kill -0 "$pid" 2>/dev/null; then break; fi
            sleep 1
        done
        wait "$pid"; local code=$?
        local last; last=$(last_line_marker "${ETC}/vpn_out.log")
        mark "VPN_EXIT_${code}"
        [ -n "$last" ] && mark "VPNERR_${code}_${last}"
        # v1.0.77：把 openvpn 启动失败的上下文写入 helper_out.log，
        # 使 Bug 反馈诊断（仅收集 web_out/helper_out）也能看到真正原因，
        # 不必再专门去翻 vpn_out.log。
        {
            echo "=== [$(date '+%F %T')] openvpn 退出 code=${code}，vpn_out.log 末 40 行 ==="
            tail -n 40 "${ETC}/vpn_out.log" 2>/dev/null
        } >>"${ETC}/helper_out.log" 2>&1
        sleep 5
    done
}

# 按本地监听端口反查并强杀占用进程（兜底 kill_stale 的 cmdline 匹配漏网：
# 如旧版本 openvpn 启动参数/路径不同、或 fnOS 升级后残留，导致新 openvpn
# 绑 1194 失败 → 反复退出 → 7505 永不监听 → 框架报"启动失败"）。
kill_by_port() {
    local port hex local_addr st inode p fd ln
    for port in "$@"; do
        hex=$(printf '%04X' "$port" | tr 'A-F' 'a-f')
        inode=""
        for f in /proc/net/tcp /proc/net/tcp6; do
            [ -r "$f" ] || continue
            while read -r ln; do
                set -- $ln
                local_addr=${2##*:}; local_addr=${local_addr,,}
                st=$4
                # inode 是 /proc/net/tcp 的最后一列（第 12 列：sl local rem st ... uid timeout inode），
                # 用末列 ${!#} 取最稳妥，避免不同内核字段数差异导致取到 uid。
                inode=${!#}
                case "$inode" in (*[!0-9]*) inode="" ;; esac
                [ "$local_addr" = "$hex" ] && [ "$st" = "0A" ] && [ -n "$inode" ] && break 2
                inode=""
            done < "$f"
        done
        [ -z "$inode" ] && continue
        for p in /proc/[0-9]*; do
            pid=${p##*/}
            [ "$pid" = "$$" ] && continue
            for fd in "$p"/fd/*; do
                ln=$(readlink "$fd" 2>/dev/null) || continue
                case "$ln" in
                    socket:\["$inode"\])
                        mark "KILL_PORT_${port}_${pid}"
                        kill -9 "$pid" 2>/dev/null
                        ;;
                esac
            done
        done
    done
}

mark "BEFORE_RUNWEB"
run_web & WEBLOOP=$!
mark "AFTER_RUNWEB"

# 等待 config.json 生成（最多 30s）
for i in $(seq 1 30); do
    [ -f "${ETC}/config.json" ] && break
    sleep 1
done
mark "CONFIG_WAIT_DONE"

# config.json 生成后再修权限：openvpn-web(viper) 创建文件时强制 0600（root 专属），
# 而 openvpn-auth 认证钩子以 nobody 运行需读取 token；若 openvpn 先于 web 生成配置
# 或 viper 重写配置，需在此处兜底 chmod 644（幂等）。
chmod 600 "${ETC}/config.json" 2>/dev/null || true

if [ ! -f "${ETC}/config.json" ]; then
    mark "REASON_CONFIG_TIMEOUT"
    kill "${WEBLOOP}" 2>/dev/null
    exit 1
fi

# 每次启动都用 config.json 重新生成 server.conf，确保 proto/port 等配置与 config 同步。
# 注意：openvpn-web 后端启动时可能先用默认值（udp）生成过一份 server.conf，
# 这里必须无条件用 ensure_server 覆盖为 config.json 的实际值，否则改了 proto/port 不会生效。
if [ -f "${EASYRSA_PKI}/ca.crt" ]; then
    mark "ENSURE_SERVER"
    if ! "${APP_BIN_DIR}/ovpn-helper.sh" ensure_server >"${ETC}/helper_out.log" 2>&1; then
        mark "REASON_ENSURESERVER_FAIL"
        kill "${WEBLOOP}" 2>/dev/null
        exit 1
    fi
else
    mark "INIT_PKI"
    if ! "${APP_BIN_DIR}/ovpn-helper.sh" init >"${ETC}/helper_out.log" 2>&1; then
        mark "REASON_INIT_FAIL"
        kill "${WEBLOOP}" 2>/dev/null
        exit 1
    fi
fi
# 兜底：init/ensure_server 由 root 执行，产物（pki/server.conf）归 root 700/644，
# web(nobody) 读证书统计/写 server.conf 会 denied。helper 内部已 chown，这里再兜底一次
# （覆盖升级/手动改数据等任何 root 写回的场景）。
chown -R nobody:nogroup "${ETC}" 2>/dev/null || true
mark "SERVERCONF_DONE"

mark "BEFORE_RUNVPN"
run_vpn & VPNLOOP=$!
mark "AFTER_RUNVPN"

cleanup() {
    # 先停保活循环，再杀真正的业务进程（否则循环会立刻把它们拉起来）
    kill "${WEBLOOP}" "${VPNLOOP}" 2>/dev/null
    local wp vp
    wp=$(cat "${ETC}/web.pid" 2>/dev/null)
    vp=$(cat "${ETC}/vpn.pid" 2>/dev/null)
    [ -n "$wp" ] && kill "$wp" 2>/dev/null
    [ -n "$vp" ] && kill "$vp" 2>/dev/null
    sleep 2
    [ -n "$wp" ] && kill -9 "$wp" 2>/dev/null
    [ -n "$vp" ] && kill -9 "$vp" 2>/dev/null
    # 兜底：扫 /proc 清掉任何漏网的同名进程
    kill_stale
    # 端口模式下不建 socket；仅当 SOCKET_PATH 被显式设置时才清理
    [ -n "${SOCKET_PATH}" ] && rm -f "${SOCKET_PATH}" 2>/dev/null
    wait 2>/dev/null
    exit 0
}
trap cleanup TERM INT

# ===== 健康自愈：status 文件新鲜度检测 =====
# openvpn 正常时每 60s 写一次 openvpn-status.log。若进程还活着但 status 超过
# 120s 未更新，说明隧道/数据通道已失效（典型：TUN 被系统回收、进程卡死）——
# 此时保活循环不会重启它（进程没退出），必须强杀让循环重建。
# 用户反馈"间隔时间长就连不上、要手动重启服务"即此盲区，v1.0.44 修复。
heal_stale_vpn() {
    local vp st now diff pid cmd
    vp=$(cat "${ETC}/vpn.pid" 2>/dev/null)
    [ -z "$vp" ] && return 0
    kill -0 "$vp" 2>/dev/null || return 0
    st=$(stat -c %Y "${ETC}/openvpn-status.log" 2>/dev/null || echo 0)
    now=$(date +%s)
    diff=$(( now - st ))
    [ "$diff" -le 120 ] && return 0
    mark "HEAL_STALE_STATUS_${diff}s"
    kill -9 "$vp" 2>/dev/null
    for p in /proc/[0-9]*; do
        pid="${p#/proc/}"
        cmd=$(tr '\0' ' ' < "$p/cmdline" 2>/dev/null) || continue
        case "$cmd" in
            */bin/openvpn\ --config*)
                [ "$pid" != "$vp" ] && { mark "HEAL_KILL_${pid}"; kill -9 "$pid" 2>/dev/null; }
                ;;
        esac
    done
}

while kill -0 "${WEBLOOP}" 2>/dev/null && kill -0 "${VPNLOOP}" 2>/dev/null; do
    mark "MAINLOOP_ALIVE"
    heal_stale_vpn
    sleep 5
done
mark "MAINLOOP_FALLTHROUGH"
exit 1
