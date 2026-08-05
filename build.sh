#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
FNOS_DIR="${FNOS_DIR:-$SCRIPT_DIR/fnos}"

# Go 工具链版本锁定：go.mod 声明 go 1.25.4，本机若装了更高版本 Go（如 1.26+）
# 会因 std 包内部结构变化报 "package ... is not in std" 编译失败。
# 优先直接用已下载的 toolchain 二进制（GOMODCACHE 下），避免 GOTOOLCHAIN
# 自动下载逻辑在国内网络卡死（proxy.golang.org 被墙）。
TC_BIN="${HOME}/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.4.windows-amd64/bin"
if [ -x "${TC_BIN}/go.exe" ] || [ -x "${TC_BIN}/go" ]; then
    # GOROOT 必须用 Windows 原生路径（go 工具不认 /c/ MSYS 格式）
    TC_ROOT_WIN="$(echo "${HOME}/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.4.windows-amd64" | sed 's|^/\([a-zA-Z]\)/|\1:/|')"
    export GOROOT="${TC_ROOT_WIN}"
    export PATH="${TC_BIN}:${PATH}"
    echo "[INFO] 使用本地 Go toolchain 1.25.4 (GOROOT=${TC_ROOT_WIN})"
else
    export GOTOOLCHAIN=go1.25.4
fi

# 工具检测：打包不强制依赖 python3（非所有人都有）。
#  - GNU tar（Linux/macOS 标准环境）：tar 打包 + md5sum + stat，零 python 依赖；
#  - 无 GNU tar（如精简 Git Bash 的 bsdtar，不支持 --mode 控制权限）：python 兜底。
# 两者都缺 → 报错退出。
if tar --version 2>/dev/null | grep -qi "gnu tar"; then
    PKG_BACKEND=tar
else
    PKG_BACKEND=python
fi
PY_CMD=""
if command -v python3 >/dev/null 2>&1; then PY_CMD=python3
elif command -v python >/dev/null 2>&1; then PY_CMD=python
elif command -v py >/dev/null 2>&1; then PY_CMD="py -3"
fi
[ "$PKG_BACKEND" = "python" ] && [ -z "$PY_CMD" ] && { echo "[ERROR] 打包需要 GNU tar 或 python3（当前环境都没有）" >&2; exit 1; }
# 非 ASCII/含空格的路径转 Windows 原生路径（python 不认 /d/ 格式）
winpath() { echo "$1" | sed 's|^/\([a-zA-Z]\)/|\1:/|'; }

# 说明：打包过程不依赖 Python，改用 GNU tar --mode + append 强制归档内权限
# （见下方“打包 app.tgz”段落）。仅一次性抽库工具 fetch_libs.py 需要 Python。

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC}  $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# 1. Validate
info "检查插件文件..."
[ -d "$FNOS_DIR/cmd" ]    || error "缺少 fnos/cmd/"
[ -d "$FNOS_DIR/config" ] || error "缺少 fnos/config/"
[ -d "$FNOS_DIR/ui" ]     || error "缺少 fnos/ui/"
[ -d "$FNOS_DIR/app" ]    || error "缺少 fnos/app/"
[ -f "$FNOS_DIR/manifest" ]      || error "缺少 fnos/manifest"
[ -f "$FNOS_DIR/ICON.PNG" ]      || error "缺少 fnos/ICON.PNG"
[ -f "$FNOS_DIR/ICON_256.PNG" ]  || error "缺少 fnos/ICON_256.PNG"

APPNAME=$(grep "^appname" "$FNOS_DIR/manifest" | awk -F'=' '{print $2}' | tr -d ' ')
VERSION=$(grep "^version" "$FNOS_DIR/manifest" | awk -F'=' '{print $2}' | tr -d ' ')
PLATFORM=$(grep "^platform" "$FNOS_DIR/manifest" | awk -F'=' '{print $2}' | tr -d ' ')
[ -z "$APPNAME" ] && error "manifest 中缺少 appname"
[ -z "$VERSION" ] && error "manifest 中缺少 version"
info "应用: $APPNAME  版本: $VERSION  平台: ${PLATFORM:-x86}"

