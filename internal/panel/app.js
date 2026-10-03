'use strict';
/* panel.js — 控制台前端逻辑（无框架、无构建；与 /v1/* 同用 Bearer api_key） */

const $ = (id) => document.getElementById(id);
const KEY_LS = 'dev2api.key';
const THEME_LS = 'dev2api.theme';

let apiKey = localStorage.getItem(KEY_LS) || '';
let view = 'status';
let statusTimer = null, logsTimer = null, logPin = true;

// ── 主题：跟随系统 → 浅色 → 深色
const THEMES = ['system', 'light', 'dark'];
let theme = localStorage.getItem(THEME_LS) || 'system';
function applyTheme() {
  const dark = theme === 'dark' || (theme === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.dataset.theme = dark ? 'dark' : 'light';
}
function cycleTheme() {
  theme = THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length];
  localStorage.setItem(THEME_LS, theme);
  applyTheme();
  toast('主题：' + ({ system: '跟随系统', light: '浅色', dark: '深色' })[theme]);
}
window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', applyTheme);
applyTheme();

// ── Toast
function toast(msg, kind) {
  const d = document.createElement('div');
  d.className = 'tst' + (kind ? ' ' + kind : '');
  d.textContent = msg;
  $('toasts').appendChild(d);
  setTimeout(() => d.remove(), 4200);
}

// ── API（401 → 弹密钥门禁）
async function api(path) {
  const r = await fetch(path, { headers: { Authorization: 'Bearer ' + apiKey }, cache: 'no-store' });
  if (r.status === 401) { showKeyGate(true); throw new Error('unauthorized'); }
  if (!r.ok) throw new Error('HTTP ' + r.status);
  return r.json();
}

// ── 密钥门禁（api_key 为空时不会触发）
function showKeyGate(wrong) {
  $('keyErr').hidden = !wrong;
  $('keyVeil').classList.add('on');
  $('keyInput').value = '';
  $('keyInput').focus();
}
$('btnKeyOk').onclick = async () => {
  apiKey = $('keyInput').value.trim();
  try {
    await api('/panel/api/status');
    localStorage.setItem(KEY_LS, apiKey);
    $('keyVeil').classList.remove('on');
    toast('已连接', 'ok');
    switchView(view);
  } catch (e) {
    $('keyErr').hidden = false;
  }
};
$('keyInput').addEventListener('keydown', (e) => { if (e.key === 'Enter') $('btnKeyOk').click(); });
$('btnKey').onclick = () => { localStorage.removeItem(KEY_LS); apiKey = ''; showKeyGate(false); };

// ── 工具
function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
}
function tag(text, kind) { return el('span', 'tag ' + kind, text); }
function fmtUptime(sec) {
  const s = Math.max(0, Math.round(sec));
  if (s < 3600) return Math.floor(s / 60) + 'm ' + (s % 60) + 's';
  return Math.floor(s / 3600) + 'h ' + Math.floor((s % 3600) / 60) + 'm';
}
function setConn(ok, text) {
  $('navPulse').className = 'pulse' + (ok ? '' : ' bad');
  $('navState').textContent = text;
}

// ── 视图切换
const TITLES = { status: '概览', models: '模型', logs: '日志' };
function switchView(v) {
  view = v;
  document.querySelectorAll('.view').forEach((s) => { s.hidden = s.id !== 'view-' + v; });
  document.querySelectorAll('.nav a').forEach((a) => a.classList.toggle('on', a.dataset.view === v));
  $('ttl').textContent = TITLES[v] || v;
  clearInterval(statusTimer);
  clearInterval(logsTimer);
  loadStatus(); // 页脚状态/版本/密钥按钮随任意视图初始化（深链 #models/#logs 不再停在「连接中」）
  if (v === 'status') { statusTimer = setInterval(loadStatus, 5000); }
  if (v === 'models') loadModels();
  if (v === 'logs') { loadLogs(); logsTimer = setInterval(loadLogs, 3000); }
  closeNav();
}
document.querySelectorAll('.nav a').forEach((a) => {
  a.addEventListener('click', (e) => { e.preventDefault(); switchView(a.dataset.view); });
});

// 移动端抽屉
function openNav() { $('nav').classList.add('open'); $('navScrim').classList.add('on'); document.body.classList.add('nav-open'); }
function closeNav() { $('nav').classList.remove('open'); $('navScrim').classList.remove('on'); document.body.classList.remove('nav-open'); }
$('btnNav').onclick = () => { $('nav').classList.contains('open') ? closeNav() : openNav(); };
$('navScrim').onclick = closeNav;
$('btnTheme').onclick = cycleTheme;

// ── 概览
async function loadStatus() {
  try {
    const st = await api('/panel/api/status');
    renderStatus(st);
    setConn(true, '服务正常');
  } catch (e) {
    setConn(false, e.message === 'unauthorized' ? '需要密钥' : '无法连接');
  }
}

