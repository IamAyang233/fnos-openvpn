# OpenVPN FPK「打开」按钮 / isOpen 修复报告（v1.0.32）

**日期**：2026-07-31　**状态**：✅ 已解决（`app status` → `isOpen: true`）

## 症状
应用中心不显示「打开」按钮、桌面无图标，`app status` 的 `control.isOpen` 恒为 `false`，但应用本身 running、8833 端口可达。v1.0.21 → v1.0.31 多轮尝试（iframe / 端口 / 网关 / checkport / 302 修复）均无效。

## 根因（两个叠加的隐蔽问题）

### ① 构建层：go build 静默写不进目标文件（导致「修复从未部署」）
- `build.sh` 里 `go build -o "$FNOS_DIR/app/bin/openvpn-web"`，在 Git Bash 下 MSYS `/d/...` 路径 + 非 ASCII 目录名（`AI项目`）会**静默写不进**目标——go build 报成功，但二进制 mtime 永远停在 12:11。
- 后果：v1.0.28~1.0.31 的所有 fpk 打包的都是**同一份旧二进制**，中间件修复从未真正进包。
- 修复：`cygpath -w` 转原生 Windows 路径 + **先 `rm -f` 再编译** + `file` 校验 ELF 拦截。
- 验证手段：给代码加唯一字符串 `X-OVPN-Public`，重编后 `strings 二进制 | grep` 确认进包。

### ② 结构层：app.tgz 多了一层 `app/` 嵌套（isOpen 恒 false 的真正原因）
- 官方文档（developer.fnnas.com 应用入口章节）：`desktop_uidir=ui` → 入口文件为 `app/ui/config`，安装后位于 **`{TRIM_APPDEST}/ui/config`**（= `target/ui/config`）。**app/ 目录内容直接映射到 target/，app.tgz 内不能有多余的 app/ 层级**（HelloFnos 示例：`app/www` → `target/www`）。
- 我们旧版 app.tgz 打成 `./app/bin`、`./app/ui` → 安装后 `target/app/ui/config`，fnOS 按 `target/ui/config` 找入口 → 找不到 → 不注册桌面入口 → `isOpen=false`。
- 修复：app.tgz 顶层改为 `./bin ./lib ./ui`；service-setup / supervisor 路径同步改 `${TRIM_APPDEST}/bin`、`${TRIM_APPDEST}/lib`；kill_stale 匹配改 `*/bin/openvpn-web*`。
- 附带修复：AuthMiddleWare 放行未登录访问 `/`（根路径返回 200 而非 302），符合 fnOS 打开检测对入口 URL 的探测预期。

## 参考范本（应用商城官方上架应用）
- **miniBill**（tianzongxinyu/miniBill）：端口模式。manifest `service_port=${wizard_port}` + `checkport=true`；ui/config `type:"url"` + `port` + `protocol:"http"` + `url:"/"`。
- **FanControlServer**（guan-ry/FanControlServerApp）：统一网关模式。ui/config `gatewaySocket` + `gatewayPrefix:"/app/FanControlServer"`。

## 验证结果（v1.0.32 已部署）
```
app status openvpn → "isOpen": true, "status": "running", "version": "1.0.32"
GET http://NAS:8833/ → HTTP 200 + X-Ovpn-Public: 1（中间件修复生效）
POST /login (admin/12345678) → 200 登录成功；/admin → 200
```

## v1.0.33 追加：端口 + 统一网关双入口（按上架要求）
按官方文档（gateway-registration 章节）新增统一网关入口，与 8833 端口直连共存：
- **ui/config** 新增第二个入口 `openvpn.Gateway`：`gatewayPrefix:"/app/openvpn"` + `gatewaySocket:"app.sock"`（官方规范：**只填文件名**，socket 放 target 根目录）+ `url:"/app/openvpn"`
- **service-setup / supervisor** 导出 `SOCKET_PATH="${TRIM_APPDEST}/app.sock"` + `GATEWAY_PREFIX="/app/openvpn"`（后端双通道共用同一 handler，互不干扰）
- **manifest** 补 `changelog`（上架更新说明）+ desc 双入口描述
- 验证：isOpen 仍 true；8833 仍 200；`app.sock` 安装后 7s 创建（=后端绑定）；`https://NAS:5667/app/openvpn/` 未登录返回 "invalid token"（fnOS 网关登录态校验生效）。完整链路待浏览器点击桌面图标实测。

## v1.0.34 定稿：纯统一网关模式（用户选定，对齐 FanControlServer 上架先例）
- **ui/config**：只保留唯一网关入口 `openvpn.Application`（`gatewaySocket:"app.sock"` + `gatewayPrefix:"/app/openvpn"` + `url:"/app/openvpn"`）
- **manifest**：删除 `service_port`/`checkport`（后端端口来自 config.json `system.base.web_port`，不依赖 manifest）；`openvpn.sc` 防火墙只留 `1194/udp`
- **main.go**：TCP 监听改为 `127.0.0.1:%s`（Web UI 仅经网关暴露，O-Token 内部钩子走回环不受影响）
- 验证：isOpen=true ✓；`https://NAS:5667/app/openvpn/` → "invalid token"（网关注册+登录态校验）✓；8833 直连 connection refused（安全收敛）✓；app.sock 正常创建 ✓