# 1.5 编译 openvpn-web 二进制（注入版本号，与 manifest 对齐；不再依赖手动预编译）
# 注意：go:embed 的文件变更在某些 Go 版本下不会被构建缓存感知，导致嵌入仍是旧内容。
# 故这里先 go clean -cache 再 -a 全量重编，保证前端/模板改动 100% 进二进制。
# SKIP_BUILD=1 时跳过编译（二进制已手动就绪，如 toolchain 环境异常时的应急路径）。
if [ -z "${SKIP_BUILD:-}" ]; then
info "清理构建缓存..."
go clean -cache 2>/dev/null || true
info "编译 openvpn-web（版本 $VERSION）..."
# 关键坑：Git Bash 下 MSYS 的 /d/... 路径 + 非 ASCII 目录名，go build 会“成功”但静默写不进
# 目标文件（二进制永远停留在旧 mtime，fpk 反复打包旧代码）。必须：
#   1) cygpath -w 转原生 Windows 路径传给 go build；
#   2) 先 rm 掉旧产物再编译，杜绝“写不进/覆盖不了”的静默失败。
BIN_OUT="$FNOS_DIR/app/bin/openvpn-web"
rm -f "$BIN_OUT" 2>/dev/null || true
# 架构：优先环境变量 ARCH，否则从 manifest platform 推断（x86→amd64, arm→arm64）
GOARCH_VAL="${ARCH:-}"
if [ -z "$GOARCH_VAL" ]; then
    case "$PLATFORM" in
        arm) GOARCH_VAL=arm64 ;;
        *)   GOARCH_VAL=amd64 ;;
    esac
fi
# cygpath 在某些 Git Bash 环境不可用，用 sed 转原生 Windows 路径（python 不认 /d/ 格式）
if command -v cygpath >/dev/null 2>&1; then
    BIN_OUT_WIN="$(cygpath -w "$BIN_OUT")"
else
    BIN_OUT_WIN="$(echo "$BIN_OUT" | sed 's|^/\([a-zA-Z]\)/|\1:/|')"
fi
( cd "$SCRIPT_DIR/openvpn-web-src" && GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH_VAL go build -buildvcs=false -a -ldflags "-X main.version=$VERSION" -o "$BIN_OUT_WIN" . ) || error "go build openvpn-web 失败"
# 产物校验：必须为 Linux ELF（Windows 交叉编译经典翻车：不带 GOOS 会产出 PE，装回 NAS 卡 start）
if command -v file >/dev/null 2>&1; then
    if ! file "$BIN_OUT" 2>/dev/null | grep -q "ELF 64-bit"; then
        error "openvpn-web 不是 Linux ELF（交叉编译失败），终止打包"
    fi
    info "编译产物校验通过: $(file "$BIN_OUT" | cut -d: -f2 | cut -c1-40)"
else
    # file 命令不可用（精简 Git Bash），用 python 检查 ELF magic + 架构
    BIN_OUT_WIN_CHECK="$(echo "$BIN_OUT" | sed 's|^/\([a-zA-Z]\)/|\1:/|')"
    if ! python -c "
import sys
with open(sys.argv[1],'rb') as f: h=f.read(20)
if h[:4] != b'\x7fELF': sys.exit(1)
print('ELF', '64-bit' if h[4]==2 else '32-bit', 'machine=0x%x' % int.from_bytes(h[18:20],'little'))
" "$BIN_OUT_WIN_CHECK"; then
        error "openvpn-web 不是 Linux ELF（交叉编译失败），终止打包"
    fi
fi
fi  # SKIP_BUILD

