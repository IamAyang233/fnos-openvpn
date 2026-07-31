#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
FNOS_DIR="${FNOS_DIR:-$SCRIPT_DIR/fnos}"

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
info "清理构建缓存..."
go clean -cache 2>/dev/null || true
info "编译 openvpn-web（版本 $VERSION）..."
# 关键坑：Git Bash 下 MSYS 的 /d/... 路径 + 非 ASCII 目录名，go build 会“成功”但静默写不进
# 目标文件（二进制永远停留在旧 mtime，fpk 反复打包旧代码）。必须：
#   1) cygpath -w 转原生 Windows 路径传给 go build；
#   2) 先 rm 掉旧产物再编译，杜绝“写不进/覆盖不了”的静默失败。
BIN_OUT="$FNOS_DIR/app/bin/openvpn-web"
rm -f "$BIN_OUT" 2>/dev/null || true
BIN_OUT_WIN="$(cygpath -w "$BIN_OUT")"
( cd "$SCRIPT_DIR/openvpn-web-src" && GOFLAGS=-mod=mod GOSUMDB=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags "-X main.version=$VERSION" -o "$BIN_OUT_WIN" . ) || error "go build openvpn-web 失败"
# 产物校验：必须为 Linux ELF（Windows 交叉编译经典翻车：不带 GOOS 会产出 PE，装回 NAS 卡 start）
if ! file "$BIN_OUT" 2>/dev/null | grep -q "ELF 64-bit"; then
    error "openvpn-web 不是 Linux ELF（交叉编译失败），终止打包"
fi
info "编译产物校验通过: $(file "$BIN_OUT" | cut -d: -f2 | cut -c1-40)"

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
( cd "$FNOS_DIR/app" && tar -cf "$WORK_DIR/app.tar" --mode=0755 ./bin )
( cd "$FNOS_DIR/app" && tar -rf "$WORK_DIR/app.tar" --mode=0644 --exclude=./bin ./lib )
# 桌面入口 ui：放进 app.tgz 顶层（target/ui），同时 service_postinst 会 symlink 到 /var/apps/{app}/ui
mkdir -p "$WORK_DIR/ui/images"
cp -af "$FNOS_DIR/ui/config"     "$WORK_DIR/ui/config"
cp -af "$FNOS_DIR/ui/index.html" "$WORK_DIR/ui/index.html" 2>/dev/null || true
cp -af "$FNOS_DIR/ui/images/."   "$WORK_DIR/ui/images/"
# 入口图标按 {0} 占位符命名（fnOS 桌面图标约定：icon_64.png / icon_256.png）
[ -f "$FNOS_DIR/ICON.PNG" ]     && cp "$FNOS_DIR/ICON.PNG"     "$WORK_DIR/ui/images/icon_64.png"
[ -f "$FNOS_DIR/ICON_256.PNG" ] && cp "$FNOS_DIR/ICON_256.PNG" "$WORK_DIR/ui/images/icon_256.png"
( cd "$WORK_DIR" && tar -rf "$WORK_DIR/app.tar" --mode=0644 ./ui )
gzip -c "$WORK_DIR/app.tar" > "$WORK_DIR/app.tgz"
APP_TGZ="$WORK_DIR/app.tgz"
CHECKSUM=$(md5sum "$APP_TGZ" 2>/dev/null | cut -d' ' -f1 || md5 -q "$APP_TGZ")

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
uninstall_callback(){ log_step "uninstall_callback"; call_func "service_postuninst" install_log; if [ "$wizard_delete_data" = "true" ]; then echo "Removing files..." | install_log; [ "$(ls -A ${TRIM_PKGHOME} 2>/dev/null)" != "" ] && find ${TRIM_PKGHOME} -mindepth 1 -delete -print | install_log; [ "$(ls -A ${TRIM_PKGVAR} 2>/dev/null)" != "" ] && find ${TRIM_PKGVAR} -mindepth 1 -delete -print | install_log; fi; exit 0; }
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
chmod +x "$PKG_DIR/cmd/"*

# Copy remaining files
cp -af "$FNOS_DIR/config" "$PKG_DIR/"
cp -af "$FNOS_DIR/wizard" "$PKG_DIR/" 2>/dev/null || true
cp "$FNOS_DIR"/*.sc "$PKG_DIR/" 2>/dev/null || true
cp "$FNOS_DIR"/ICON*.PNG "$PKG_DIR/" 2>/dev/null || true
# fnOS 首页图标约定文件名为 ICON.png（小写），Windows 大小写不敏感会导致
# ICON_256.PNG 与 ICON.png 被视为同一文件，故此处显式产出一份 ICON.png。
cp -f "$FNOS_DIR/ICON_256.PNG" "$PKG_DIR/ICON.png" 2>/dev/null || true
# 桌面入口 ui 已打进 app.tgz 的 app/ui/，fpk 顶层不再放 ui 目录
# （顶层 ui/ 不会被 fnOS 识别为入口，详见上方 app.tgz 段落）
cp "$FNOS_DIR/manifest" "$PKG_DIR/manifest"
sed -i.tmp "s/^checksum.*/checksum        = ${CHECKSUM}/" "$PKG_DIR/manifest"
rm -f "$PKG_DIR/manifest.tmp" 2>/dev/null || true

# 4. Package
FPK_NAME="${APPNAME}_${VERSION}_${PLATFORM:-x86}.fpk"
info "打包 -> $FPK_NAME ..."
cd "$PKG_DIR"
tar -cf "$WORK_DIR/pkg.tar" *
# fnOS 首页图标需精确命名为 ICON.png（小写）。Windows 大小写不敏感，
# 磁盘上 ICON.png 会被视作 ICON.PNG，故用 tar 显式以 ICON.png 名义追加一份。
tar -rf "$WORK_DIR/pkg.tar" ICON.png 2>/dev/null || true
gzip -c "$WORK_DIR/pkg.tar" > "$SCRIPT_DIR/$FPK_NAME"
cd "$SCRIPT_DIR"
info "完成: $FPK_NAME ($(du -h "$FPK_NAME" | cut -f1))"
info "安装: 飞牛OS → 应用中心 → 手动安装 → 选择 $FPK_NAME"
