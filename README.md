# OpenVPN 服务器（fnOS）

为飞牛私有云 (fnOS) 一键安装的 **OpenVPN 服务器** 应用（FPK 封装）。OpenVPN 二进制、Web 管理后端 (openvpn-web) 及证书生成脚本 (easyrsa) 全部打包在 FPK 内，安装不联网，以原生进程方式跑在飞牛宿主机上，由应用中心统一管理启动 / 停止 / 状态。

## 功能

- 内置 **Web 管理界面**（默认账号 `admin / admin`，建议首次登录修改密码）：
  - 初始化向导：设置密码 → 服务器地址 / 端口 / 协议 → 创建拨号用户 → 生成并下载客户端配置
  - 客户端证书管理（生成 / 下载 / 吊销）、用户与 MFA 管理
  - 在线客户端实时列表（在线 / 限速 / 禁网 / 断开）、连接历史记录
  - 在线编辑 `server.conf` 并一键重启服务
- 社区版 OpenVPN（非 Access Server），**无并发连接数限制**
- 证书由内置 easyrsa 离线生成（CA / 服务端 / 客户端），客户端配置导出即用
- 接入端口 **UDP / TCP 1194**（设置页可随时切换，防火墙已同时放行）
- Web 管理界面经**统一网关 `/app/openvpn`** 打开（复用 NAS 登录态，不对外暴露独立端口）
- 数据（证书 / 配置 / 数据库）持久化到应用数据卷，重装或升级不丢失

## 适用前提

- **外网访问**：需在外网能访问飞牛的 **UDP/TCP 1194** 端口（路由器端口转发 / DDNS / 公网 IP），否则只能局域网内使用。
- **权限**：应用以 root 启动以创建 TUN 设备并加载 nft 防火墙规则（官方权限规范允许的必要场景）；OpenVPN 数据通道已降权至 `nobody` 用户运行，Web 管理后端仅监听本机回环并经统一网关对外。

## 安装

1. 飞牛 OS → 应用中心 → 「手动安装」→ 选择 `openvpn_1.0.43_x86.fpk`。
2. 安装完成后应用中心显示「运行中」，桌面出现应用图标。
3. 点击图标经统一网关打开 Web 管理界面，跟随初始化向导完成配置。

## 从源码构建

```bash
# 需要 Go 1.21+（编译 openvpn-web）与 GNU tar
bash build.sh
# 产物：openvpn_<version>_x86.fpk
```

## 目录结构

```
fnos/                 FPK 包内容
├── manifest         应用元数据（名称 / 版本 / 入口 / 描述）
├── openvpn.sc       防火墙规则（1194 udp/tcp）
├── ui/config        桌面入口（统一网关 /app/openvpn）
├── cmd/service-setup 安装后服务配置
├── config/          特权与资源配置
└── app/             运行时内容（bin: openvpn / web 后端 / 脚本；lib: 运行库）
openvpn-web-src/     Web 管理后端 Go 源码（//go:embed 内嵌前端）
build.sh             打包脚本
```

## 许可

OpenVPN 为 GPLv2（Community Edition）。本 FPK 由 Panda（www.aykeji.cn）打包发布。