# 2. Create app.tgz
info "打包 app.tgz ..."
# 每次使用唯一 staging 目录，避开 noacl 文件系统上 rm -rf 残留导致的冲突
WORK_DIR="$SCRIPT_DIR/.build_$$"
rm -rf "$WORK_DIR" 2>/dev/null || true
mkdir -p "$WORK_DIR"
trap 'rm -rf "$WORK_DIR" 2>/dev/null || :' EXIT
# 官方规范（developer.fnnas.com/docs/core-concepts/app-entry）：desktop_uidir=ui → 入口文件为
# app/ui/config → 安装后位于 {TRIM_APPDEST}/ui/config（= target/ui/config）。
# 关键：app/ 目录内容【直接映射到 target/】，app.tgz 内不能有多余的 app/ 层级！
# 旧版打成 ./app/bin、./app/ui → 安装后 target/app/ui/config，fnOS 找不到入口 → isOpen=false。
# 修正后 app.tgz 顶层为 ./bin、./lib、./ui → target/bin、target/lib、target/ui。
# 打包 app.tgz：GNU tar 优先（Linux/macOS 无需 python），python 兜底（Windows bsdtar）。
# 官方规范：app/ 目录内容【直接映射到 target/】，app.tgz 顶层为 ./bin、./lib、./ui。
APP_TGZ="$WORK_DIR/app.tgz"
if [ "$PKG_BACKEND" = "tar" ]; then
    info "用 GNU tar 打包 app.tgz ..."
    APPROOT="$WORK_DIR/approot"
    mkdir -p "$APPROOT"
    cp -r "$FNOS_DIR/app/bin" "$APPROOT/bin"
    [ -d "$FNOS_DIR/app/lib" ] && cp -r "$FNOS_DIR/app/lib" "$APPROOT/lib"
    cp -r "$FNOS_DIR/ui" "$APPROOT/ui" 2>/dev/null || true
    # 入口图标按 {0} 占位符命名（fnOS 桌面图标约定：icon_64.png / icon_256.png）
    mkdir -p "$APPROOT/ui/images"
    cp -f "$FNOS_DIR/ICON.PNG" "$APPROOT/ui/images/icon_64.png" 2>/dev/null || true
    cp -f "$FNOS_DIR/ICON_256.PNG" "$APPROOT/ui/images/icon_256.png" 2>/dev/null || true
    # 归档内权限：bin 可执行 0755，lib/ui 数据 0644。
    # noacl 盘上 chmod 源文件是空操作，故 chmod staging 副本 + tar 保留 mode（不设 --mode）。
    chmod -R 0644 "$APPROOT/lib" "$APPROOT/ui" 2>/dev/null || true
    chmod -R 0755 "$APPROOT/bin"
    (cd "$APPROOT" && tar -czf "$APP_TGZ" --owner=0 --group=0 --transform 's|^\./||' .)
else
    info "用 python 打包 app.tgz ..."
    WIN_WORK="$(winpath "$WORK_DIR")"
    WIN_FNOS="$(winpath "$FNOS_DIR")"
    "$PY_CMD" - "$WIN_WORK" "$WIN_FNOS" << 'PY_PACK'
import sys, os, gzip, tarfile
work, fnos = sys.argv[1], sys.argv[2]
app_dir = os.path.join(fnos, 'app')

def add_tree(tf, base, mode):
    for dp, _, fs in os.walk(base):
        for f in fs:
            src = os.path.join(dp, f)
            rel = os.path.relpath(src, app_dir).replace(os.sep, '/')
            ti = tf.gettarinfo(src, rel)
            ti.mode = mode; ti.uid = ti.gid = 0; ti.uname = ti.gname = ''
            with open(src, 'rb') as fh: tf.addfile(ti, fh)