function renderStatus(st) {
  const acc = st.account || {};
  const ka = st.keepalive || {};
  $('navVer').textContent = 'v' + (st.version || '-');
  $('navSub').textContent = st.listen || '控制台';
  $('subMeta').textContent = st.listen || '-';
  $('btnKey').hidden = !st.auth_required;
  $('sUptime').textContent = fmtUptime(st.uptime_seconds || 0);
  $('sAccount').textContent = acc.name || '未登录';

  const days = acc.jwt_days_left;
  $('sJwt').textContent = days == null ? '—' : Number(days).toFixed(1);
  $('cJwt').className = 'stat' + (days == null ? '' : days > 7 ? ' good' : days > 2 ? ' warn' : ' bad');
  $('sKeepalive').textContent = ka.hours > 0 ? String(ka.hours) : '关闭';
  $('stNote').textContent = '更新于 ' + new Date().toLocaleTimeString();

  const body = $('stBody');
  body.textContent = '';
  const rows = [
    ['服务地址', st.listen],
    ['上游', st.upstream],
    ['配置文件', st.config_path],
    ['账号 ID', acc.id || '—'],
    ['默认模型', st.model_default],
    ['思维链剥离', (st.thinking_models || []).join('、') || '（无）'],
  ];
  rows.forEach(([k, v]) => {
    const tr = el('tr');
    tr.append(el('td', null, k), el('td', 'num', v == null ? '—' : String(v)));
    body.append(tr);
  });
  const tr = el('tr');
  const td = el('td');
  const lr = ka.last_refresh;
  if (lr) {
    td.append(el('span', 'num', lr.at + '  '), tag(lr.ok ? '成功' : '失败', lr.ok ? 'ok' : 'bad'));
  } else {
    td.append(el('span', null, '尚未刷新（启动自检通过）'));
  }
  tr.append(el('td', null, '最近 token 刷新'), td);
  body.append(tr);
}

// ── 模型
async function loadModels() {
  $('mdNote').textContent = '查询中…';
  try {
    const d = await api('/panel/api/models');
    $('mdNote').textContent = '更新于 ' + d.at + ' · 共 ' + d.count + ' 个';
    const body = $('mdBody');
    body.textContent = '';
    (d.models || []).forEach((m) => {
      const tr = el('tr');
      tr.append(el('td', 'num', m.id));
      tr.append(el('td', null, (m.group_cn || m.group || '') + (m.group ? '（' + m.group + '）' : '')));
      tr.append(el('td', 'num', String(m.context_window || '—')));
      tr.append(el('td', 'num', String(m.output || '—')));
      const cTh = el('td');
      cTh.append(tag(m.thinking === 'on' ? '开启' : m.thinking === 'configurable' ? '可配置' : (m.thinking || '—'), m.thinking === 'on' ? 'warn' : 'mute'));
      tr.append(cTh);
      const cTl = el('td');
      cTl.append(m.tools === 'tool_calls' ? tag('支持', 'ok') : tag('不支持', 'mute'));
      tr.append(cTl);
      const cSt = el('td');
      cSt.append(m.thinking_stripped ? tag('已剥离', 'ok') : tag('透传', 'mute'));
      tr.append(cSt);
      body.append(tr);
    });
    if (!(d.models || []).length) {
      const tr = el('tr');
      const td = el('td', 'empty', '上游未返回模型');
      td.colSpan = 7;
      tr.append(td);
      body.append(tr);
    }
  } catch (e) {
    $('mdNote').textContent = '查询失败：' + e.message;
  }
}
$('btnModels').onclick = loadModels;
$('btnRefresh').onclick = () => { if (view === 'models') loadModels(); else if (view === 'logs') loadLogs(); else loadStatus(); };

// ── 日志
async function loadLogs() {
  try {
    const d = await api('/panel/api/logs?limit=300');
    const box = $('logBox');
    const atBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 30;
    box.textContent = '';
    (d.lines || []).forEach((l) => {
      const cls = 'ln' + (l.level === 'ERROR' ? ' e' : l.level === 'WARNING' ? ' w' : '');
      box.append(el('span', cls, '[' + l.time + '][' + l.level + '] ' + l.msg + '\n'));
    });
    $('logNote').textContent = (d.lines || []).length + ' 行';
    if (logPin && atBottom) box.scrollTop = box.scrollHeight;
  } catch (e) {
    $('logNote').textContent = '读取失败：' + e.message;
  }
}
$('btnLogs').onclick = loadLogs;
$('btnLogPin').onclick = () => { logPin = !logPin; $('btnLogPin').textContent = '自动滚动：' + (logPin ? '开' : '关'); };

// ── 启动
const initView = (location.hash || '').replace('#', '');
switchView(TITLES[initView] ? initView : 'status');
