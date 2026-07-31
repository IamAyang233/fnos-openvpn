# OpenVPN FPK 代码审查（瑕疵清单）

审查范围：`openvpn-web-src`（Go 后端 + 原生 JS 前端）、`fnos/`（打包脚本、supervisor、ovpn-helper）。
当前线上版本：**v1.0.18**。以下按严重级别排序，均附文件:行号与修复建议。

---

## 🔴 高 / 安全

### 1. `genRandomString` 用 `math/rand` 生成敏感密钥（非密码学安全）
- 位置：`main.go:456` `genRandomString`；调用点 `config.go:104/110/111/179`
- 影响：`secretKey`（会话签名，`main.go:539` gormsessions）、`system.base.token`（O-Token API 鉴权，`config.go:179`）、`server_cn/server_name` 均由它生成。`math/rand` 可预测，密钥强度不足。
- 现状：首次启动写入 `config.json` 后持久化，本地攻击者读到即破解——但用可预测 RNG 仍是坏习惯，且若 `config.json` 被意外清空会重新生成弱密钥。
- 修复：敏感密钥改用 `crypto/rand` 生成（如 `crypto/rand` + base64/hex 编码）。非敏感的（captcha key）可保留。

---

## 🟠 中 / 功能与数据正确性

### 2. 连接记录「时间」可能显示 `—`（断开未回写 time_unix）
- 位置：`history.go:116` `RecordDisconnect`（只更新 bytes/duration，**不更新 time_unix`**）
- 影响：OpenVPN 在 `client-connect` 时未必提供 `time_unix`（为空→存 0）。若 connect 存了 0、disconnect 又不回写，这条记录 `time_unix=0` → 前端按 `/^1970-/` 过滤显示 `—`。
- 验证：当前库里部分记录有真实时间（说明该环境 connect 提供了 time_unix），但逻辑上不保证，跨环境/版本会踩。
- 修复（两处，低风险）：
  - `ovpn-helper.sh:246` `add_history`：`time_unix=${time_unix:-$(date +%s)}`，connect 时兜底为当前时间戳。
  - `history.go:122` `RecordDisconnect`：UPDATE 时加 `"time_unix": h.TimeUnix`，但仅当现有行 `time_unix=0` 才覆盖（保留 connect 时间优先）。

### 3. CSV 导出时间用 UTC，与界面北京时间差 8 小时
- 位置：`main.go:1827` `time.Unix(h.TimeUnix, 0).Format(...)`（/ovpn/history/export）
- 影响：界面连接记录已是北京时间（`history.go:68` `.In(cst)`），但导出 CSV 用 `time.Unix` 默认 UTC → 同一条记录界面 23:59 / 导出 15:59，对不上。
- 同类：`parseCert`/`parseCrl`（`main.go:332/385`）用 `.Local()`（NAS=UTC）格式化证书过期时间，也与北京时间差 8h。
- 修复：导出与证书时间统一 `.In(cst)`（history.go 已有 `cst`）。

### 4. `version` 硬编码 `1.0.0`，与真实版本脱节
- 位置：`main.go:110` `version = "1.0.0"`；`build.sh` 未注入 `-ldflags -X main.version`
- 影响：当前 `/admin` 把 `"v"+version` 传给模板（`main.go:768`），虽 index.html 未渲染，但属潜在 bug（将来加页脚就显示错版本），也影响排障判断。
- 修复：`build.sh` 加 `go build -ldflags "-X main.version=$VERSION"`，与 manifest 对齐。

### 5. 大量死代码被 `//go:embed` 打进二进制
- 位置：`templates/static/js/{history,cert,client,user,firewall,settings,setup-wizard}.js` 均未被任何 HTML 引用（index.html 只加载 index.js；client.html/login.html 只用 utils.js）。
- 影响：这些 DataTables 版页面 JS + 整批未引用的库（`lib/dataTables*.js`、`lib/buttons.*`、`lib/colReorder.*`、`lib/responsive.*`、`lib/datepicker*`、`lib/ColReorderWithResize.js`、`lib/dayjs.min.js`、`lib/copy-to-clipboard.js`、`css/dataTables*.css`、`css/datepicker*`、`css/fontawesome.min.css`、`webfonts/*`、`*_playstore.svg`/`apple_appstore.svg`）随包嵌入，徒增二进制体积、混淆维护。
- 修复：删除上述死文件（`index.js` 已用原生实现覆盖它们）。保留 jquery / bootstrap.bundle / qrious / js.cookie / gocaptcha（client.html、login.html 在用）。

---

## 🟡 低 / 健壮性与一致性

### 6. `sendCommand` 在读取循环内重复编译正则
- 位置：`main.go:157` `re := regexp.MustCompile(...)` 在 `for` 内
- 影响：每次读取都编译一次正则，纯性能小瑕疵。
- 修复：提升为包级 `var` 或函数外编译一次。

### 7. 用户导出明文密码 + MFA 密钥到 CSV
- 位置：`main.go:1171` 解密密码、`main.go:1182` 写 `MfaSecret`
- 影响：仅管理员可用，但把全部用户明文密码、MFA 密钥落进可下载 CSV，存在泄露面。
- 建议：导出时密码列留空或打码；MFA 密钥不导出。

### 8. 密码强度策略不一致
- 位置：`isValidPassword`（`main.go:430`，12 位+4 类）仅用于 `/client/modifyPass`；管理员 `/settings` 改密码无强度校验；CSV 用户导入（`main.go:1240`）原样入库；向导提示「至少 6 位」。
- 影响：策略割裂，弱密码可能入库。
- 建议：统一一套强度规则，至少对管理员密码也做校验。

### 9. `/settings` 是宽开放置器
- 位置：`main.go:802` 遍历 POST form 直接 `viper.Set(k, v)`
- 影响：管理员可写任意配置键（含 `system.base.init_done` 等）。当前仅管理员，风险低，但属于隐式大权限。
- 建议：白名单限定可改键。

### 10. 空壳客户端删除偶发 500（已知 `user-0730` 遗留）
- 位置：`main.go:1715` `easyrsa revoke` + `isCertNotFound`（`main.go:227`）
- 影响：PKI 索引里无该证书的客户端，revoke 报错文案若不匹配 `isCertNotFound` 关键词 → 返回 500「删除客户端失败」。
- 建议：revoke 失败时直接按「证书不存在」处理（能跳过就跳过），不再依赖错误文案匹配；并清理 `user-0730` 残留（需进 NAS 手工 `file rm clients/user-0730.ovpn`）。

### 11. supervisor「诊断版」marker 文件堆积
- 位置：`fnos/app/bin/openvpn-supervisor.sh` 大量 `mark "..."` 写入数据卷
- 影响：功能正常，但数据卷里累积 `*.marker` 文件，略脏。
- 建议：稳定后精简 marker（保留关键几个）。

### 12. 仓库残留 `build_err.txt`
- 位置：`openvpn-web-src/build_err.txt`
- 影响：源码目录里的临时构建错误日志，应清理/加 gitignore。

---

## 小结与建议
- **必改（影响正确/安全）**：#1（crypto/rand）、#2（time_unix 回写）、#3（导出时区）、#4（版本注入）。
- **建议清理**：#5（死代码）、#7（明文导出）、#8（密码策略）、#12（残留文件）。
- **可观察**：#6/#9/#10/#11 视优先级处理。

以上均可打包进一次 **v1.0.19** 统一修复（重编译 + 重打 FPK + 部署）。你定一下要修哪些，我直接开干。
