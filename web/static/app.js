'use strict';

// 从 URL fragment 读取 token；fragment 不会被发送到服务端或代理日志。
(function initToken() {
  const u = new URL(window.location.href);
  const fragment = new URLSearchParams(window.location.hash.slice(1));
  const t = fragment.get('token');
  if (t) {
    sessionStorage.setItem('weclaw_token', t);
    u.hash = '';
    window.history.replaceState({}, '', u.toString());
  }
})();

function token() { return sessionStorage.getItem('weclaw_token') || ''; }

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json', 'X-WeClaw-Token': token() },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) throw new Error((await res.text()) || res.status);
  const ct = res.headers.get('content-type') || '';
  return ct.includes('application/json') ? res.json() : res.text();
}

function toast(msg) {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.style.display = 'block';
  setTimeout(() => { el.style.display = 'none'; }, 3000);
}

let currentConfig = null;

async function loadConfig() {
  currentConfig = await api('GET', '/api/config');
  document.getElementById('workspace-roots').value = (currentConfig.allowed_workspace_roots || []).join('\n');
  document.getElementById('rate-limit').value = currentConfig.rate_limit_per_minute || 0;
}

async function loadStatus() {
  const st = await api('GET', '/api/status');
  const status = document.getElementById('status');
  status.replaceChildren();
  const daemon = document.createElement('span');
  daemon.className = st.daemon_running ? 'pill ok' : 'pill warn';
  daemon.textContent = st.daemon_running ? '守护进程运行中' : '守护进程未运行';
  status.appendChild(daemon);
  for (const p of (st.platforms || [])) {
    appendStatusLine(status, `${p.name}${p.account_id ? ` (${p.account_id})` : ''}: ${p.enabled ? '启用' : '停用'} · 凭证${p.credentials_present ? '已配置' : '缺失'} · 白名单 ${p.allowed_users_count} 人`);
  }
  const agents = (st.agents || []).map(a => {
    const handoff = a.name === 'claude' ? ` · 本地交接${a.local_command ? '已配置' : '未配置'}` : '';
    return `${a.name} (${a.type})${handoff}`;
  }).join('、');
  appendStatusLine(status, `Agents: ${agents || '无'}`);
}

function appendStatusLine(parent, text) {
  parent.appendChild(document.createElement('br'));
  parent.appendChild(document.createTextNode(text));
}

async function saveConfig() {
  if (!currentConfig) return;
  currentConfig.allowed_workspace_roots = document.getElementById('workspace-roots').value
    .split('\n').map(s => s.trim()).filter(Boolean);
  currentConfig.rate_limit_per_minute = parseInt(document.getElementById('rate-limit').value || '0', 10);
  try {
    const r = await api('PUT', '/api/config', currentConfig);
    toast(r.restart_required ? '已保存，运行 weclaw restart 生效' : '已保存并即时生效');
  } catch (e) { toast('保存失败: ' + e.message); }
}

async function saveFeishu() {
  const name = document.getElementById('fs-name').value.trim();
  const app_id = document.getElementById('fs-appid').value.trim();
  const app_secret = document.getElementById('fs-secret').value.trim();
  try {
    await api('POST', '/api/feishu/credentials', { name, app_id, app_secret });
    document.getElementById('fs-secret').value = '';
    toast('飞书凭证已保存');
    loadStatus();
  } catch (e) { toast('保存失败: ' + e.message); }
}

async function validateFeishu() {
  const name = document.getElementById('fs-name').value.trim();
  const app_id = document.getElementById('fs-appid').value.trim();
  const app_secret = document.getElementById('fs-secret').value.trim();
  try {
    const r = await api('POST', '/api/validate/feishu', { name, app_id, app_secret });
    toast(r.ok ? '凭证有效' : '校验失败: ' + r.message);
  } catch (e) { toast('校验失败: ' + e.message); }
}

let wechatTimer = null;
let wechatQRObjectURL = null;

async function startWeChatLogin() {
  const qrBox = document.getElementById('qr');
  qrBox.textContent = '正在获取二维码…';
  try {
    const r = await api('POST', '/api/wechat/login/start', {});
    const qrResponse = await fetch(`/api/wechat/login/qr?login_id=${encodeURIComponent(r.login_id)}`, {
      headers: { 'X-WeClaw-Token': token() },
    });
    if (!qrResponse.ok) throw new Error((await qrResponse.text()) || qrResponse.status);
    if (wechatQRObjectURL) URL.revokeObjectURL(wechatQRObjectURL);
    const qrObjectURL = URL.createObjectURL(await qrResponse.blob());
    wechatQRObjectURL = qrObjectURL;
    const hint = document.createElement('div');
    hint.className = 'muted';
    hint.textContent = '用微信扫码并确认';
    const image = document.createElement('img');
    image.className = 'qr';
    image.src = qrObjectURL;
    image.alt = '二维码';
    image.onload = () => {
      URL.revokeObjectURL(qrObjectURL);
      if (wechatQRObjectURL === qrObjectURL) wechatQRObjectURL = null;
    };
    const state = document.createElement('div');
    state.id = 'wechat-state';
    state.className = 'muted';
    state.textContent = '等待扫码…';
    qrBox.replaceChildren(hint, image, state);
    if (wechatTimer) clearInterval(wechatTimer);
    wechatTimer = setInterval(() => pollWeChat(r.login_id), 2000);
  } catch (e) { qrBox.textContent = '获取二维码失败: ' + e.message; }
}

async function pollWeChat(loginID) {
  try {
    const r = await api('GET', '/api/wechat/login/status?login_id=' + encodeURIComponent(loginID));
    const el = document.getElementById('wechat-state');
    if (el) el.textContent = '状态: ' + r.status;
    if (r.status === 'confirmed') {
      clearInterval(wechatTimer);
      toast('微信账号已添加，运行 weclaw restart 接管');
      loadStatus();
    } else if (r.status === 'expired') {
      clearInterval(wechatTimer);
    }
  } catch (e) { /* ignore transient */ }
}

loadConfig().catch(e => toast('加载配置失败: ' + e.message));
loadStatus().catch(() => {});