with gzip.open(os.path.join(work, 'app.tgz'), 'wb') as gz:
    with tarfile.open(fileobj=gz, mode='w') as tf:
        add_tree(tf, os.path.join(app_dir, 'bin'), 0o755)
        lib_dir = os.path.join(app_dir, 'lib')
        if os.path.isdir(lib_dir):
            add_tree(tf, lib_dir, 0o644)
        ui_dir = os.path.join(fnos, 'ui')
        work_ui = os.path.join(work, 'ui')
        if os.path.isdir(ui_dir):
            import shutil
            if os.path.isdir(work_ui):
                shutil.rmtree(work_ui)
            shutil.copytree(ui_dir, work_ui)
            for src_name, dst_name in [('ICON.PNG', 'icon_64.png'), ('ICON_256.PNG', 'icon_256.png')]:
                s = os.path.join(fnos, src_name)
                if os.path.isfile(s):
                    img_dir = os.path.join(work_ui, 'images')
                    os.makedirs(img_dir, exist_ok=True)
                    shutil.copy2(s, os.path.join(img_dir, dst_name))
            for dp, _, fs in os.walk(work_ui):
                for f in fs:
                    src = os.path.join(dp, f)
                    rel = 'ui/' + os.path.relpath(src, work_ui).replace(os.sep, '/')
                    ti = tf.gettarinfo(src, rel)
                    ti.mode = 0o644; ti.uid = ti.gid = 0; ti.uname = ti.gname = ''
                    with open(src, 'rb') as fh: tf.addfile(ti, fh)
print('[python] app.tgz done')
PY_PACK
fi
if [ ! -f "$APP_TGZ" ]; then
    error "app.tgz 生成失败"
fi
if command -v md5sum >/dev/null 2>&1; then
    CHECKSUM=$(md5sum "$APP_TGZ" | awk '{print $1}')
else
    CHECKSUM=$("$PY_CMD" -c "import hashlib,sys;print(hashlib.md5(open(sys.argv[1],'rb').read()).hexdigest())" "$(winpath "$APP_TGZ")")
fi

# 3. Assemble fpk
info "组装 fpk ..."
PKG_DIR="$WORK_DIR/package"
mkdir -p "$PKG_DIR/cmd"
cp "$APP_TGZ" "$PKG_DIR/app.tgz"

