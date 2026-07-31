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
  var titleMap = { dashboard: '仪表盘', clients: '客户端', users: '用户', certs: '证书', history: '连接记录', settings: '设置' };
  function showPanel(s) {
    document.querySelectorAll('.nav-item').forEach(function (n) { n.classList.toggle('active', n.dataset.screen === s); });
    document.querySelectorAll('[data-panel]').forEach(function (p) { p.hidden = p.dataset.panel !== s; });
    $('pageTitle').textContent = titleMap[s] || s;
    $('pageCrumb').textContent = 'OpenVPN 服务器 · ' + (titleMap[s] || s);
    if (s === 'dashboard') loadDashboard();
    else if (s === 'clients') loadClients();
    else if (s === 'users') loadUsers();
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
    if (!onlineAll.length) { tb.innerHTML = '<tr><td colspan="7" class="empty">当前无在线客户端</td></tr>'; renderLocalPager('onlinePg', 1, ONLINE_PER_PAGE, 0, function () {}); return; }
    var start = (onlinePage - 1) * ONLINE_PER_PAGE;
    var slice = onlineAll.slice(start, start + ONLINE_PER_PAGE);
    tb.innerHTML = slice.map(function (c) {
      var name = c.commonName || c.username || '—';
      var vip = c.vip || c.vip6 || '—';
      var rateStr = fmtRate(c.rateBps);
      return '<tr>' +
        '<td>' + esc(name) + '</td>' +
        '<td class="mono">' + esc(vip) + '</td>' +
        '<td>' + rateStr + '</td>' +
        '<td>' + esc(c.onlineTime || '—') + '</td>' +
        '<td>' + fmtBytes(c.recvBytes) + '</td>' +
        '<td>' + fmtBytes(c.sendBytes) + '</td>' +
        '<td><div class="row-actions" style="justify-content:flex-end">' +
          '<button class="icon-btn" data-act="kill" data-cid="' + esc(name) + '" aria-label="断开"><svg class="icon icon-sm"><use href="#i-close"/></svg></button>' +
          '<button class="icon-btn" data-act="limit" data-vip="' + esc(vip) + '" aria-label="限速"><svg class="icon icon-sm"><use href="#i-bolt"/></svg></button>' +
          '<button class="icon-btn btn-danger" data-act="ban" data-vip="' + esc(vip) + '" aria-label="禁网"><svg class="icon icon-sm"><use href="#i-lock"/></svg></button>' +
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
    }).catch(function () {});
  }

  /* 在线客户端操作 */
  $('onlineTbody').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-act]'); if (!btn) return;
    var act = btn.dataset.act;
    if (act === 'kill') {
      request.post('/ovpn/kill', { cid: btn.dataset.cid }).then(function () { toast('已断开 ' + btn.dataset.cid); loadDashboard(true); }).catch(function () {});
    } else if (act === 'ban') {
      request.post('/ovpn/firewall?a=add_blacklist', { vip: btn.dataset.vip }).then(function () { toast('已拉黑 ' + btn.dataset.vip); }).catch(function () {});
    } else if (act === 'limit') {
      uiPrompt({ title: '限速 (Mbps)', placeholder: '0 表示不限速', validate: function (v) {
        if (v === '') return '请输入数值';
        var n = Number(v);
        if (isNaN(n) || n < 0) return '请输入非负数字';
        return '';
      } }).then(function (v) {
        if (v === null) return;
        request.post('/ovpn/firewall?a=set_rateLimit', { vip: btn.dataset.vip, rate: v }).then(function () { toast('已限速 ' + btn.dataset.vip); }).catch(function () {});
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
    }).catch(function () {});
  }
  $('saveSettings').addEventListener('click', function () {
    var addr = $('setAddr').value.trim();
    var port = $('setPort').value.trim();
    var proto = $('setProto').value;
    var subnet = $('setSubnet').value.trim();
    var p1 = $('setPass').value, p2 = $('setPass2').value;
    var payload = {};
    if (addr) payload['system.base.server_addr'] = addr;
    if (port) payload['openvpn.ovpn_port'] = port;
    if (proto) payload['openvpn.ovpn_proto'] = proto;
    if (subnet) payload['openvpn.ovpn_subnet'] = subnet;
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
        // 端口/协议/子网修改需 openvpn 重启才重新生成 server.conf
        var needRestart = payload['openvpn.ovpn_port'] || payload['openvpn.ovpn_proto'] || payload['openvpn.ovpn_subnet'];
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
        '<td class="mono">' + esc(c.vip || '—') + '</td>' +
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
    uiPrompt({ title: '客户端名称', placeholder: '例如 laptop / phone' }).then(function (name) {
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
    $('ceErr').textContent = '';
    $('clientEditModal').hidden = false;
    request.get('/ovpn/client/' + encodeURIComponent(name) + '/ccd', { silent: true }).then(function (d) {
      var content = (d && d.content) || '';
      var m = /ifconfig-push\s+(\S+)/i.exec(content);
      if (m) $('ceIp').value = m[1];
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
    var content = '';
    if (ip) {
      if (!/^\d{1,3}(\.\d{1,3}){3}$/.test(ip)) { $('ceErr').textContent = 'IP 格式不正确'; return; }
      content = 'ifconfig-push ' + ip + ' 255.255.255.0';
    }
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
          (r.users || []).forEach(function (u) { all.push(u); usersById[u.id] = u; });
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
    if (p.length < 6) { $('auErr').textContent = '密码至少 6 位'; return; }
    request.post('/ovpn/user', { username: u, password: p, name: u, isEnable: true })
      .then(function () { toast('已添加用户 ' + u); closeAddUser(); loadUsers(); })
      .catch(function (e) { $('auErr').textContent = (e && e.message) || '添加失败'; });
  };

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
          '<td class="mono">' + esc(h.vip || h.vip6 || '—') + '</td>' +
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
      if (!addr) { err(2, '请填写服务器对外 IP 或域名'); return; }
      err(2, '');
      request.post('/settings', { 'system.base.server_addr': addr, 'openvpn.ovpn_port': port, 'openvpn.ovpn_proto': proto })
        .then(function () { state.addr = addr; state.port = port; state.proto = proto; step = 3; render(); }).catch(function (e) { err(2, (e && e.message) || '保存失败'); });
    }
    function s3() {
      var u = $('wizUser').value.trim(), p1 = $('wizUserPass1').value, p2 = $('wizUserPass2').value;
      if (!u) { err(3, '请填写用户名'); return; }
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
      request.post('/settings', { 'system.base.init_done': true }).then(function () { closeWizard(); toast('初始化完成'); }).catch(function (e) { toast((e && e.message) || '完成失败'); });
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
  initWizard();
  loadSettings();
  showPanel('dashboard');
  // 未初始化则自动弹向导
  request.get('/api/bootstrap', { silent: true }).then(function (boot) {
    if (boot && !boot.init_done) openWizard();
  }).catch(function () {});
  // 仪表盘每 5 秒静默刷新
  setInterval(function () {
    var dash = document.querySelector('[data-panel="dashboard"]');
    if (dash && !dash.hidden) loadDashboard(true);
  }, 5000);
})();