## v1.0.35 定稿：权限合规整改（上架要求）
官方「应用权限」规范：默认 `run-as=package`；root 仅限「访问包用户无法处理的设备」（OpenVPN 的 TUN/nft 属此官方例外）；**长期运行、对外进程应尽可能非 root**。
- **server.conf** 增加 `user nobody` + `group nogroup`：openvpn 数据通道降权（root 仅启动期创建 TUN / 加载 nft 规则）
- **supervisor** 启动早期：`touch + chown nobody` openvpn-status.log（openvpn 需写）、`chmod 644` config.json（openvpn-auth/钩子以 nobody 读取 token）
- 钩子（client-connect/disconnect）均为纯 curl POST，降权不影响
- **验证降权生效**：`openvpn-status.log` uid=65534（nobody）且 mtime 持续刷新（openvpn 每 60s 写入）= 降权运行健康 ✓
- desc 已附权限说明（root 用途 + 数据通道降权）
- **遗留建议**：openvpn-web（Go 后端）仍以 root 运行，纵深防御可再降权到专用用户（需 useradd + chown 数据卷 + runuser），属可选项

## v1.0.36 / v1.0.37 追加：仪表盘分母统计修复 + 初始化向导修复
- **v1.0.36**：`/ovpn/online-client` 的 total 由「clients/ 目录条目数」改为「pki 客户端证书数（Kind=client）」——原逻辑会被目录杂项（如调试残留）污染（曾显示 0/2 实际 1 个客户端）。
- **v1.0.37（初始化向导）**：
  - `system.base.init_done` 曾误入 /settings forbidden map → 向导 finish() 写入被静默忽略 → init_done 恒 false → **每次打开都自动弹向导**。已移除 forbidden，并在 /api/bootstrap 增加「CA 存在即视为已初始化」兼容存量安装（新装仍正常弹向导）
  - 向导 s1 密码校验与后端强密码规则对齐（≥12 位含大小写数字特殊符，原前端只查 ≥8 位导致弱密码前端过、后端 400）
  - s2/s3/s4 步骤检查正常（服务器地址/端口/proto 保存、创建用户、生成并下载客户端配置）

## v1.0.38 追加：系统性 bug 排查（用户要求全面检查）
- 修复：设置页改密码校验与后端强密码规则对齐（原 ≥8 位会前端过、后端拒且 catch 空函数**静默失败**）；改端口/协议/子网后提示需「重启服务」生效；session 过期（401）全局自动跳登录页
- 排查确认无问题：客户端名路径穿越（`clientNameRe ^[a-zA-Z0-9_-]{1,64}$` + `os.OpenRoot` 双重防护）、网关 BASE 前缀（request 封装）、XSS esc() 转义、StaticFS 防穿越、前后端密码一致性、sendCommand 正则预编译
- 遗留（非 bug）：marker 堆积 1489 个（v1.0.19 #11，建议文件管理器清理 `*.marker`）、admin 弱密码 12345678（建议改强密码）、openvpn-web 仍 root（可选降权）

## v1.0.39 追加：UI 分页完善（用户要求）
- 连接记录每页 10 条（原 50 条导致记录少时看起来不分页）
- 客户端、用户列表新增分页（每页 10 条）；在线客户端分页（每页 5 条）
- 实现：通用 `renderLocalPager(prefix,page,perPage,total,onChange)` + 三个表 pager 容器（复用 .pager 样式，≤每页条数时自动隐藏）
- 顺带：index.html 历史 CRLF 统一转 LF

## v1.0.40 追加：密码策略放宽 + 在线分页 3 条（用户要求）
- 密码策略：`isValidPassword` 放宽为「至少 8 位」（原 12 位强密码），同步更新 3 处后端校验消息 + 前端向导/设置页提示
- 在线客户端分页每页 3 条（原 5 条）

## v1.0.41 追加：移除冗余「防火墙」页（用户确认）
- 该页仅静态端口信息（且 8833 已过时——纯网关模式不对外），删除导航 + 页面
- 拉黑/限速功能保留在在线客户端操作按钮（/ovpn/firewall API），后端 firewall.go 不动
- 顺带清理 loadSettings 中对已删元素 fwWeb/fwVpn 的赋值（避免 null 运行时错误）

## 关键产物
- `apps/openvpn-as-fpk/openvpn_1.0.41_x86.fpk`（30M，已安装运行）
- 修复涉及：`build.sh`（编译路径 + app.tgz 结构）、`fnos/cmd/service-setup`、`fnos/app/bin/openvpn-supervisor.sh`、`fnos/app/bin/ovpn-helper.sh`（降权）、`openvpn-web-src/main.go`（AuthMiddleWare/TCP 回环/init_done/bootstrap/online-client 统计）、`templates/static/js/index.js`（向导密码校验）、`fnos/manifest`、`fnos/ui/config`、`fnos/openvpn.sc`