# Inline shared/cmd/common (lifecycle framework)
cat > "$PKG_DIR/cmd/common" << 'SHARED_COMMON'
#!/bin/bash
MV="/bin/mv -f"; RM="/bin/rm -rf"; CP="/bin/cp -rfp"; MKDIR="/bin/mkdir -p"
LN="/bin/ln -nsf"; TEE="/usr/bin/tee -a"; RSYNC="/bin/rsync -avh"; TAR="/bin/tar"
if [ -z "${TRIM_PKGVAR:-}" ]; then echo "ERROR: TRIM_PKGVAR 未设置" >&2; exit 1; fi
case "${TRIM_PKGVAR}" in /vol*) ;; *) echo "ERROR: TRIM_PKGVAR=${TRIM_PKGVAR} 不在数据卷上" >&2; exit 1 ;; esac
/bin/mkdir -p "${TRIM_PKGVAR}" 2>/dev/null || true
INST_ETC="/var/apps/${TRIM_APPNAME}/etc"; INST_VARIABLES="${INST_ETC}/installer-variables"
INST_LOG="/var/log/apps/${TRIM_APPNAME}.log"
FWPORTS_FILE="/var/apps/${TRIM_APPNAME}/etc/${TRIM_APPNAME}.sc"
SHARE_PATH="/var/apps/${TRIM_APPNAME}/shares/${TRIM_APPNAME}"
LOG_FILE="${TRIM_PKGVAR}/${TRIM_APPNAME}.log"; PID_FILE="${TRIM_PKGVAR}/${TRIM_APPNAME}.pid"
SVC_WAIT_TIMEOUT=15; SVC_CWD="${TRIM_PKGVAR}"; SVC_BACKGROUND=y; SVC_WRITE_PID=y; SVC_QUIET=y
DOCKER_NAME=""; DNAME="${TRIM_APPNAME}"; SVC_NO_REDIRECT=""
OUT=/dev/null; [ -z "${SVC_NO_REDIRECT}" ] && OUT="${LOG_FILE}"
error_exit() { local msg="$1"; [ -n "${TRIM_TEMP_LOGFILE:-}" ] && echo "$msg" > "${TRIM_TEMP_LOGFILE}"; echo "ERROR: $msg" >&2; exit 1; }
validate_preinst()   { echo "validate_preinst"; }
service_preinst()    { echo "service_preinst"; }
service_postinst()   { echo "service_postinst"; }
service_preuninst()  { echo "service_preuninst"; }
service_postuninst() { echo "service_postuninst"; }
validate_preupgrade(){ echo "validate_preupgrade"; }
service_preupgrade() { echo "service_preupgrade"; }
service_save()       { echo "service_save"; }
service_restore()    { echo "service_restore"; }
service_postupgrade(){ echo "service_postupgrade"; }
service_preconfig()  { echo "service_preconfig"; }
service_postconfig() { echo "service_postconfig"; }
check_docker() { FILE_PATH="${TRIM_APPDEST}/app/docker-compose.yaml"; if [ -f "$FILE_PATH" ]; then DOCKER_NAME=$(cat $FILE_PATH | grep "container_name" | awk -F ':' '{print $2}' | xargs); fi; }; check_docker
install_log() { local _msg_="$@"; if [ -z "${_msg_}" ]; then while IFS=$'\n' read -r line; do install_log "${line}"; done; else echo -e "$(date +'%Y/%m/%d %H:%M:%S')\t${_msg_}" 1>&2; fi; }
call_func() { FUNC=$1; LOG=$2; if type "${FUNC}" 2>/dev/null | grep -q 'function' 2>/dev/null; then if [ -n "${LOG}" ]; then install_log "Begin ${FUNC}"; eval ${FUNC} 2>&1 | ${LOG}; install_log "End ${FUNC}"; else echo "Begin ${FUNC}" >> ${LOG_FILE}; eval ${FUNC} >> ${LOG_FILE}; echo "End ${FUNC}" >> ${LOG_FILE}; fi; fi; }
get_key_value() { cat $1 | grep -F "$2" | awk -F "=" '{print $2}' | tr -d ' ' | tr -d '"'; }
log_step() { install_log "===> Step $1. STATUS=${TRIM_APP_STATUS}"; }
save_wizard_variables() { [ -e "${INST_VARIABLES}" ] && $RM "${INST_VARIABLES}"; [ -n "${GROUP}" ] && echo "GROUP=${GROUP}" >> ${INST_VARIABLES}; [ -n "${SHARE_PATH}" ] && echo "SHARE_PATH=${SHARE_PATH}" >> ${INST_VARIABLES}; }
sync_var_folder() { if [ -d ${TRIM_APPDEST}/var -a "$(ls -A ${TRIM_APPDEST}/var 2>/dev/null)" ]; then $RSYNC --ignore-existing --remove-source-files ${TRIM_APPDEST}/var/ ${TRIM_PKGVAR}; find ${TRIM_APPDEST}/var -type f -exec sh -c 'x="{}"; mv "$x" "${x}.new"' \;; $RSYNC --remove-source-files ${TRIM_APPDEST}/var/ ${TRIM_PKGVAR}; $RM ${TRIM_APPDEST}/var; fi; }
install_init()      { log_step "install_init"; call_func "validate_preinst" install_log; call_func "service_preinst" install_log; exit 0; }
install_callback()  { log_step "install_callback"; call_func "save_wizard_variables" install_log; sync_var_folder; call_func "service_postinst" install_log; exit 0; }
uninstall_init()    { log_step "uninstall_init"; stop_daemon; call_func "service_preuninst" install_log; exit 0; }
uninstall_callback(){ log_step "uninstall_callback"; call_func "service_postuninst" install_log; if [ "$wizard_delete_data" = "yes" ]; then echo "Removing files..." | install_log; [ "$(ls -A ${TRIM_PKGHOME} 2>/dev/null)" != "" ] && find ${TRIM_PKGHOME} -mindepth 1 -delete -print | install_log; [ "$(ls -A ${TRIM_PKGVAR} 2>/dev/null)" != "" ] && find ${TRIM_PKGVAR} -mindepth 1 -delete -print | install_log; fi; exit 0; }
upgrade_init()      { log_step "upgrade_init"; call_func "validate_preupgrade" install_log; stop_daemon; call_func "service_preupgrade" install_log; call_func "service_save" install_log; exit 0; }
fix_data_ownership() { if [ -n "${TRIM_USERNAME}" ] && [ -n "${TRIM_GROUPNAME}" ]; then local owner="${TRIM_USERNAME}:${TRIM_GROUPNAME}"; for dir in "${TRIM_PKGVAR}" "${TRIM_PKGETC}" "${TRIM_PKGHOME}"; do [ -d "$dir" ] && chown -R "$owner" "$dir" 2>/dev/null || true; done; fi; }
upgrade_callback()  { log_step "upgrade_callback"; call_func "fix_data_ownership" install_log; call_func "service_restore" install_log; call_func "service_postupgrade" install_log; exit 0; }
config_init()       { log_step "config_init"; call_func "service_preconfig" install_log; exit 0; }
config_callback()   { log_step "config_callback"; call_func "service_postconfig" install_log; exit 0; }
start_daemon() { if [ -n "$DOCKER_NAME" ]; then return; fi; i=0; [ -z "${SVC_QUIET}" ] && { [ -z "${SVC_KEEP_LOG}" ] && date > ${LOG_FILE} || date >> ${LOG_FILE}; }; call_func "service_prestart"; printf "%s" "$SERVICE_COMMAND" | while read -r service || [ -n "$service" ]; do i=$((i + 1)); [ -z "${SVC_QUIET}" ] && echo "Starting ${DNAME} command ${service}" >> ${LOG_FILE}; if [ -n "${service}" ]; then [ -n "${SVC_CWD}" ] && cd ${SVC_CWD}; if [ -z "${SVC_BACKGROUND}" ]; then ${service} >> ${OUT} 2>&1; else ${service} >> ${OUT} 2>&1 & fi; if [ -n "${SVC_WRITE_PID}" -a -n "${SVC_BACKGROUND}" -a -n "${PID_FILE}" ]; then [ $i -eq 1 ] && printf "%s" "$!" > ${PID_FILE} || printf "\n%s" "$!" >> ${PID_FILE}; else wait_for_status 0 ${SVC_WAIT_TIMEOUT:=20}; fi; fi; done; }
stop_daemon() { if [ -n "$DOCKER_NAME" ]; then return; fi; if [ -n "${PID_FILE}" -a -r "${PID_FILE}" ]; then for pid in $(cat "${PID_FILE}"); do [ -z "$pid" ] && continue; kill -TERM ${pid} >> ${LOG_FILE} 2>&1; wait_for_status 1 ${SVC_WAIT_TIMEOUT:=20} ${pid} || kill -KILL ${pid} >> ${LOG_FILE} 2>&1; done; [ -f "${PID_FILE}" ] && rm -f "${PID_FILE}"; fi; call_func "service_poststop"; }
daemon_status() { if [ -n "$DOCKER_NAME" ]; then docker inspect $DOCKER_NAME | grep -q '"Status": "running",' || exit 1; return; fi; status=0; [ -z "${1}" ] && pid_list=$(cat ${PID_FILE} 2>/dev/null) || pid_list=${1}; if [ -n "${pid_list}" ]; then for pid in ${pid_list}; do kill -0 ${pid} > /dev/null 2>&1; status=$((status + $?)); done; [ $status -ne 0 ] && { rm -f "${PID_FILE}"; return 1; } || return 0; else return 1; fi; }
wait_for_status() { counter=${2}; counter=${counter:=20}; while [ ${counter} -gt 0 ]; do daemon_status ${3}; [ $? -eq $1 ] && return; counter=$((counter - 1)); sleep 1; done; return 1; }
SHARED_COMMON

