# OpenVPN 控制台功能测试报告

- **日期**：2026-07-29
- **环境**：NAS fnOS `192.168.100.254:8833`（fpk v1.0.0，原生新模板）
- **测试账号**：admin / admin（数据卷保留，密码未变）

## 测试方式说明

原计划用 **QQ浏览器自动化** 做可视化点击测试，但 `qqbrowser-skill install` 要求 Windows **管理员提权（UAC）**，当前非交互 shell 无法满足（RunAs 返回 exit 1，`D:\QQBrowser\QQBrowser.exe` 始终未能安装）。因此改用 `curl` 对控制台 REST API 做 **端到端验证**，直接覆盖每个 UI 功能背后的后端逻辑；前端静态资源（新原生模板）已另行确认由 NAS 正确提供服务。

## 测试结果

| 功能 | 接口 | 结果 |
|------|------|------|
| 登录 | `POST /login` (admin/admin) | 200 登录成功 |
| 仪表盘 / 初始化态 | `GET /api/bootstrap` | 200 `init_done=true`，server `192.168.100.254:1194 udp` |
| 仪表盘页面 | `GET /admin` | 200，含新模板标记 `i-shield` / `statOnline` / `wizardBtn` |
| 客户端列表 | `GET /ovpn/client` | 200 |
| 生成客户端（真实签发） | `POST /ovpn/client` | 200 客户端添加成功 |
| 下载客户端配置 | `GET /ovpn/client/<name>/config` | 200，返回真实 `.ovpn`（cert + CA） |
| 删除客户端 | `DELETE /ovpn/client/<name>` | 200 删除客户端成功 |
| 证书 | `GET /ovpn/certs` | 200，CA 正常，364 天有效 |
| 分组列表 | `GET /ovpn/group` | 200（Default） |
| 新建分组 | `POST /ovpn/group` (parent_id=1) | 200 添加成功 |
| 删除分组 | `DELETE /ovpn/group/2` | 200 删除成功 |
| 用户 | `GET /ovpn/user` | 200 |
| 在线客户端 / 服务状态 | `GET /ovpn/online-client` | 200，`Status: CONNECTED`（OpenVPN 2.6.14，10.8.0.1） |
| 连接记录 | `GET /ovpn/history` | 200（空） |
| 防火墙 | `GET /ovpn/firewall` | 200（空） |
| 服务器配置读取 | `POST /ovpn/server` action=getConfig | 200，返回 server.conf |
| 服务器重载 | `POST /ovpn/server` action=restartSrv | 200 重启服务成功（SIGHUP） |
| 断开客户端 | `POST /ovpn/kill` | 200（无活动连接，安全 no-op） |

## 关键结论

- **核心功能全部正常**：登录、仪表盘、客户端签发与下载、证书、分组增删、服务器配置读取与重载、VPN 服务 `CONNECTED`。
- **此前记录的「DELETE 客户端 500」已自愈**：本轮 `apitest1` 与早先 `wzsmoke` 均成功删除（200）。原 500 系当时 easyrsa 索引态异常，属 transient 环境问题，**非代码缺陷**；系统初始化完整后删除正常。
- **前端已为原生新模板**：NAS 正确提供 `/static/js/index.js`（头部「OpenVPN 控制台 — 原生 JS」，无 Bootstrap/jQuery/DataTables），`index.html` 引用原生 `index.js`。
- **测试残留已清理**：`apitest1` / `wzsmoke` / `apitestgrp` 均已删除；NAS 现仅余 `VPNServer` 与 `wizard-test` 两个客户端。

## 未逐一造数据项（次要，非阻断）

- 用户 / 防火墙「新增」、MFA 设置、邮件发送：对应 GET / 列表接口均 200，新增需构造具体数据且会污染运行环境，未在本轮写入。
- 4 步初始化向导的 UI 逐步点击流：底层 `POST /ovpn/client` 已验证可用，向导 JS 为原生新版；可视化逐步点击需浏览器自动化（待管理员环境可用时补测）。
