/* OpenVPN 控制台 — 原生 JS（无 jQuery / Bootstrap / DataTables 依赖）
   以 openvpn-redesign/preview.html 为真源重建 */
(function () {
  'use strict';

  /* ---------- 统一网关 base path ---------- */
  var BASE = (function () {
    var m = (location.pathname || '').match(/^\/app\/[A-Za-z0-9_-]+/);
    return m ? m[0] : '';
  })();

  /* ---------- 工具 ---------- */
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (m) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[m];
    });
  }
  function fmt(d) {
    var p = function (n) { return String(n).padStart(2, '0'); };
    return p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
  }
  function $(id) { return document.getElementById(id); }

  /* ---------- 实时速率（前后端差值计算） ----------
     openvpn 只给累计字节(RecvBytes/SendBytes)，速率必须两次采样求差。
     prevSamples 按客户端稳定 key 缓存上一次的累计字节与采样时间。 */
  var prevSamples = {};
  function computeRate(key, c) {
    var now = Date.now();
    var prev = prevSamples[key];
    var rx = 0, tx = 0;
    if (prev && prev.t) {
      var dt = (now - prev.t) / 1000;
      if (dt > 0) {
        rx = Math.max(0, (Number(c.recvBytes) - prev.recv) / dt); // bytes/sec
        tx = Math.max(0, (Number(c.sendBytes) - prev.send) / dt);
      }
    }
    prevSamples[key] = { recv: Number(c.recvBytes) || 0, send: Number(c.sendBytes) || 0, t: now };
    return rx + tx; // 总 bytes/sec
  }
  function fmtRate(bps) {
    var mbps = (bps || 0) * 8 / 1e6; // bytes/sec -> Mbps
    if (mbps >= 1) return mbps.toFixed(2) + ' Mbps';
    return ((bps || 0) * 8 / 1e3).toFixed(0) + ' Kbps';
  }
  // 字节数格式化（用于累计下载/上传流量）；0 或缺失显示「—」
  function fmtBytes(n) {
    n = Number(n) || 0;
    if (n <= 0) return '—';
    var u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    var i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), u.length - 1);
    return (n / Math.pow(1024, i)).toFixed(i ? 1 : 0) + ' ' + u[i];
  }

  /* ---------- 请求封装（form 编码，兼容后端；出错 Toast） ---------- */
  var request = {
    go: function (method, url, data, opts) {
      opts = opts || {};
      var o = {
        method: method,
        headers: { 'Content-Type': 'application/x-www-form-urlencoded; charset=UTF-8' },
        redirect: 'manual'
      };
      if (data) o.body = new URLSearchParams(data);
      return fetch(BASE + url, o).then(function (resp) {
        // session 过期（401）：统一跳转登录页；否则页面停留在空数据状态，操作全部报错
        if (resp.status === 401) {
          if (location.pathname.indexOf('/login') === -1) location.href = BASE + '/login';
          throw new Error('登录超时，请重新登录');
        }
        if (resp.type === 'opaqueredirect') { location.href = resp.url; return; }
        return resp.json().catch(function () { return {}; }).then(function (body) {
          if (!resp.ok) {
            var msg = (body && body.message) || ('请求失败 ' + resp.status);
            if (!opts.silent) toast(msg);
            throw new Error(msg);
          }
          return body;
        });
      });
    },
    get: function (u, o) { return this.go('GET', u, null, o); },
    post: function (u, d, o) { return this.go('POST', u, d, o); },
    put: function (u, d, o) { return this.go('PUT', u, d, o); },
    patch: function (u, d, o) { return this.go('PATCH', u, d, o); },
    delete: function (u, o) { return this.go('DELETE', u, o); }
  };

  /* ---------- Toast ---------- */
  var toastTimer;
  function toast(msg) {
    var t = $('toast');
    if (!t) return;
    t.textContent = msg;
    t.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { t.classList.remove('show'); }, 1800);
  }

  /* ---------- 主题 ---------- */
  var html = document.documentElement;
  var themeBtn = $('themeBtn'), themeIcon = $('themeIcon');
  function setTheme(t) {
    html.setAttribute('data-theme', t);
    var ic = t === 'dark' ? '#i-sun' : '#i-moon';
    if (themeIcon) themeIcon.querySelector('use').setAttribute('href', ic);
    if (themeBtn) themeBtn.setAttribute('aria-pressed', t === 'dark');
    try { localStorage.setItem('ovpn-theme', t); } catch (e) {}
  }
  (function initTheme() {
    var t = 'light';
    try { t = localStorage.getItem('ovpn-theme') || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'); } catch (e) {}
    setTheme(t);
  })();
  if (themeBtn) themeBtn.addEventListener('click', function () {
    setTheme(html.getAttribute('data-theme') === 'dark' ? 'light' : 'dark');
  });

  /* ---------- 退出登录 ---------- */
  var logoutBtn = $('logoutBtn');
  if (logoutBtn) logoutBtn.addEventListener('click', function () {
    window.location.href = BASE + '/logout';
  });

  /* ---------- 侧边栏 / 抽屉 ---------- */
  var app = $('app');
  var backdrop = $('drawerBackdrop');
  function openDrawer() { app.classList.remove('collapsed'); backdrop.hidden = false; }
  function closeDrawer() { if (window.innerWidth <= 920) { app.classList.add('collapsed'); backdrop.hidden = true; } }
  $('menuBtn').addEventListener('click', function () {
    if (window.innerWidth <= 920) { backdrop.hidden ? openDrawer() : closeDrawer(); }
    else { app.classList.toggle('collapsed'); }
  });
  backdrop.addEventListener('click', closeDrawer);
  if (window.innerWidth <= 920) app.classList.add('collapsed');

  /* ---------- 导航切换 ---------- */
  var titleMap = { dashboard: '仪表盘', clients: '客户端', users: '用户', groups: '分组', certs: '证书', history: '连接记录', settings: '设置' };
  function showPanel(s) {
    document.querySelectorAll('.nav-item').forEach(function (n) { n.classList.toggle('active', n.dataset.screen === s); });
    document.querySelectorAll('[data-panel]').forEach(function (p) { p.hidden = p.dataset.panel !== s; });
    $('pageTitle').textContent = titleMap[s] || s;
    $('pageCrumb').textContent = 'OpenVPN 服务器 · ' + (titleMap[s] || s);
    if (s === 'dashboard') loadDashboard();
    else if (s === 'clients') loadClients();
    else if (s === 'users') loadUsers();
    else if (s === 'groups') loadGroups();
    else if (s === 'certs') loadCerts();
    else if (s === 'history') loadHistory();
    else if (s === 'settings') loadSettings();
    if (window.innerWidth <= 920) closeDrawer();
  }
  document.querySelectorAll('.nav-item').forEach(function (a) {
    a.addEventListener('click', function (e) { e.preventDefault(); showPanel(a.dataset.screen); });
  });

  /* ---------- 仪表盘 ---------- */
  function setStatus(text) {
    var badge = $('statStatusBadge'), label = $('statStatus');
    var on = /(run|up|\bconnected\b|active|在线|正常)/i.test(text || '');
    badge.className = 'badge ' + (on ? 'on' : 'off');
    label.textContent = text || (on ? '运行中' : '已停止');
  }
  var onlineAll = [], onlinePage = 1, ONLINE_PER_PAGE = 3;
  function renderOnline(clients) {
    onlineAll = clients || [];
    onlinePage = 1;
    renderOnlinePage();
  }
  function renderOnlinePage() {
    var tb = $('onlineTbody');
    var em = $('onlineEmpty');
    if (!onlineAll.length) { tb.innerHTML = ''; if (em) em.hidden = false; renderLocalPager('onlinePg', 1, ONLINE_PER_PAGE, 0, function () {}); return; }
    if (em) em.hidden = true;
    var start = (onlinePage - 1) * ONLINE_PER_PAGE;
    var slice = onlineAll.slice(start, start + ONLINE_PER_PAGE);
    tb.innerHTML = slice.map(function (c) {
      var name = c.commonName || c.username || '—';
      var vip = c.vip || '—';
      var vip6 = c.vip6 || '';
      var vipCell = vip6 ? '<div class="mono">' + esc(vip) + '</div><div class="mono sub">' + esc(vip6) + '</div>' : '<div class="mono">' + esc(vip) + '</div>';
      var rateStr = fmtRate(c.rateBps);
      var banned = !!c.isNftBlacklist;
      var banBtn = banned
        ? '<button class="icon-btn btn-warn" data-act="ban" data-banned="1" data-vip="' + esc(vip) + '" data-vip6="' + esc(vip6) + '" aria-label="解除拉黑"><svg class="icon icon-sm"><use href="#i-unlock"/></svg></button>'
        : '<button class="icon-btn btn-danger" data-act="ban" data-banned="0" data-vip="' + esc(vip) + '" data-vip6="' + esc(vip6) + '" aria-label="禁网"><svg class="icon icon-sm"><use href="#i-lock"/></svg></button>';
      return '<tr>' +
        '<td>' + esc(name) + '</td>' +
        '<td>' + vipCell + '</td>' +
        '<td>' + rateStr + '</td>' +
        '<td>' + esc(c.onlineTime || '—') + '</td>' +
        '<td>' + fmtBytes(c.recvBytes) + '</td>' +
        '<td>' + fmtBytes(c.sendBytes) + '</td>' +
        '<td><div class="row-actions" style="justify-content:flex-end">' +
          '<button class="icon-btn" data-act="kill" data-cid="' + esc(c.id || name) + '" aria-label="断开"><svg class="icon icon-sm"><use href="#i-close"/></svg></button>' +
          '<button class="icon-btn" data-act="limit" data-vip="' + esc(vip) + '" data-vip6="' + esc(vip6) + '" aria-label="限速"><svg class="icon icon-sm"><use href="#i-bolt"/></svg></button>' +
          banBtn +
        '</div></td></tr>';
    }).join('');
    onlinePage = renderLocalPager('onlinePg', onlinePage, ONLINE_PER_PAGE, onlineAll.length, function (p) { onlinePage = p; renderOnlinePage(); });
  }
  function loadDashboard(silent) {
    request.get('/ovpn/online-client', { silent: silent }).then(function (res) {
      var clients = (res && res.clients) || [];
      var server = (res && res.server) || {};
      var agg = 0;
      clients.forEach(function (c) {
        var key = c.commonName || c.username || c.id;
        c.rateBps = computeRate(key, c);
        agg += c.rateBps;
      });
      renderOnline(clients);
      $('statOnline').textContent = clients.length;
      // 分母：已配置客户端总数（后端 total；缺失时回退到在线数，避免显示 0）
      $('statTotal').textContent = (res && typeof res.total === 'number') ? res.total : clients.length;
      // 今日连接：后端按今自然日 0 点起统计 history 记录数
      $('statToday').textContent = (res && typeof res.today === 'number') ? res.today : 0;
      $('statRate').textContent = (agg * 8 / 1e6).toFixed(2); // Mbps
      // 下载/上传流量：来自 OpenVPN load-stats 的 bytesout/bytesin（自服务启动累计）
      $('statDownload').textContent = server.BytesOut || '0';
      $('statUpload').textContent = server.BytesIn || '0';
      setStatus(server.Status);
      if (server.Address) $('siAddress').textContent = server.Address;
      // 版本：仅显示 OpenVPN 语义版本 + FPK 版本，去掉编译平台/特性长串
      var ovVer = server.Version || '—';
      var fpkVer = server.FpkVersion ? 'v' + server.FpkVersion : '—';
      $('siVersion').textContent = 'OpenVPN ' + ovVer + ' · FPK ' + fpkVer;
      // 运行时长：由服务启动时间戳实时计算（仪表盘每 5s 轮询自动刷新）
      if (typeof server.RunUnix === 'number' && server.RunUnix > 0) {
        window.__srvStart = server.RunUnix;
        var up = $('statUptime');
        if (up) up.textContent = fmtUptime(Math.floor(Date.now() / 1000) - server.RunUnix);
      }
      refreshUpdateBadge();
    }).catch(function () {});
  }
  function fmtUptime(sec) {
    sec = Math.max(0, sec | 0);
    var d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
    if (d > 0) return d + ' 天 ' + h + ' 小时';
    if (h > 0) return h + ' 小时 ' + m + ' 分';
    return m + ' 分钟';
  }
  var __updateBadgeChecked = false;
  function refreshUpdateBadge() {
    var badge = $('siUpdateBadge'), txt = $('siUpdateText');
    if (!badge || !txt || __updateBadgeChecked) return;
    __updateBadgeChecked = true; // 只查一次（失败也不重试），避免每 5s 轮询反复请求
    request.get('/ovpn/update', { silent: true }).then(function (d) {
      if (!d || d.ok === false) { badge.hidden = true; return; }
      function pickVer(s) { var m = String(s || '').match(/(\d+\.\d+\.\d+)/); return m ? m[1] : ''; }
      function cmpVer(a, b) {
        var pa = String(a).split('.').map(Number), pb = String(b).split('.').map(Number);
        for (var i = 0; i < 3; i++) { var x = pa[i] || 0, y = pb[i] || 0; if (x !== y) return x - y; }
        return 0;
      }
      var rels = (d.releases || []).filter(function (r) { return r.version; });
      var latest = rels[0] || null;
      var cur = pickVer(d.current), lat = pickVer(latest && latest.version);
      if (!lat) { badge.hidden = true; return; }
      var hasNew = !!(cur && cmpVer(lat, cur) > 0);
      badge.className = hasNew ? 'badge warn' : 'badge on';
      txt.textContent = hasNew ? '发现新版本 v' + lat : '已是最新';
      badge.hidden = false;
    }).catch(function () { badge.hidden = true; });
  }

  /* 在线客户端操作 */
  $('onlineTbody').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-act]'); if (!btn) return;
    var act = btn.dataset.act;
    if (act === 'kill') {
      request.post('/ovpn/kill', { cid: btn.dataset.cid }).then(function () { toast('已断开 ' + btn.dataset.cid); loadDashboard(true); }).catch(function () {});
    } else if (act === 'ban') {
      // 拉黑/解除拉黑 toggle：已拉黑再点一次即解除
      var banned = btn.dataset.banned === '1';
      var url = '/ovpn/firewall?a=' + (banned ? 'remove_blacklist' : 'add_blacklist');
      var label = banned ? '已解除拉黑 ' : '已拉黑 ';
      request.post(url, { vip: btn.dataset.vip, vip6: btn.dataset.vip6 || '' }).then(function () { toast(label + btn.dataset.vip); loadDashboard(true); }).catch(function () {});
    } else if (act === 'limit') {
      uiPrompt({ title: '限速 (Mbps)', placeholder: '0 表示不限速', validate: function (v) {
        if (v === '') return '请输入数值';
        var n = Number(v);
        if (isNaN(n) || n < 0) return '请输入非负数字';
        return '';
      } }).then(function (v) {
        if (v === null) return;
        // 后端 set_rateLimit 协议：upload/download + 单位；同时支持 vip6
        var payload = { vip: btn.dataset.vip, vip6: btn.dataset.vip6 || '', upload: v, download: v, uploadUnit: 'Mbps', downloadUnit: 'Mbps' };
        request.post('/ovpn/firewall?a=set_rateLimit', payload).then(function () { toast('已限速 ' + btn.dataset.vip); }).catch(function () {});
      });
    }
  });

  /* ---------- 设置 ---------- */
  function loadSettings() {
    request.get('/settings', { silent: true }).then(function (d) {
      window.__settings = d || {};
      var ov = d.openvpn || {};
      var base = (d.system && d.system.base) || {};
      if (ov.ovpn_subnet) $('siSubnet').textContent = ov.ovpn_subnet;
      if (ov.ovpn_max_clients != null) $('siMax').textContent = ov.ovpn_max_clients;
      if (ov.ovpn_proto) $('siProto').textContent = String(ov.ovpn_proto).toUpperCase();
      if (base.server_addr) $('siAddress').textContent = base.server_addr;
      // 预填设置表单
      $('setAddr').value = base.server_addr || '';
      $('setPort').value = ov.ovpn_port || '';
      $('setProto').value = ov.ovpn_proto || 'udp';
      $('setSubnet').value = ov.ovpn_subnet || '';
      $('setIpv6').checked = !!ov.ovpn_ipv6;
      $('setIpv6Listen').checked = !!ov.ovpn_ipv6_listen;
      $('setSubnet6').value = ov.ovpn_subnet6 || '';
      $('setGateway').checked = !!ov.ovpn_gateway;
      // v1.0.84：网关模式 DNS 回填（缺省回退默认 223.5.5.5 / 114.114.114.114）
      $('setDns1').value = ov.ovpn_push_dns1 || '223.5.5.5';
      $('setDns2').value = ov.ovpn_push_dns2 || '114.114.114.114';
    }).catch(function () {});
  }
  // v1.0.70：服务器地址 / 端口合法性校验（与后端 POST /settings 保持一致）
  // 说明：前端只做“便宜且高命中”的拦截（空格/逗号/非法字符/IPv4 八位/域名），
  // 含冒号的 IPv6 直接放行，交由后端 net.ParseIP 权威校验，避免前端正则误拦合法地址。
  function isValidServerAddr(s) {
    s = (s || '').trim();
    if (!s) return false;
    if (/\s/.test(s)) return false;            // 拒绝任何空白
    if (/:/.test(s)) return true;              // IPv6：放行，后端校验
    if (/^\d{1,3}(\.\d{1,3}){3}$/.test(s)) {   // IPv4：逐段 0–255
      return s.split('.').every(function (p) { var n = parseInt(p, 10); return n >= 0 && n <= 255; });
    }
    // 域名/FQDN：RFC 1123 标签 + 点分隔，仅允许 [a-zA-Z0-9-.]（逗号/空格/中文等自然被拒）
    return /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$/.test(s);
  }
  function isValidPort(s) {
    s = (s || '').trim();
    if (!/^\d+$/.test(s)) return false;
    var n = parseInt(s, 10);
    return n >= 1 && n <= 65535;
  }
  // v1.0.79：子网合法性校验——OpenVPN --server 只接受「网段地址」（主机位全 0）。
  // 用户若填网关 IP（如 192.168.31.1/24），ParseCIDR 格式合法但 OpenVPN 启动即报
  // Options error: --server directive network/netmask combination is invalid。
  // 因此必须在保存时就拦截：CIDR 合法 + 主机位必须全 0（IPv4 / IPv6 均校验）。
  function isValidSubnetCIDR(s) {
    s = (s || '').trim();
    var m = s.match(/^([^\/]+)\/(\d{1,3})$/);
    if (!m) return false;
    var ip = m[1], prefix = parseInt(m[2], 10);
    if (ip.indexOf(':') >= 0) {
      if (prefix < 1 || prefix > 128) return false;
      var groups = expandV6(ip);
      if (!groups) return false;
      var hex = '';
      for (var i = 0; i < 8; i++) hex += parseInt(groups[i], 16).toString(16).padStart(4, '0');
      var addr = BigInt('0x' + hex);
      var mask = (BigInt(1) << BigInt(128 - prefix)) - BigInt(1);
      return (addr & mask) === BigInt(0);
    }
    var oct = ip.split('.').map(Number);
    if (oct.length !== 4 || oct.some(function (n) { return isNaN(n) || n < 0 || n > 255; })) return false;
    if (prefix < 1 || prefix > 32) return false;
    var full = ((oct[0] << 24) | (oct[1] << 16) | (oct[2] << 8) | oct[3]) >>> 0;
    var mask32 = (prefix === 0) ? 0 : (~((1 << (32 - prefix)) - 1)) >>> 0;
    // 注意：JS 位运算结果是有符号 32 位，必须再 >>>0 归一为无符号再比较，
    // 否则 192.x/172.x 等首字节>=128 的网段会被误判为非法。
    return ((full & mask32) >>> 0) === full;
  }
  function expandV6(ip) {
    // 展开 IPv6 为 8 组，返回数组或 null（不合法）
    var parts;
    if (ip.indexOf('::') >= 0) {
      var sp = ip.split('::');
      if (sp.length > 2) return null;
      var left = sp[0] ? sp[0].split(':') : [];
      var right = sp[1] ? sp[1].split(':') : [];
      var fill = 8 - left.length - right.length;
      if (fill < 1) return null;
      parts = left.concat(new Array(fill).fill('0'), right);
    } else {
      parts = ip.split(':');
      if (parts.length !== 8) return null;
    }
    for (var i = 0; i < parts.length; i++) {
      if (!/^[0-9a-fA-F]{1,4}$/.test(parts[i])) return null;
    }
    return parts;
  }
  // v1.0.80：通用 IP 校验（IPv4 / IPv6），用于分组 DNS 等参数
  function isValidIP(s) {
    s = (s || '').trim();
    if (!s) return false;
    if (s.indexOf(':') >= 0) return expandV6(s) !== null;
    var oct = s.split('.').map(Number);
    return oct.length === 4 && oct.every(function (n) { return !isNaN(n) && n >= 0 && n <= 255; });
  }
  // v1.0.80：分组「访问内网网段」参数校验——支持「网段 掩码」或「网段/前缀」两种写法
  function isValidRouteParam(s) {
    s = (s || '').trim();
    var m = s.match(/^(\d{1,3}(?:\.\d{1,3}){3})(?:\s+(\d{1,3}(?:\.\d{1,3}){3})|(\/\d{1,2}))?$/);
    if (!m) return false;
    var oct = m[1].split('.').map(Number);
    if (oct.some(function (n) { return n < 0 || n > 255; })) return false;
    if (m[2]) {
      var moct = m[2].split('.').map(Number);
      if (moct.some(function (n) { return n < 0 || n > 255; })) return false;
    }
    if (m[3]) {
      var pre = parseInt(m[3].slice(1), 10);
      if (pre < 1 || pre > 32) return false;
    }
    return true;
  }

  $('saveSettings').addEventListener('click', function () {
    var addr = $('setAddr').value.trim();
    var port = $('setPort').value.trim();
    var proto = $('setProto').value;
    // v1.0.70：服务器地址 / 端口实时校验，避免手输错（如 nas.aykeji,cn 逗号笔误）
    if (addr && !isValidServerAddr(addr)) { toast('服务器地址格式不正确（仅支持 IPv4 / IPv6 / 域名，不能含空格、逗号等）'); return; }
    if (port && !isValidPort(port)) { toast('端口必须是 1–65535 之间的整数'); return; }
    var subnet = $('setSubnet').value.trim();
    var ipv6 = $('setIpv6').checked;
    var ipv6Listen = $('setIpv6Listen').checked;
    var subnet6 = $('setSubnet6').value.trim();
    var gateway = $('setGateway').checked;
    var dns1 = $('setDns1').value.trim();
    var dns2 = $('setDns2').value.trim();
    // v1.0.79：子网必须在保存时就校验（网段地址、主机位全 0），不让坏配置落到 server.conf
    if (subnet && !isValidSubnetCIDR(subnet)) { toast('子网格式不正确：必须是网段地址（主机位全 0），如 10.8.0.0/24 或 192.168.31.0/24；不要填网关 IP（如 192.168.31.1/24）'); return; }
    if (subnet6 && !isValidSubnetCIDR(subnet6)) { toast('IPv6 子网格式不正确：必须是网段地址（主机位全 0），如 fdaf:f178:e916:6dd0::/64'); return; }
    // v1.0.84：DNS 格式校验（留空则不覆盖默认值；非空必须合法 IPv4/IPv6）
    if (dns1 && !isValidIP(dns1)) { toast('主 DNS 格式不正确（应为 IPv4 或 IPv6 地址）'); return; }
    if (dns2 && !isValidIP(dns2)) { toast('备 DNS 格式不正确（应为 IPv4 或 IPv6 地址）'); return; }
    var p1 = $('setPass').value, p2 = $('setPass2').value;
    var payload = {};
    if (addr) payload['system.base.server_addr'] = addr;
    if (port) payload['openvpn.ovpn_port'] = port;
    if (proto) payload['openvpn.ovpn_proto'] = proto;
    if (subnet) payload['openvpn.ovpn_subnet'] = subnet;
    payload['openvpn.ovpn_ipv6'] = ipv6;
    payload['openvpn.ovpn_ipv6_listen'] = ipv6Listen;
    if (subnet6) payload['openvpn.ovpn_subnet6'] = subnet6;
    payload['openvpn.ovpn_gateway'] = gateway;
    // v1.0.84：网关模式 DNS（留空则不发送，后端保留默认 223.5.5.5 / 114.114.114.114）
    if (dns1) payload['openvpn.ovpn_push_dns1'] = dns1;
    if (dns2) payload['openvpn.ovpn_push_dns2'] = dns2;
    if (p1) {
      // 密码策略（v1.0.40）：管理员密码至少 8 位
      if (p1.length < 8) { toast('管理员密码至少 8 位'); return; }
      if (p1 !== p2) { toast('两次密码不一致'); return; }
      payload['system.base.admin_password'] = p1;
    }
    var keys = Object.keys(payload);
    if (!keys.length) { toast('没有需要保存的修改'); return; }
    (function next(i) {
      if (i >= keys.length) {
        // 端口/协议/子网/IPv6/网关模式修改需 openvpn 重启才重新生成 server.conf
        var needRestart = payload['openvpn.ovpn_port'] || payload['openvpn.ovpn_proto'] || payload['openvpn.ovpn_subnet'] || payload['openvpn.ovpn_ipv6'] !== undefined || payload['openvpn.ovpn_subnet6'] || payload['openvpn.ovpn_gateway'] !== undefined;
        toast(needRestart ? '设置已保存（端口/协议/子网修改需点击「重启服务」生效）' : '设置已保存');
        $('setPass').value = ''; $('setPass2').value = '';
        loadSettings();
        return;
      }
      var k = keys[i];
      request.post('/settings', (function () { var o = {}; o[k] = payload[k]; return o; })())
        .then(function () { next(i + 1); })
        .catch(function (e) { toast((e && e.message) || ('保存失败：' + k)); });
    })(0);
  });
  $('cancelSettings').addEventListener('click', function () { loadSettings(); });

  $('restartSrvBtn').addEventListener('click', function () {
    request.post('/ovpn/server', { action: 'restartSrv' }).then(function () { toast('已发送重启指令'); }).catch(function () {});
  });

  /* ---------- 关于（更新检测 / Bug 反馈）---------- */
  function esc2(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (m) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[m];
    });
  }
  function checkUpdate(silent) {
    var btn = $('checkUpdateBtn'), st = $('updateStatus');
    if (btn) btn.disabled = true;
    if (st) st.textContent = '检查中…';
    request.get('/ovpn/update', { silent: !!silent }).then(function (d) {
      if (!d || d.ok === false) { if (st) st.textContent = (d && d.error) || '检查失败'; return; }
      // 版本号从 version 提取；数据由 panda 后台「推送更新」页人工录入
      function pickVer(s) { var m = String(s || '').match(/(\d+\.\d+\.\d+)/); return m ? m[1] : ''; }
      function cmpVer(a, b) {
        var pa = String(a).split('.').map(Number), pb = String(b).split('.').map(Number);
        for (var i = 0; i < 3; i++) { var x = pa[i] || 0, y = pb[i] || 0; if (x !== y) return x - y; }
        return 0;
      }
      var rels = (d.releases || []).filter(function (r) { return r.version; });
      var latest = rels[0] || null;
      var cur = pickVer(d.current), lat = pickVer(latest && latest.version);
      var hasNew = !!(cur && lat && cmpVer(lat, cur) > 0);
      if (st) st.textContent = lat ? (hasNew ? '发现新版本 v' + lat + '！' : '已是最新版本') : '暂无版本信息';
      var res = $('updateResult');
      if (res) {
        res.hidden = false;
        if (!latest) { res.innerHTML = '<div class="hint">暂无发布版本。</div>'; }
        else if (hasNew) {
          var dl = latest.download_url ? ' <a href="' + esc2(latest.download_url) + '" target="_blank" rel="noopener" style="color:var(--accent,#4f7cff);font-weight:600">下载</a>' : '';
          res.innerHTML = '<div class="alert" style="padding:10px 12px"><b>发现新版本 v' + esc2(lat) + '</b>（' + esc2((latest.pub_date || '').slice(0, 10)) + '）—— 请到飞牛「应用中心」手动升级安装。' + dl + '</div>';
        } else {
          res.innerHTML = '<div class="hint">当前已是最新版本 v' + esc2(cur) + '（' + esc2((latest.pub_date || '').slice(0, 10)) + ' 发布）。</div>';
        }
      }
      var chg = $('changelogList');
      if (chg) {
        chg.innerHTML = rels.length ? rels.map(function (r) {
          var content = esc2(r.content || (r.title ? '（' + r.title + '）' : '（无说明）')).replace(/\n/g, '<br>');
          var ttl = r.title ? '<div style="margin-top:2px;font-weight:600">' + esc2(r.title) + '</div>' : '';
          return '<div style="padding:8px 0;border-bottom:1px solid var(--border,#e5e7eb)"><b>v' + esc2(r.version) + '</b> <span class="hint">' + esc2((r.pub_date || '').slice(0, 10)) + '</span>' + ttl + '<div style="margin-top:4px;font-size:13px;line-height:1.6">' + content + '</div></div>';
        }).join('') : '暂无版本记录（由 PanDa 后台「推送更新」页录入）。';
      }
    }).catch(function (e) {
      if (st) st.textContent = '检查失败：' + ((e && e.message) || '未知错误');
    }).then(function () { if (btn) btn.disabled = false; });
  }
  function submitFeedback() {
    var t = $('fbTitle'), d = $('fbDesc'), c = $('fbContact'), cat = $('fbCategory'), lg = $('fbLogs'), err = $('fbErr');
    var title = (t && t.value || '').trim();
    if (!title) { if (err) err.textContent = '请填写反馈标题'; return; }
    if (err) err.textContent = '';
    var btn = $('fbSubmit'); if (btn) btn.disabled = true;
    request.post('/ovpn/feedback', {
      category: cat ? cat.value : '', title: title,
      description: d ? d.value : '', contact: c ? c.value : '',
      include_logs: lg && lg.checked ? 'true' : 'false'
    }).then(function (r) {
      toast((r && r.message) || '反馈已提交，感谢！');
      if (t) t.value = ''; if (d) d.value = ''; if (c) c.value = '';
    }).catch(function (e) {
      if (err) err.textContent = (e && e.message) || '提交失败，请重试';
    }).then(function () { if (btn) btn.disabled = false; });
  }
  var checkBtn = $('checkUpdateBtn');
  if (checkBtn) checkBtn.addEventListener('click', function () { checkUpdate(false); });
  var fbBtn = $('fbSubmit');
  if (fbBtn) fbBtn.addEventListener('click', submitFeedback);
  // 进入关于页自动检查一次（静默）
  document.querySelectorAll('.nav-item').forEach(function (a) {
    a.addEventListener('click', function () {
      if (a.dataset.screen === 'about') checkUpdate(true);
    });
  });

  /* ---------- 客户端 ---------- */
  function downloadClient(name) {
    request.get('/ovpn/client/' + encodeURIComponent(name) + '/config').then(function (cfg) {
      var text = (cfg && cfg.content) || '';
      var blob = new Blob([text], { type: 'text/plain' });
      var url = URL.createObjectURL(blob);
      var a = document.createElement('a'); a.href = url; a.download = name + '.ovpn'; a.click();
      URL.revokeObjectURL(url); toast('开始下载 ' + name + '.ovpn');
    }).catch(function () {});
  }
  var clientAll = [], clientPage = 1, CLIENT_PER_PAGE = 10;
  function loadClients() {
    request.get('/ovpn/client?_=' + Date.now(), { silent: true }).then(function (d) {
      clientAll = (Array.isArray(d) ? d : (d && d.data)) || [];
      clientPage = 1;
      renderClientsPage();
    }).catch(function () {});
  }
  function renderClientsPage() {
    var tb = $('clientsTbody');
    if (!clientAll.length) { tb.innerHTML = '<tr><td colspan="7" class="empty">暂无客户端</td></tr>'; renderLocalPager('clientPg', 1, CLIENT_PER_PAGE, 0, function () {}); return; }
    var start = (clientPage - 1) * CLIENT_PER_PAGE;
    var slice = clientAll.slice(start, start + CLIENT_PER_PAGE);
    tb.innerHTML = slice.map(function (c) {
      return '<tr>' +
        '<td>' + esc(c.name || '—') + '</td>' +
        '<td>' + (c.vip6 ? '<div class="mono">' + esc(c.vip || '—') + '</div><div class="mono sub">' + esc(c.vip6) + '</div>' : '<div class="mono">' + esc(c.vip || '—') + '</div>') + '</td>' +
        '<td>' + (c.online ? '<span class="badge on"><span class="dot"></span>在线</span>' : '<span class="badge off"><span class="dot"></span>离线</span>') + '</td>' +
        '<td>' + esc(c.date || '—') + '</td>' +
        '<td>' + fmtBytes(c.recvBytes) + '</td>' +
        '<td>' + fmtBytes(c.sendBytes) + '</td>' +
        '<td><div class="row-actions" style="justify-content:flex-end">' +
          '<button class="icon-btn" data-act="download" data-name="' + esc(c.name) + '" aria-label="下载"><svg class="icon icon-sm"><use href="#i-download"/></svg></button>' +
          '<button class="icon-btn" data-act="edit" data-name="' + esc(c.name) + '" aria-label="编辑"><svg class="icon icon-sm"><use href="#i-edit"/></svg></button>' +
          '<button class="icon-btn btn-danger" data-act="revoke" data-name="' + esc(c.name) + '" aria-label="吊销"><svg class="icon icon-sm"><use href="#i-trash"/></svg></button>' +
        '</div></td></tr>';
    }).join('');
    clientPage = renderLocalPager('clientPg', clientPage, CLIENT_PER_PAGE, clientAll.length, function (p) { clientPage = p; renderClientsPage(); });
  }
  $('clientsTbody').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-act]'); if (!btn) return;
    var name = btn.dataset.name, act = btn.dataset.act;
    if (act === 'download') downloadClient(name);
    else if (act === 'revoke') {
      if (!confirm('确认吊销客户端 ' + name + '？')) return;
      request.delete('/ovpn/client/' + encodeURIComponent(name)).then(function () { toast('已吊销 ' + name); loadClients(); }).catch(function () {});
    } else if (act === 'edit') openClientEdit(name);
  });
  $('addClientBtn').addEventListener('click', function () {
    uiPrompt({ title: '客户端名称', placeholder: '例如 laptop / phone', validate: function (v) {
      if (!v) return '请输入客户端名称';
      if (!/^[a-zA-Z0-9_-]{1,64}$/.test(v)) return '仅支持字母、数字、_ -，长度 1–64';
      return '';
    } }).then(function (name) {
      if (!name) return;
      request.post('/ovpn/client', { name: name, serverAddr: '', serverPort: '', config: '', ccdConfig: '', mfa: '' })
        .then(function () { toast('已添加 ' + name); loadClients(); }).catch(function () {});
    });
  });
  $('clientSearch').addEventListener('input', function () {
    var q = this.value.trim().toLowerCase();
    document.querySelectorAll('#clientsTbody tr').forEach(function (tr) {
      var name = (tr.children[0] && tr.children[0].textContent || '').toLowerCase();
      tr.style.display = (!q || name.indexOf(q) >= 0) ? '' : 'none';
    });
  });

  /* ---------- 通用非阻塞 prompt 弹窗（替代原生 prompt，避免页面阻塞） ---------- */
  function uiPrompt(opts) {
    opts = opts || {};
    return new Promise(function (resolve) {
      $('promptTitle').textContent = opts.title || '提示';
      var inp = $('promptInput');
      inp.value = opts.value || '';
      inp.placeholder = opts.placeholder || '';
      $('promptErr').textContent = '';
      $('promptModal').hidden = false;
      inp.focus();
      function cleanup() { $('promptModal').hidden = true; }
      $('promptOk').onclick = function () {
        var v = inp.value.trim();
        if (opts.validate) { var m = opts.validate(v); if (m) { $('promptErr').textContent = m; return; } }
        cleanup(); resolve(v);
      };
      function cancel() { cleanup(); resolve(null); }
      $('promptCancel').onclick = cancel;
      $('promptClose').onclick = cancel;
      inp.onkeydown = function (e) { if (e.key === 'Enter') $('promptOk').click(); else if (e.key === 'Escape') cancel(); };
    });
  }

  /* ---------- 客户端编辑 ---------- */
  function openClientEdit(name) {
    $('ceName').value = name;
    $('ceNameShow').value = name;
    $('ceIp').value = '';
    $('ceIp6').value = '';
    $('ceErr').textContent = '';
    $('clientEditModal').hidden = false;
    request.get('/ovpn/client/' + encodeURIComponent(name) + '/ccd', { silent: true }).then(function (d) {
      var content = (d && d.content) || '';
      var m4 = /ifconfig-push\s+(\S+)/i.exec(content);
      if (m4) $('ceIp').value = m4[1];
      // IPv6：ifconfig-ipv6-push 接受 IP/CIDR（OpenVPN 2.4+）
      var m6 = /ifconfig-ipv6-push\s+(\S+)/i.exec(content);
      if (m6) $('ceIp6').value = m6[1];
    }).catch(function () {});
  }
  function closeClientEdit() { $('clientEditModal').hidden = true; }
  $('clientEditClose').addEventListener('click', closeClientEdit);
  $('clientEditCancel').addEventListener('click', closeClientEdit);
  $('clientEditDownload').addEventListener('click', function () {
    var name = $('ceName').value; if (name) downloadClient(name);
  });
  $('clientEditSave').addEventListener('click', function () {
    var name = $('ceName').value;
    var ip = $('ceIp').value.trim();
    var ip6 = $('ceIp6').value.trim();
    var lines = [];
    if (ip) {
      if (!/^\d{1,3}(\.\d{1,3}){3}$/.test(ip)) { $('ceErr').textContent = 'IPv4 格式不正确'; return; }
      lines.push('ifconfig-push ' + ip + ' 255.255.255.0');
    }
    if (ip6) {
      // IPv6 基本格式校验（允许 /CIDR 后缀），避免写入垃圾导致 openvpn 重启失败
      if (!/^[0-9a-fA-F:]+(\/\d+)?$/.test(ip6)) { $('ceErr').textContent = 'IPv6 格式不正确'; return; }
      lines.push('ifconfig-ipv6-push ' + ip6);
    }
    var content = lines.join('\n') + (lines.length ? '\n' : '');
    request.put('/ovpn/client/' + encodeURIComponent(name) + '/ccd', { content: content })
      .then(function (d) { toast((d && d.message) || '已保存'); closeClientEdit(); loadClients(); })
      .catch(function (e) { $('ceErr').textContent = (e && e.message) || '保存失败'; });
  });

  /* ---------- 用户 ---------- */
  var usersById = {};
  function loadUsers() {
    request.get('/ovpn/group', { silent: true }).then(function (groups) {
      var all = [];
      usersById = {};
      var pending = (groups || []).map(function (g) {
        return request.get('/ovpn/group/' + g.id + '/users', { silent: true }).then(function (r) {
          // GetUsers 用递归 CTE，查任意分组都会连带返回其所有子孙分组成员，
          // 因此逐个分组聚合时同一用户可能被多次返回（上级组 + 本级组），
          // 必须按 id 去重，否则用户列表中会出现重复行。
          (r.users || []).forEach(function (u) {
            if (usersById[u.id]) return;
            usersById[u.id] = u;
            all.push(u);
          });
        }).catch(function () {});
      });
      Promise.all(pending).then(function () {
        userAll = all;
        userPage = 1;
        renderUsersPage();
      });
    }).catch(function () {});
  }
  var userAll = [], userPage = 1, USER_PER_PAGE = 10;
  function renderUsersPage() {
    var tb = $('usersTbody');
    if (!userAll.length) { tb.innerHTML = '<tr><td colspan="6" class="empty">暂无用户</td></tr>'; renderLocalPager('userPg', 1, USER_PER_PAGE, 0, function () {}); return; }
    var start = (userPage - 1) * USER_PER_PAGE;
    var slice = userAll.slice(start, start + USER_PER_PAGE);
    tb.innerHTML = slice.map(function (u) {
      var on = u.isEnable;
      return '<tr>' +
        '<td>' + esc(u.username || '—') + '</td>' +
        '<td>' + esc(u.name || '—') + '</td>' +
        '<td><span class="badge ' + (on ? 'on' : 'off') + '"><span class="dot"></span>' + (on ? '启用' : '禁用') + '</span></td>' +
        '<td>' + fmtBytes(u.recvBytes) + '</td>' +
        '<td>' + fmtBytes(u.sendBytes) + '</td>' +
        '<td><div class="row-actions" style="justify-content:flex-end">' +
          '<button class="icon-btn" data-act="edituser" data-id="' + esc(u.id) + '" aria-label="编辑"><svg class="icon icon-sm"><use href="#i-edit"/></svg></button>' +
          '<button class="icon-btn btn-danger" data-act="deluser" data-id="' + esc(u.id) + '" aria-label="删除"><svg class="icon icon-sm"><use href="#i-trash"/></svg></button>' +
        '</div></td></tr>';
    }).join('');
    userPage = renderLocalPager('userPg', userPage, USER_PER_PAGE, userAll.length, function (p) { userPage = p; renderUsersPage(); });
  }
  $('usersTbody').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-act]'); if (!btn) return;
    var id = btn.dataset.id, act = btn.dataset.act;
    if (act === 'deluser') {
      if (!confirm('确认删除该用户？')) return;
      request.delete('/ovpn/user/' + encodeURIComponent(id)).then(function () { toast('已删除'); loadUsers(); }).catch(function () {});
    } else if (act === 'edituser') {
      openEditUser(usersById[id]);
    }
  });
  function openEditUser(u) {
    if (!u) { toast('用户数据缺失'); return; }
    $('euId').value = u.id || '';
    $('euUsername').value = u.username || '';
    $('euName').value = u.name || '';
    $('euEmail').value = u.email || '';
    $('euIpAddr').value = u.ipAddr || '';
    $('euExpireDate').value = (u.expireDate || '').slice(0, 10);
    $('euErr').textContent = '';
    request.get('/ovpn/group', { silent: true }).then(function (groups) {
      $('euGid').innerHTML = (groups || []).map(function (g) {
        return '<option value="' + esc(g.id) + '"' + (String(g.id) === String(u.gid) ? ' selected' : '') + '>' + esc(g.name) + '</option>';
      }).join('');
    }).catch(function () {});
    request.get('/ovpn/client', { silent: true }).then(function (d) {
      var list = (Array.isArray(d) ? d : (d && d.data)) || [];
      $('euOvpnConfig').innerHTML = list.map(function (c) {
        var v = c.fullName || c.name;
        return '<option value="' + esc(v) + '"' + (v === (u.ovpnConfig || '') ? ' selected' : '') + '>' + esc(c.name || v) + '</option>';
      }).join('');
    }).catch(function () {});
    $('editUserModal').hidden = false;
  }
  function closeEditUser() { $('editUserModal').hidden = true; }
  $('editUserClose').addEventListener('click', closeEditUser);
  $('editUserCancel').addEventListener('click', closeEditUser);
  $('editUserSave').addEventListener('click', function () {
    var payload = {
      id: $('euId').value,
      username: $('euUsername').value,
      name: $('euName').value,
      email: $('euEmail').value,
      ipAddr: $('euIpAddr').value,
      gid: Number($('euGid').value),
      ovpnConfig: $('euOvpnConfig').value,
      expireDate: $('euExpireDate').value
    };
    var pw = $('euPassword').value;
    if (pw) {
      if (pw.length < 6) { $('euErr').textContent = '密码至少 6 位'; return; }
      payload.password = pw;
    }
    request.patch('/ovpn/user', payload).then(function (d) {
      toast(d.message || '已保存'); closeEditUser(); loadUsers();
    }).catch(function (e) { $('euErr').textContent = (e && e.message) || '保存失败'; });
  });
  function closeAddUser() { $('addUserModal').hidden = true; }
  $('addUserBtn').addEventListener('click', function () {
    $('auUsername').value = '';
    $('auPassword').value = '';
    $('auErr').textContent = '';
    $('addUserModal').hidden = false;
    $('auUsername').focus();
  });
  $('addUserClose').onclick = closeAddUser;
  $('addUserCancel').onclick = closeAddUser;
  $('addUserSave').onclick = function () {
    var u = $('auUsername').value.trim();
    var p = $('auPassword').value;
    if (!u) { $('auErr').textContent = '请填写用户名'; return; }
    if (!/^[a-zA-Z0-9_.-]{1,64}$/.test(u)) { $('auErr').textContent = '用户名仅支持字母、数字、_ . -，长度 1–64'; return; }
    if (p.length < 6) { $('auErr').textContent = '密码至少 6 位'; return; }
    request.post('/ovpn/user', { username: u, password: p, name: u, isEnable: true })
      .then(function () { toast('已添加用户 ' + u); closeAddUser(); loadUsers(); })
      .catch(function (e) { $('auErr').textContent = (e && e.message) || '添加失败'; });
  };

  /* ---------- 分组管理 ---------- */
  var groupAll = [], groupById = {};
  function loadGroups() {
    request.get('/ovpn/group', { silent: true }).then(function (groups) {
      groupAll = groups || [];
      groupById = {};
      groupAll.forEach(function (g) { groupById[g.id] = g; });
      renderGroups();
    }).catch(function () { $('groupsTbody').innerHTML = '<tr><td colspan="4" class="empty">加载失败，请刷新</td></tr>'; });
  }
  function groupName(id) {
    var g = groupById[id];
    return g ? g.name : '—';
  }
  function renderGroups() {
    var tb = $('groupsTbody');
    if (!groupAll.length) { tb.innerHTML = '<tr><td colspan="4" class="empty">暂无分组</td></tr>'; return; }
    tb.innerHTML = groupAll.map(function (g) {
      var cfg = g.config || '';
      var preview = cfg ? (cfg.length > 48 ? esc(cfg.slice(0, 48)) + '…' : esc(cfg)) : '<span class="muted">—</span>';
      var parent = g.parent_id != null ? groupName(g.parent_id) : '（根分组）';
      return '<tr>' +
        '<td>' + esc(g.name || '—') + '</td>' +
        '<td>' + esc(parent) + '</td>' +
        '<td class="mono" style="max-width:340px;white-space:pre-wrap;word-break:break-all">' + preview + '</td>' +
        '<td><div class="row-actions" style="justify-content:flex-end">' +
          '<button class="icon-btn" data-act="members" data-id="' + esc(g.id) + '" title="成员" aria-label="成员"><svg class="icon icon-sm"><use href="#i-user"/></svg></button>' +
          '<button class="icon-btn" data-act="editgroup" data-id="' + esc(g.id) + '" title="编辑" aria-label="编辑"><svg class="icon icon-sm"><use href="#i-edit"/></svg></button>' +
          '<button class="icon-btn btn-danger" data-act="delgroup" data-id="' + esc(g.id) + '" title="删除" aria-label="删除"><svg class="icon icon-sm"><use href="#i-trash"/></svg></button>' +
        '</div></td></tr>';
    }).join('');
  }
  $('groupsTbody').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-act]'); if (!btn) return;
    var id = btn.dataset.id, act = btn.dataset.act;
    if (act === 'members') openGroupMembers(id);
    else if (act === 'editgroup') openEditGroup(id);
    else if (act === 'delgroup') {
      var g = groupById[id];
      if (!confirm('确认删除分组「' + (g ? g.name : id) + '」？\n该分组的成员将改挂到 Default 分组，子分组也会改挂到 Default，不会删除任何用户。')) return;
      request.delete('/ovpn/group/' + encodeURIComponent(id)).then(function () { toast('已删除'); loadGroups(); }).catch(function () {});
    }
  });
  function descendantIds(rootId) {
    // 递归收集 rootId 的所有后代分组 id，用于防止把分组挂到自己的子孙下形成环
    var out = {}, stack = [rootId];
    while (stack.length) {
      var cur = stack.pop();
      groupAll.forEach(function (g) {
        if (g.parent_id != null && String(g.parent_id) === String(cur) && !out[g.id]) {
          out[g.id] = true; stack.push(g.id);
        }
      });
    }
    return out;
  }
  function fillGroupParentSelect(sel, excludeId, allowRoot) {
    var blocked = descendantIds(excludeId || ''); // 排除自身及所有后代，避免成环
    if (excludeId) blocked[excludeId] = true;
    var opts = '';
    if (allowRoot) opts += '<option value="">（根分组 / 无上级）</option>';
    opts += groupAll.filter(function (g) { return !blocked[g.id]; }).map(function (g) {
      return '<option value="' + esc(g.id) + '">' + esc(g.name) + '</option>';
    }).join('');
    sel.innerHTML = opts;
  }
  function openEditGroup(id) {
    $('egErr').textContent = '';
    if (id) {
      var g = groupById[id];
      $('editGroupTitle').textContent = '编辑分组';
      $('egId').value = g.id || '';
      $('egName').value = g.name || '';
      egParseConfig(g.config || '');
      var isRoot = g.parent_id == null;
      fillGroupParentSelect($('egParent'), g.id, isRoot); // 排除自身及后代，避免成环；根分组可保留为根
      $('egParent').value = isRoot ? '' : String(g.parent_id);
    } else {
      $('editGroupTitle').textContent = '添加分组';
      $('egId').value = '';
      $('egName').value = '';
      egParseConfig('');
      fillGroupParentSelect($('egParent'), null, false);
      var def = groupAll.filter(function (g) { return g.name === 'Default'; })[0] || groupAll[0];
      if (def) $('egParent').value = String(def.id);
    }
    $('editGroupModal').hidden = false;
    $('egName').focus();
  }
  function closeEditGroup() { $('editGroupModal').hidden = true; }
  $('editGroupClose').addEventListener('click', closeEditGroup);
  $('editGroupCancel').addEventListener('click', closeEditGroup);
  $('editGroupSave').addEventListener('click', function () {
    var id = $('egId').value;
    var name = $('egName').value.trim();
    if (!name) { $('egErr').textContent = '请填写分组名称'; return; }
    if (name.length > 50) { $('egErr').textContent = '分组名称过长（最多 50 字符）'; return; }
    // v1.0.80：分组推送参数合法性校验——route 网段与 DNS 地址在保存时拦截，防止坏 push 下发客户端
    var routeCb = $('egPreset_route'), dnsCb = $('egPreset_dns');
    if (routeCb && routeCb.checked) {
      var rp = $('egPresetParam_route').value.trim();
      if (!rp) { $('egErr').textContent = '请填写访问内网网段（如 192.168.0.0 255.255.0.0）'; return; }
      if (!isValidRouteParam(rp)) { $('egErr').textContent = '内网网段格式不正确（应为「网段 掩码」或「网段/前缀」，如 192.168.0.0 255.255.0.0）'; return; }
    }
    if (dnsCb && dnsCb.checked) {
      var dp = $('egPresetParam_dns').value.trim();
      if (!dp) { $('egErr').textContent = '请填写 DNS 地址'; return; }
      if (!isValidIP(dp)) { $('egErr').textContent = 'DNS 地址格式不正确'; return; }
    }
    var rawParent = $('egParent').value;
    var payload = { name: name, config: egAssembleConfig() };
    if (rawParent !== '') payload.parent_id = Number(rawParent); // 根分组不发送 parent_id，保留为无上级
    if (id) {
      payload.id = id;
      request.patch('/ovpn/group', payload).then(function (d) {
        toast(d.message || '已保存'); closeEditGroup(); loadGroups();
      }).catch(function (e) { $('egErr').textContent = (e && e.message) || '保存失败'; });
    } else {
      request.post('/ovpn/group', payload).then(function (d) {
        toast(d.message || '已添加'); closeEditGroup(); loadGroups();
      }).catch(function (e) { $('egErr').textContent = (e && e.message) || '添加失败'; });
    }
  });
  $('addGroupBtn').addEventListener('click', function () { openEditGroup(null); });

  // ---- 分组配置：常用场景预设（多选，小白友好） ----
  // 预设只产出标准 push 指令；自定义框保留任意原始指令，二者拼接为最终 config。
  var OVC_PRESETS = [
    {
      key: 'route', title: '访问内网网段',
      desc: '让 VPN 客户端能连上你家里的局域网（填写要访问的内网网段与子网掩码）',
      hasParam: true, defParam: '192.168.0.0 255.255.0.0',
      build: function (p) { return 'push "route ' + p + '"'; },
      match: function (line) {
        var m = line.match(/^\s*push\s+"route\s+([^"]+)"/i) || line.match(/^\s*route\s+([^\s]+)\s+([^\s]+)/i);
        return m ? (m[2] !== undefined ? m[1] + ' ' + m[2] : m[1]) : null;
      }
    },
    {
      key: 'dns', title: '下发 DNS',
      desc: '让客户端使用指定 DNS（建议国内可用地址，如 223.5.5.5，勿用 Google）',
      hasParam: true, defParam: '223.5.5.5',
      build: function (p) { return 'push "dhcp-option DNS ' + p + '"'; },
      match: function (line) {
        var m = line.match(/^\s*push\s+"dhcp-option\s+DNS\s+([^"]+)"/i) || line.match(/^\s*dhcp-option\s+DNS\s+([^\s]+)/i);
        return m ? m[1] : null;
      }
    },
    {
      key: 'redirect', title: '全局代理（所有流量走 VPN）',
      desc: '客户端所有上网流量都经 VPN 转发；与全局设置叠加，分组无法取消已开启的全局隧道',
      hasParam: false,
      build: function () { return 'push "redirect-gateway def1 bypass-dhcp"'; },
      match: function (line) { return /redirect-gateway/i.test(line) ? '' : null; }
    },
    {
      key: 'blockdns', title: '阻止 DNS 泄漏',
      desc: 'Windows 客户端专用，防止系统 DNS 绕过 VPN 造成泄露',
      hasParam: false,
      build: function () { return 'push "block-outside-dns"'; },
      match: function (line) { return /block-outside-dns/i.test(line) ? '' : null; }
    }
  ];

  // 首次注入预设 DOM（仅一次），并绑定联动事件
  function egInitPresets() {
    var box = $('egPresets'); if (!box) return;
    box.innerHTML = OVC_PRESETS.map(function (p) {
      var param = p.hasParam
        ? '<div class="ovcfg-paramwrap"><input class="input ovcfg-param" id="egPresetParam_' + p.key + '" placeholder="' + esc(p.defParam) + '" /></div>'
        : '';
      return '<div class="ovcfg-row" data-key="' + p.key + '">' +
        '<label class="ovcfg-toggle"><input type="checkbox" id="egPreset_' + p.key + '" />' +
        '<span class="ovcfg-title">' + esc(p.title) + '</span></label>' +
        '<div class="ovcfg-desc">' + esc(p.desc) + '</div>' + param + '</div>';
    }).join('');
    OVC_PRESETS.forEach(function (p) {
      var cb = $('egPreset_' + p.key);
      cb.addEventListener('change', function () {
        var row = cb.closest('.ovcfg-row');
        row.classList.toggle('on', cb.checked);
        if (cb.checked && p.hasParam) {
          var inp = $('egPresetParam_' + p.key);
          if (!inp.value.trim()) inp.value = p.defParam;
        }
        egUpdatePreview();
      });
      if (p.hasParam) $('egPresetParam_' + p.key).addEventListener('input', egUpdatePreview);
    });
    $('egConfig').addEventListener('input', egUpdatePreview);
  }

  // 由勾选项 + 自定义框组装最终 config 字符串
  function egAssembleConfig() {
    var lines = [];
    OVC_PRESETS.forEach(function (p) {
      var cb = $('egPreset_' + p.key);
      if (cb && cb.checked) {
        var val = p.hasParam ? $('egPresetParam_' + p.key).value.trim() : '';
        if (p.hasParam && !val) return; // 参数缺失则跳过该预设
        lines.push(p.build(val));
      }
    });
    var custom = $('egConfig').value.trim();
    if (custom) custom.split(/\r?\n/).forEach(function (l) { if (l.trim()) lines.push(l.trim()); });
    return lines.join('\n');
  }

  // 实时预览
  function egUpdatePreview() {
    var pre = $('egConfigPreview'); if (!pre) return;
    var cfg = egAssembleConfig();
    pre.textContent = cfg || '（未配置，客户端仅继承上级分组与全局设置）';
    pre.classList.toggle('empty', !cfg);
  }

  // 打开弹窗时：把已有 config 解析回填到预设 + 自定义框（保证旧数据不丢）
  function egParseConfig(cfg) {
    var lines = (cfg || '').split(/\r?\n/);
    var remaining = [];
    OVC_PRESETS.forEach(function (p) {
      var found = false, param = '';
      for (var i = 0; i < lines.length; i++) {
        var m = p.match(lines[i]);
        if (m !== null) { found = true; param = p.hasParam ? m : ''; break; }
      }
      var cb = $('egPreset_' + p.key);
      if (cb) {
        cb.checked = found;
        cb.closest('.ovcfg-row').classList.toggle('on', found);
        if (found && p.hasParam) $('egPresetParam_' + p.key).value = param || p.defParam;
        else if (!found && p.hasParam) $('egPresetParam_' + p.key).value = '';
      }
    });
    lines.forEach(function (l) {
      if (!l.trim()) return;
      var matched = OVC_PRESETS.some(function (p) { return p.match(l) !== null; });
      if (!matched) remaining.push(l.trim());
    });
    $('egConfig').value = remaining.join('\n');
    egUpdatePreview();
  }

  egInitPresets();

  function openGroupMembers(id) {
    var g = groupById[id];
    $('gmGroupName').textContent = '分组：' + (g ? g.name : id) + (g && g.config ? '（含继承自上级的配置）' : '');
    $('gmErr').textContent = '';
    var tb = $('gmTbody');
    tb.innerHTML = '<tr><td colspan="6" class="empty">加载中…</td></tr>';
    $('groupMembersModal').hidden = false;
    request.get('/ovpn/group/' + encodeURIComponent(id) + '/users', { silent: true }).then(function (r) {
      var users = (r && r.users) || [];
      if (!users.length) { tb.innerHTML = '<tr><td colspan="6" class="empty">该分组暂无成员</td></tr>'; return; }
      tb.innerHTML = users.map(function (u) {
        var on = u.isEnable;
        return '<tr>' +
          '<td>' + esc(u.username || '—') + '</td>' +
          '<td>' + esc(u.name || '—') + '</td>' +
          '<td><span class="badge ' + (on ? 'on' : 'off') + '"><span class="dot"></span>' + (on ? '启用' : '禁用') + '</span></td>' +
          '<td>' + (u.online ? '<span class="badge on"><span class="dot"></span>在线</span>' : '<span class="badge off"><span class="dot"></span>离线</span>') + '</td>' +
          '<td>' + fmtBytes(u.recvBytes) + '</td>' +
          '<td>' + fmtBytes(u.sendBytes) + '</td>' +
        '</tr>';
      }).join('');
    }).catch(function (e) { $('gmErr').textContent = (e && e.message) || '加载失败'; });
  }
  function closeGroupMembers() { $('groupMembersModal').hidden = true; }
  $('groupMembersClose').addEventListener('click', closeGroupMembers);
  $('groupMembersCancel').addEventListener('click', closeGroupMembers);

  /* ---------- 证书 ---------- */
  var certCache = {};
  function setCertBadge(id, c) {
    var el = $(id);
    if (!c) { el.className = 'badge'; el.innerHTML = '<span class="dot"></span>—'; return; }
    var ok = !/revok|expire|invalid/i.test(c.status || '');
    el.className = 'badge ' + (ok ? 'on' : 'warn');
    el.innerHTML = '<span class="dot"></span>' + (ok ? '有效' : (c.status || '异常'));
  }
  function cnOf(s) {
    var m = /CN=([^,]+)/.exec(s || '');
    return m ? m[1].trim() : (s || '');
  }
  function loadCerts() {
    request.get('/ovpn/certs', { silent: true }).then(function (d) {
      var list = (Array.isArray(d) ? d : (d && d.data)) || [];
      var ca = list.filter(function (c) { return (c.kind || '') === 'ca'; })[0];
      var srv = list.filter(function (c) { return (c.kind || '') === 'server'; })[0];
      var cli = list.filter(function (c) { return (c.kind || '') === 'client'; });
      var revoked = list.filter(function (c) { return c.revoked === true; }).length;
      setCertBadge('certCA', ca);
      setCertBadge('certServer', srv);
      $('certClient').textContent = cli.length;
      $('certRevoked').textContent = revoked;
      var tb = $('certsTbody');
      if (!list.length) { tb.innerHTML = '<tr><td colspan="5" class="empty">暂无证书</td></tr>'; return; }
      certCache = {};
      tb.innerHTML = list.map(function (c, idx) {
        var key = String(idx);
        certCache[key] = c;
        var isClient = (c.kind || '') === 'client';
        var isCA = (c.kind || '') === 'ca';
        var isServer = (c.kind || '') === 'server';
        var isRevoked = c.revoked === true || /revok|吊销/i.test(c.status || '');
        var ok = !isRevoked && !/expire|invalid|过期/i.test(c.status || '');
        var badge = '<span class="badge ' + (ok ? 'on' : 'warn') + '"><span class="dot"></span>' + esc(c.status || '正常') + '</span>';

        var btns = ['<button class="icon-btn" data-act="detail" data-key="' + key + '" title="详情" aria-label="详情"><svg class="icon icon-sm"><use href="#i-info"/></svg></button>'];
        if (isCA) {
          btns.push('<button class="icon-btn" data-act="dlca" title="下载 CA 证书" aria-label="下载 CA 证书"><svg class="icon icon-sm"><use href="#i-download"/></svg></button>');
        }
        if (isServer) {
          btns.push('<button class="icon-btn" data-act="renew" title="续期" aria-label="续期"><svg class="icon icon-sm"><use href="#i-gear"/></svg></button>');
        }
        if (isClient && !isRevoked) {
          btns.push('<button class="icon-btn" data-act="dlovpn" data-name="' + esc(c.name) + '" title="下载配置" aria-label="下载配置"><svg class="icon icon-sm"><use href="#i-download"/></svg></button>');
          btns.push('<button class="icon-btn btn-danger" data-act="revoke" data-name="' + esc(c.name) + '" title="吊销" aria-label="吊销"><svg class="icon icon-sm"><use href="#i-trash"/></svg></button>');
        }

        var cn = cnOf(c.subject) || c.name;
        return '<tr>' +
          '<td>' + esc(c.type || '—') + '</td>' +
          '<td class="mono">' + esc(cn) + '</td>' +
          '<td class="mono">' + esc(c.notAfter || '—') + (c.expiresIn ? ' <span class="muted">(' + esc(c.expiresIn) + ')</span>' : '') + '</td>' +
          '<td>' + badge + '</td>' +
          '<td><div class="row-actions" style="justify-content:flex-end">' + btns.join('') + '</div></td>' +
        '</tr>';
      }).join('');
    }).catch(function () {});
  }
  $('certsTbody').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-act]');
    if (!btn) return;
    var act = btn.dataset.act, name = btn.dataset.name;
    if (act === 'detail') {
      openCertDetail(certCache[btn.dataset.key]);
    } else if (act === 'dlca') {
      downloadCA();
    } else if (act === 'renew') {
      openCertManage();
    } else if (act === 'dlovpn') {
      downloadClient(name);
    } else if (act === 'revoke') {
      if (!confirm('确认吊销客户端证书「' + name + '」？该操作不可恢复，且会同步移除其 VPN 配置。')) return;
      request.delete('/ovpn/client/' + encodeURIComponent(name)).then(function () {
        toast('已吊销 ' + name); loadCerts();
      }).catch(function () {});
    }
  });

  function openCertDetail(c) {
    if (!c) return;
    var ok = !(c.revoked === true) && !/expire|invalid|过期/i.test(c.status || '');
    $('cdType').textContent = c.type || '—';
    $('cdSubject').textContent = c.subject || '—';
    $('cdIssuer').textContent = c.issuer || '—';
    $('cdSerial').textContent = c.serialNo || '—';
    $('cdNotBefore').textContent = c.notBefore || '—';
    $('cdNotAfter').textContent = c.notAfter || '—';
    $('cdExpiresIn').textContent = c.expiresIn || '—';
    $('cdStatus').innerHTML = '<span class="badge ' + (ok ? 'on' : 'warn') + '"><span class="dot"></span>' + esc(c.status || '正常') + '</span>';
    $('certDetailModal').hidden = false;
  }
  function closeCertDetail() { $('certDetailModal').hidden = true; }
  $('certDetailClose').addEventListener('click', closeCertDetail);
  $('certDetailCancel').addEventListener('click', closeCertDetail);

  function downloadCA() {
    var a = document.createElement('a');
    a.href = BASE + '/ovpn/ca';
    a.download = 'ca.crt';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  }

  function openCertManage() { $('certManageErr').textContent = ''; $('certManageModal').hidden = false; }
  function closeCertManage() { $('certManageModal').hidden = true; }
  $('certManageBtn').addEventListener('click', openCertManage);
  $('certManageClose').addEventListener('click', closeCertManage);
  $('certManageCancel').addEventListener('click', closeCertManage);
  $('caDownloadBtn').addEventListener('click', downloadCA);
  $('renewServerBtn').addEventListener('click', function () {
    var days = parseInt($('renewDays').value, 10);
    if (!(days > 0)) { $('certManageErr').textContent = '请输入有效的续期天数'; return; }
    $('certManageErr').textContent = '';
    request.post('/ovpn/server', { action: 'renewCert', day: String(days) }).then(function (r) {
      toast((r && r.message) || '服务端证书已续期');
      closeCertManage();
      loadCerts();
    }).catch(function (e) {
      $('certManageErr').textContent = (e && e.message) || '续期失败';
    });
  });

  /* ---------- 连接记录（含分页） ---------- */
  /* 通用本地分页渲染：prefix 为 pager 元素 id 前缀（如 'clientPg' → clientPgRoot/Info/First/Prev/Page/Next/Last） */
  function renderLocalPager(prefix, page, perPage, total, onChange) {
    var pages = Math.max(1, Math.ceil(total / perPage));
    if (page > pages) page = pages;
    if (page < 1) page = 1;
    $(prefix + 'Root').hidden = total <= perPage;
    $(prefix + 'Info').textContent = '共 ' + total + ' 条';
    $(prefix + 'Page').textContent = page + ' / ' + pages;
    $(prefix + 'First').disabled = page <= 1;
    $(prefix + 'Prev').disabled = page <= 1;
    $(prefix + 'Next').disabled = page >= pages;
    $(prefix + 'Last').disabled = page >= pages;
    $(prefix + 'First').onclick = function () { onChange(1); };
    $(prefix + 'Prev').onclick = function () { onChange(page - 1); };
    $(prefix + 'Next').onclick = function () { onChange(page + 1); };
    $(prefix + 'Last').onclick = function () { onChange(pages); };
    return page;
  }
  var histState = { start: 0, length: 10, total: 0, draw: 1 };
  function histRenderPager() {
    var len = histState.length, total = histState.total;
    var pageCount = Math.max(1, Math.ceil(total / len));
    var cur = Math.floor(histState.start / len) + 1;
    $('histCount').textContent = '共 ' + total + ' 条';
    $('histPage').textContent = cur + ' / ' + pageCount;
    $('histFirst').disabled = histState.start <= 0;
    $('histPrev').disabled = histState.start <= 0;
    $('histNext').disabled = histState.start + len >= total;
    $('histLast').disabled = histState.start + len >= total;
  }
  function loadHistory(opts) {
    opts = opts || {};
    if (opts.reset) histState.start = 0;
    if (typeof opts.start === 'number') histState.start = opts.start;
    histState.draw++;
    request.get('/ovpn/history?draw=' + histState.draw + '&start=' + histState.start + '&length=' + histState.length, { silent: true }).then(function (d) {
      var list = (Array.isArray(d) ? d : (d && d.data)) || [];
      var tb = $('historyTbody');
      if (!list.length) { tb.innerHTML = '<tr><td colspan="5" class="empty">暂无连接记录</td></tr>'; histState.total = 0; histRenderPager(); return; }
      if (d && typeof d.recordsTotal === 'number') histState.total = d.recordsTotal;
      tb.innerHTML = list.map(function (h) {
        var raw = h.time_unix ? String(h.time_unix) : '';
        var tStr = /^1970-/.test(raw) ? '—' : raw; // 后端已格式化为字符串（如 2026-07-30 23:59:01）；time_unix=0 的脏记录按缺失展示
        return '<tr>' +
          '<td class="mono">' + esc(tStr || '—') + '</td>' +
          '<td>' + esc(h.username || '—') + '</td>' +
          '<td>' + esc(h.common_name || '—') + '</td>' +
          '<td>' + (h.vip6 ? '<div class="mono">' + esc(h.vip || '—') + '</div><div class="mono sub">' + esc(h.vip6) + '</div>' : '<div class="mono">' + esc(h.vip || '—') + '</div>') + '</td>' +
          '<td><span class="badge on"><span class="dot"></span>成功</span></td>' +
        '</tr>';
      }).join('');
      histRenderPager();
    }).catch(function () {});
  }
  $('histFirst').addEventListener('click', function () { loadHistory({ start: 0 }); });
  $('histPrev').addEventListener('click', function () { loadHistory({ start: Math.max(0, histState.start - histState.length) }); });
  $('histNext').addEventListener('click', function () { loadHistory({ start: histState.start + histState.length }); });
  $('histLast').addEventListener('click', function () { loadHistory({ start: Math.max(0, (Math.ceil(histState.total / histState.length) - 1) * histState.length) }); });

  /* ---------- 初始化向导（原生，移植 setup-wizard 逻辑） ---------- */
  function openWizard() { $('wizardOverlay').hidden = false; }
  function closeWizard() { $('wizardOverlay').hidden = true; }
  function initWizard() {
    var step = 1;
    var state = { addr: '', port: '1194', proto: 'udp' };
    function err(n, msg) { var b = $('wizErr' + n); if (b) b.textContent = msg || ''; }
    function render() {
      document.querySelectorAll('#wizardOverlay .wpanel').forEach(function (p) { p.hidden = (+p.dataset.wp !== step); });
      document.querySelectorAll('#wizSteps .step').forEach(function (s) {
        var n = +s.dataset.s;
        s.classList.toggle('active', n === step);
        s.classList.toggle('done', n < step);
      });
      $('wizPrev').hidden = step === 1;
      $('wizNext').hidden = step === 4;
      $('wizGen').hidden = step !== 4;
      $('wizFinish').hidden = step !== 4;
      $('wizFinish').disabled = step === 4 && $('wizDone').hidden;
    }
    function s1() {
      var p1 = $('wizPass1').value, p2 = $('wizPass2').value;
      // 密码策略（v1.0.40）：管理员密码至少 8 位
      if (p1.length < 8) { err(1, '管理员密码至少 8 位'); return; }
      if (p1 !== p2) { err(1, '两次输入的密码不一致'); return; }
      err(1, '');
      request.post('/settings', { 'system.base.admin_password': p1 }).then(function () { step = 2; render(); }).catch(function (e) { err(1, (e && e.message) || '保存失败'); });
    }
    function s2() {
      var addr = $('wizAddr').value.trim(), port = $('wizPort').value.trim() || '1194', proto = $('wizProto').value;
      var ipv6 = $('wizIpv6').checked, ipv6Listen = $('wizIpv6Listen').checked, gateway = $('wizGateway').checked;
      if (!addr) { err(2, '请填写服务器对外 IP 或域名'); return; }
      // v1.0.80：向导复用设置页的地址/端口校验，避免坏值直达后端
      if (!isValidServerAddr(addr)) { err(2, '服务器地址格式不正确（仅支持 IPv4 / IPv6 / 域名，不能含空格、逗号等）'); return; }
      if (port && !isValidPort(port)) { err(2, '端口必须是 1–65535 之间的整数'); return; }
      err(2, '');
      request.post('/settings', { 'system.base.server_addr': addr, 'openvpn.ovpn_port': port, 'openvpn.ovpn_proto': proto, 'openvpn.ovpn_ipv6': ipv6, 'openvpn.ovpn_ipv6_listen': ipv6Listen, 'openvpn.ovpn_gateway': gateway })
        .then(function () { state.addr = addr; state.port = port; state.proto = proto; step = 3; render(); }).catch(function (e) { err(2, (e && e.message) || '保存失败'); });
    }
    function s3() {
      var u = $('wizUser').value.trim(), p1 = $('wizUserPass1').value, p2 = $('wizUserPass2').value;
      if (!u) { err(3, '请填写用户名'); return; }
      if (!/^[a-zA-Z0-9_.-]{1,64}$/.test(u)) { err(3, '用户名仅支持字母、数字、_ . -，长度 1–64'); return; }
      if (p1.length < 6) { err(3, '密码至少 6 位'); return; }
      if (p1 !== p2) { err(3, '两次输入的密码不一致'); return; }
      err(3, '');
      request.post('/ovpn/user', { username: u, password: p1, name: u, isEnable: true }).then(function () { step = 4; render(); }).catch(function (e) { err(3, (e && e.message) || '添加失败'); });
    }
    function gen() {
      var name = $('wizClient').value.trim() || 'my-device';
      $('wizGen').disabled = true; err(4, '');
      request.post('/ovpn/client', { name: name, serverAddr: state.addr, serverPort: state.port, config: '', ccdConfig: '', mfa: '' })
        .then(function () { return request.get('/ovpn/client/' + encodeURIComponent(name) + '/config'); })
        .then(function (cfg) {
          var text = (cfg && cfg.content) || '';
          var blob = new Blob([text], { type: 'text/plain' });
          var url = URL.createObjectURL(blob);
          var a = document.createElement('a'); a.href = url; a.download = name + '.ovpn'; a.click();
          URL.revokeObjectURL(url);
          $('wizDone').hidden = false; $('wizFinish').disabled = false;
        }).catch(function (e) { err(4, (e && e.message) || '生成失败'); $('wizGen').disabled = false; });
    }
    function finish() {
      request.post('/settings', { 'system.base.init_done': true })
        .then(function () {
          // 向导完成自动重启服务：让第 2 步保存的协议/端口/IPv6/网关配置同步到 server.conf 并真正生效
          // （restartSrv = config.json→server.conf 增量同步 + ensure_nat + SIGHUP 重载）
          return request.post('/ovpn/server', { action: 'restartSrv' });
        })
        .then(function () { closeWizard(); toast('初始化完成，服务已重启生效'); })
        .catch(function (e) { closeWizard(); toast('初始化完成（服务重启失败：' + ((e && e.message) || '未知错误') + '，可到设置页手动重启）'); });
    }
    $('wizNext').onclick = function () { if (step === 1) s1(); else if (step === 2) s2(); else if (step === 3) s3(); };
    $('wizPrev').onclick = function () { if (step > 1) { step--; render(); } };
    $('wizGen').onclick = gen;
    $('wizFinish').onclick = finish;
    $('wizClose').onclick = closeWizard;
    render();
  }
  $('wizardBtn').addEventListener('click', openWizard);

  /* ---------- 初始化 ---------- */
  // 先鉴权再渲染：未登录（401）立即跳登录页，避免管理界面骨架闪一下（旧 client.html 的闪烁即由此类时序引起）
  request.get('/api/bootstrap', { silent: true }).then(function (boot) {
    document.body.classList.remove('boot'); // 鉴权通过，显示管理界面
    initWizard();
    loadSettings();
    showPanel('dashboard');
    if (boot && !boot.init_done) openWizard();
    // 仪表盘每 5 秒静默刷新
    setInterval(function () {
      var dash = document.querySelector('[data-panel="dashboard"]');
      if (dash && !dash.hidden) loadDashboard(true);
    }, 5000);
  }).catch(function (e) {
    // request 封装内 401 已跳转 /login；此处兜底（如 fetch 异常）
    if (location.pathname.indexOf('/login') === -1) location.href = BASE + '/login';
  });
})();