# cmd/main
cat > "$PKG_DIR/cmd/main" << 'EOF'
#!/bin/bash
COMMON=$(dirname $0)"/common"; [ -r "${COMMON}" ] && . "${COMMON}"
SVC_SETUP=$(dirname $0)"/service-setup"; [ -r "${SVC_SETUP}" ] && . "${SVC_SETUP}"
case $1 in
start) if daemon_status; then exit 0; else start_daemon; exit $?; fi ;;
stop) stop_daemon; exit 0 ;;
status) if daemon_status; then echo "${DNAME} is running"; exit 0; else echo "${DNAME} is not running"; exit 3; fi ;;
log) LINES="${2:-100}"; [ -f "${LOG_FILE}" ] && tail -n "$LINES" "${LOG_FILE}" || echo "No log"; exit 0 ;;
*) exit 1 ;;
esac
EOF

# cmd/installer
cat > "$PKG_DIR/cmd/installer" << 'EOF'
#!/bin/bash
COMMON=$(dirname $0)"/common"; [ -r "${COMMON}" ] && . "${COMMON}"
SVC_SETUP=$(dirname $0)"/service-setup"; [ -r "${SVC_SETUP}" ] && . "${SVC_SETUP}"
case "$1" in
install_init) install_init ;; install_callback) install_callback ;;
uninstall_init) uninstall_init ;; uninstall_callback) uninstall_callback ;;
upgrade_init) upgrade_init ;; upgrade_callback) upgrade_callback ;;
config_init) config_init ;; config_callback) config_callback ;;
*) exit 1 ;;
esac
EOF

