'use strict';

// 从 URL ?token= 读取并存入 sessionStorage，后续请求以 X-WeClaw-Token 携带。
(function initToken() {
  const u = new URL(window.location.href);
  const t = u.searchParams.get('token');
  if (t) {
    sessionStorage.setItem('weclaw_token', t);
    u.searchParams.delete('token');
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
  const daemon = st.daemon_running ? '<span class="pill ok">守护进程运行中</span>' : '<span class="pill warn">守护进程未运行</span>';
  const plats = (st.platforms || []).map(p =>
    `${p.name}${p.account_id ? ` (${p.account_id})` : ''}: ${p.enabled ? '启用' : '停用'} · 凭证${p.credentials_present ? '已配置' : '缺失'} · 白名单 ${p.allowed_users_count} 人`).join('<br>');
  const agents = (st.agents || []).map(a => `${a.name} (${a.type})`).join('、');
  document.getElementById('status').innerHTML = `${daemon}<br>${plats}<br>Agents: ${agents || '无'}`;
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

async function startWeChatLogin() {
  const qrBox = document.getElementById('qr');
  qrBox.innerHTML = '正在获取二维码…';
  try {
    const r = await api('POST', '/api/wechat/login/start', {});
    const qrSrc = `/api/wechat/login/qr?login_id=${encodeURIComponent(r.login_id)}&token=${encodeURIComponent(token())}`;
    qrBox.innerHTML = `<div class="muted">用微信扫码并确认</div>` +
      `<img class="qr" src="${qrSrc}" alt="二维码" />` +
      `<div id="wechat-state" class="muted">等待扫码…</div>`;
    if (wechatTimer) clearInterval(wechatTimer);
    wechatTimer = setInterval(() => pollWeChat(r.login_id), 2000);
  } catch (e) { qrBox.innerHTML = '获取二维码失败: ' + e.message; }
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