# Hook scripts
for hook in install_init install_callback uninstall_init uninstall_callback upgrade_init upgrade_callback config_init config_callback; do
cat > "$PKG_DIR/cmd/$hook" << HOOK_EOF
#!/bin/bash
COMMON=\$(dirname \$0)"/common"; [ -r "\${COMMON}" ] && . "\${COMMON}"
SVC_SETUP=\$(dirname \$0)"/service-setup"; [ -r "\${SVC_SETUP}" ] && . "\${SVC_SETUP}"
${hook}
HOOK_EOF
done

# Overlay app-specific cmd/
cp "$FNOS_DIR"/cmd/* "$PKG_DIR/cmd/" 2>/dev/null || true
chmod +x "$PKG_DIR/cmd/"* 2>/dev/null || "$PY_CMD" - "$(winpath "$PKG_DIR")" << 'PY_CHMOD'
import os, sys
cmd_dir = os.path.join(sys.argv[1], 'cmd')
for f in os.listdir(cmd_dir):
    os.chmod(os.path.join(cmd_dir, f), 0o755)
print('[python] chmod done')
PY_CHMOD

# Copy remaining files
cp -af "$FNOS_DIR/config" "$PKG_DIR/"
cp -af "$FNOS_DIR/wizard" "$PKG_DIR/" 2>/dev/null || true
cp "$FNOS_DIR"/*.sc "$PKG_DIR/" 2>/dev/null || true
cp "$FNOS_DIR"/ICON*.PNG "$PKG_DIR/" 2>/dev/null || true
# fnOS 首页图标约定文件名为 ICON.png（小写）。tar 后端在 Linux 上直接产出 ICON.png；
# python 后端（Windows 大小写不敏感）由 PY_PKG 以 ICON.png 名义追加进 fpk，无需 cp。
if [ "$PKG_BACKEND" = "tar" ]; then
    cp -f "$FNOS_DIR/ICON_256.PNG" "$PKG_DIR/ICON.png" 2>/dev/null || true
fi
# 桌面入口 ui 已打进 app.tgz 的 app/ui/，fpk 顶层不再放 ui 目录
# （顶层 ui/ 不会被 fnOS 识别为入口，详见上方 app.tgz 段落）
cp "$FNOS_DIR/manifest" "$PKG_DIR/manifest"
# 统一用 python 写 checksum（避免 sed -i.tmp 在部分环境残留 manifest.tmp 进包）
"$PY_CMD" - "$(winpath "$PKG_DIR")" "$CHECKSUM" << 'PY_MF'
import os, re, sys
pkg_dir, checksum = sys.argv[1], sys.argv[2]
mf = os.path.join(pkg_dir, 'manifest')
content = open(mf, encoding='utf-8').read()
content = re.sub(r'(?m)^checksum\s*=.*$', f'checksum        = {checksum}', content)
open(mf, 'w', encoding='utf-8', newline='\n').write(content)
print('[python] checksum written')
PY_MF
# 清理 sed 可能残留的临时文件
rm -f "$PKG_DIR/manifest.tmp" 2>/dev/null || true
rm -f "$PKG_DIR/manifest"*tmp* 2>/dev/null || true
"$PY_CMD" - "$(winpath "$PKG_DIR")" << 'PY_CLEAN'
import os, sys
pkg_dir = sys.argv[1]
for f in os.listdir(pkg_dir):
    if 'tmp' in f.lower() and f != 'app.tgz':
        try: os.remove(os.path.join(pkg_dir, f)); print('清理:', f)
        except: pass
PY_CLEAN

# 4. Package
FPK_NAME="${APPNAME}_${VERSION}_${PLATFORM:-x86}.fpk"
info "打包 -> $FPK_NAME ..."
cd "$PKG_DIR"
if [ "$PKG_BACKEND" = "tar" ]; then
    # GNU tar 打包 fpk：先 chmod staging 副本（cmd 0755 / 其余 0644），tar 保留权限。
    # ICON.png（小写，fnOS 首页图标约定）已在上方 tar 后端分支复制到 package 目录。
    chmod -R 0755 "$PKG_DIR/cmd" 2>/dev/null || true
    find "$PKG_DIR" -type f ! -path "$PKG_DIR/cmd/*" -exec chmod 0644 {} + 2>/dev/null || true
    tar -czf "$SCRIPT_DIR/$FPK_NAME" --owner=0 --group=0 --transform 's|^\./||' -C "$PKG_DIR" .
else
    # python 打包 fpk：cmd/ 0755，其余 0644；ICON.png 以显式名义追加
    # （Windows 大小写不敏感，磁盘上 ICON.png 与 ICON.PNG 视为同名，无法直接 cp）。
    "$PY_CMD" - "$(winpath "$PKG_DIR")" "$(winpath "$SCRIPT_DIR")" "$FPK_NAME" << 'PY_PKG'
import os, sys, tarfile, gzip
pkg_dir, script_dir, fpk_name = sys.argv[1], sys.argv[2], sys.argv[3]
out = os.path.join(script_dir, fpk_name)

entries = []
for name in sorted(os.listdir(pkg_dir)):
    src = os.path.join(pkg_dir, name)
    if os.path.isdir(src):
        for dp, _, fs in os.walk(src):
            for f in fs:
                fsrc = os.path.join(dp, f)
                rel = os.path.relpath(fsrc, pkg_dir).replace(os.sep, '/')
                entries.append((fsrc, rel))
    else:
        entries.append((src, name))

with gzip.open(out, 'wb') as gz:
    with tarfile.open(fileobj=gz, mode='w') as tf:
        for fsrc, rel in entries:
            ti = tf.gettarinfo(fsrc, rel)
            ti.uid = ti.gid = 0; ti.uname = ti.gname = ''
            ti.mode = 0o755 if rel.startswith('cmd/') else 0o644
            with open(fsrc, 'rb') as fh:
                tf.addfile(ti, fh)
        icon_src = os.path.join(pkg_dir, 'ICON_256.PNG')
        if os.path.isfile(icon_src):
            ti = tf.gettarinfo(icon_src, 'ICON.png')
            ti.uid = ti.gid = 0; ti.uname = ti.gname = ''
            ti.mode = 0o644
            with open(icon_src, 'rb') as fh:
                tf.addfile(ti, fh)
print('[python] fpk package done')
PY_PKG
fi
cd "$SCRIPT_DIR"
if command -v stat >/dev/null 2>&1; then
    FPK_SIZE=$(stat -c%s "$FPK_NAME")
else
    FPK_SIZE=$("$PY_CMD" -c "import os;print(os.path.getsize('$(winpath "$FPK_NAME")'))")
fi
info "完成: $FPK_NAME ($(echo "scale=1; $FPK_SIZE/1048576" | bc 2>/dev/null || awk -v s="$FPK_SIZE" 'BEGIN{printf "%.1f", s/1048576}') MB)"
info "安装: 飞牛OS → 应用中心 → 手动安装 → 选择 $FPK_NAME"
