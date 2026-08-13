import { renderChart } from './chart.js';
import { createEditor } from './editor.js';
import { icons } from './icons.js';
import { t, lang, setLang, applyDirection } from './i18n.js';
import {
  coreFormat, cpuSpeedFormat, escapeHtml, formatClock, formatDateTime, formatSecond,
  mean, peak, percentOf, relativeTime, sizeCompact, sizeFormat, speedFormat, speedFormatShort, usageColor,
  CRIT_PERCENT, WARN_PERCENT,
} from './fmt.js';

const FLEET_WINDOW = 90;

const state = {
  payload: null,
  lastMessage: 0,
  route: { name: 'overview', id: null },
  mounted: null,
  showIp: false,
  fleet: { cpu: [], mem: [], disk: [], up: [], down: [], labels: [] },
  series: {},
  detail: { range: 'live', points: null, loading: false },
};

const view = document.getElementById('view');
const toastHost = document.getElementById('toasts');

/* ---------- infrastructure ---------- */

async function api(path, options = {}) {
  const response = await fetch(path, {
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });
  if (response.status === 401) {
    window.location.href = '/login';
    throw new Error('unauthorized');
  }
  const data = await response.json().catch(() => null);
  if (!response.ok) throw new Error((data && data.error) || response.statusText);
  return data;
}

function toast(message, kind = '') {
  const node = document.createElement('div');
  node.className = `toast ${kind}`;
  node.textContent = message;
  toastHost.appendChild(node);
  setTimeout(() => node.remove(), 3200);
}

function ref(name, root = view) {
  return root.querySelector(`[data-ref="${name}"]`);
}

function setText(name, value, root = view) {
  const node = ref(name, root);
  if (node) node.textContent = value;
}

function applyTheme(dark) {
  document.documentElement.classList.toggle('dark', dark);
  localStorage.setItem('srvmon-dark', dark ? 'true' : 'false');
  const button = document.getElementById('themeToggle');
  if (button) button.innerHTML = dark ? icons.sun : icons.moon;
}

function isDark() {
  return document.documentElement.classList.contains('dark');
}

/* ---------- routing ---------- */

function parseRoute() {
  const hash = window.location.hash.replace(/^#\/?/, '');
  const [name, id, section] = hash.split('/');
  if (name === 'server' && id) return { name: 'server', id: Number(id), section: section || 'metrics' };
  if (['servers', 'alerts', 'settings', 'notes'].includes(name)) return { name, id: null };
  return { name: 'overview', id: null };
}

function navigate() {
  state.route = parseRoute();
  state.detail = { range: 'live', points: null, loading: false };
  document.querySelectorAll('.nav-item[data-route]').forEach((item) => {
    const active = item.dataset.route === state.route.name ||
      (state.route.name === 'server' && item.dataset.route === 'overview');
    item.classList.toggle('active', active);
  });
  state.mounted = null;
  render();
}

function render() {
  const route = state.route;
  const needsMount = !state.mounted ||
    state.mounted.name !== route.name ||
    state.mounted.id !== route.id ||
    state.mounted.section !== route.section;

  if (needsMount) {
    state.mounted = { name: route.name, id: route.id, section: route.section };
    switch (route.name) {
      case 'server': route.section === 'notes' ? mountServerNotes() : mountDetail(); break;
      case 'servers': mountServers(); break;
      case 'alerts': mountAlerts(); break;
      case 'notes': mountNoteSearch(); break;
      case 'settings': mountSettings(); break;
      default: mountOverview();
    }
  }
  if (route.name === 'overview') updateOverview();
  if (route.name === 'server' && route.section !== 'notes') updateDetail();
}

// The tab strip is shared by both server sections so the header does not jump
// when you switch between them.
function serverTabsMarkup(id, active) {
  const tab = (key, label) =>
    `<a class="seg-link${active === key ? ' active' : ''}" href="#/server/${id}${key === 'metrics' ? '' : '/' + key}">${label}</a>`;
  return `<div class="seg">${tab('metrics', t('metricsTab'))}${tab('notes', t('notesTab'))}</div>`;
}

/* ---------- live data ---------- */

// Only the first payload carries each server's rolling window; every later tick
// is one fresh sample that gets appended here. Keeping the series client-side is
// what lets the stream stay small no matter how many servers report.
function trackSeries(payload) {
  const seen = new Set();

  for (const server of payload.servers || []) {
    seen.add(server.id);
    if (!state.series[server.id]) state.series[server.id] = server.live ? server.live.slice() : [];
    if (!server.status || !server.online) continue;

    const status = server.status;
    const series = state.series[server.id];
    const last = series[series.length - 1];
    if (last && last.t === payload.t) continue;

    series.push({
      t: payload.t,
      cpu: status.cpu || 0,
      mem: percentOf(status.mem),
      swap: percentOf(status.swap),
      disk: percentOf(status.disk),
      netUp: status.netIO.up || 0,
      netDown: status.netIO.down || 0,
      tcp: status.tcpCount || 0,
      udp: status.udpCount || 0,
      load1: (status.loads && status.loads[0]) || 0,
    });
    if (series.length > FLEET_WINDOW) state.series[server.id] = series.slice(-FLEET_WINDOW);
  }

  for (const id of Object.keys(state.series)) {
    if (!seen.has(Number(id))) delete state.series[id];
  }
}

function seriesFor(serverId) {
  return state.series[serverId] || [];
}

function applyPayload(payload) {
  state.payload = payload;
  state.lastMessage = Date.now();
  trackSeries(payload);

  const version = document.getElementById('sideVersion');
  if (version && payload.version) version.textContent = `v${payload.version}`;

  const summary = payload.summary || {};
  const fleet = state.fleet;
  fleet.cpu.push(summary.cpuAvg || 0);
  fleet.mem.push(summary.memAvg || 0);
  fleet.disk.push(summary.diskAvg || 0);
  fleet.up.push(summary.netUp || 0);
  fleet.down.push(summary.netDown || 0);
  fleet.labels.push(formatClock(payload.t));
  for (const key of Object.keys(fleet)) {
    if (fleet[key].length > FLEET_WINDOW) fleet[key] = fleet[key].slice(-FLEET_WINDOW);
  }

  render();
}

function connectStream() {
  const stream = new EventSource('/api/stream');
  stream.onmessage = (event) => {
    try {
      applyPayload(JSON.parse(event.data));
    } catch {
      /* a truncated frame is dropped; the next tick replaces it */
    }
  };
  stream.onerror = () => updateLivePill();

  // A proxy that buffers responses silently breaks SSE, so fall back to
  // polling whenever the stream goes quiet for longer than three ticks.
  setInterval(async () => {
    const interval = (state.payload && state.payload.pushInterval) || 2;
    if (Date.now() - state.lastMessage < interval * 3000) return;
    updateLivePill();
    try {
      applyPayload(await api('/api/dashboard'));
    } catch {
      /* offline; the next attempt retries */
    }
  }, 5000);
}

function isStalled() {
  const interval = (state.payload && state.payload.pushInterval) || 2;
  return Date.now() - state.lastMessage > interval * 3000;
}

function updateLivePill() {
  const pill = ref('livePill');
  if (!pill) return;
  const stalled = isStalled();
  pill.dataset.state = stalled ? 'stalled' : 'running';
  pill.style.color = stalled ? 'var(--warn)' : 'var(--success)';
  setText('liveText', stalled ? t('stalled') : t('live'));
}

function serverById(id) {
  const servers = (state.payload && state.payload.servers) || [];
  return servers.find((server) => server.id === id) || null;
}

/* ---------- shared markup ---------- */

function tileMarkup(id, icon, label) {
  return `<div class="card hoverable ov-tile">
    <div class="ov-tile-head"><span class="ov-tile-icon">${icon}</span><span class="ov-kicker">${label}</span></div>
    <div class="ov-tile-value">
      <span class="ov-tile-number" data-ref="${id}Value">0</span>
      <span class="ov-tile-unit" data-ref="${id}Unit">%</span>
    </div>
    <div class="ov-tile-detail" data-ref="${id}Detail"></div>
    <div class="ov-tile-foot"><span data-ref="${id}Left"></span><span data-ref="${id}Right"></span></div>
    <div class="ov-tile-chart" data-ref="${id}Chart"></div>
  </div>`;
}

function updateTile(id, options) {
  setText(`${id}Value`, options.value);
  setText(`${id}Unit`, options.unit ?? '%');
  setText(`${id}Detail`, options.detail ?? '');
  setText(`${id}Left`, options.left ?? '');
  setText(`${id}Right`, options.right ?? '');

  const chart = ref(`${id}Chart`);
  if (!chart) return;
  renderChart(chart, {
    height: window.innerWidth < 768 ? 48 : 62,
    yMax: options.yMax === undefined ? 100 : options.yMax,
    format: options.format || ((v) => `${v.toFixed(0)}%`),
    series: options.series,
    refLines: options.refLines || [],
  });
}

function healthLine(items, okKey = 'allHealthy') {
  const list = (subset) => subset.map((item) => `${item.name} ${item.value.toFixed(0)}%`).join(', ');
  const critical = items.filter((item) => item.value >= CRIT_PERCENT);
  if (critical.length) return { text: t('healthCritical', { list: list(critical) }), color: 'var(--crit)' };
  const warm = items.filter((item) => item.value >= WARN_PERCENT);
  if (warm.length) return { text: t('healthWarm', { list: list(warm) }), color: 'var(--warn)' };
  return { text: t(okKey), color: 'var(--text-3)' };
}

/* ---------- overview ---------- */

function mountOverview() {
  view.innerHTML = `<div class="ov-page">
    <div class="ov-bar">
      <h1 class="ov-title">${t('fleet')}</h1>
      <span class="ov-state" data-ref="livePill" data-state="running" style="color:var(--success)">
        <span class="ov-state-dot"></span><span data-ref="liveText">${t('live')}</span>
      </span>
      <span class="ov-state" data-ref="countPill"></span>
      <div class="ov-bar-actions">
        <span class="ov-sub" data-ref="updatedAt"></span>
      </div>
    </div>
    <div class="ov-health" data-ref="health"><span class="ov-health-mark"></span><span data-ref="healthText"></span></div>
    <hr class="ov-rule">
    <div class="ov-vitals">
      ${tileMarkup('fcpu', icons.cpu, t('avgCpu'))}
      ${tileMarkup('fmem', icons.memory, t('avgMemory'))}
      ${tileMarkup('fdisk', icons.disk, t('avgStorage'))}
      ${tileMarkup('fnet', icons.network, t('throughput'))}
    </div>
    <div class="srv-groups" data-ref="grid"></div>
  </div>`;
}

function updateOverview() {
  if (!state.payload) return;
  const { summary, servers } = state.payload;

  updateLivePill();
  setText('countPill', t('onlineOf', { online: summary.online, total: summary.total }));
  setText('updatedAt', t('lastUpdate', { value: formatClock(state.payload.t) }));

  const health = healthLine([
    { name: t('cpu'), value: summary.cpuAvg || 0 },
    { name: t('memory'), value: summary.memAvg || 0 },
    { name: t('storage'), value: summary.diskAvg || 0 },
  ]);
  const healthNode = ref('health');
  if (healthNode) healthNode.style.color = health.color;
  setText('healthText', health.text);

  const fleet = state.fleet;
  updateTile('fcpu', {
    value: (summary.cpuAvg || 0).toFixed(1),
    detail: t('onlineOf', { online: summary.online, total: summary.total }),
    left: `${t('avg')} ${mean(fleet.cpu).toFixed(0)}%`,
    right: `${t('peak')} ${peak(fleet.cpu).toFixed(0)}%`,
    series: [{ data: fleet.cpu, color: usageColor(summary.cpuAvg || 0), name: t('cpu') }],
    refLines: fleet.cpu.length > 1 ? [{ y: mean(fleet.cpu), color: 'var(--text-3)' }] : [],
  });
  updateTile('fmem', {
    value: (summary.memAvg || 0).toFixed(1),
    detail: t('avgMemory'),
    left: `${t('avg')} ${mean(fleet.mem).toFixed(0)}%`,
    right: `${t('peak')} ${peak(fleet.mem).toFixed(0)}%`,
    series: [{ data: fleet.mem, color: usageColor(summary.memAvg || 0), name: t('memory') }],
  });
  updateTile('fdisk', {
    value: (summary.diskAvg || 0).toFixed(1),
    detail: t('avgStorage'),
    left: `${t('avg')} ${mean(fleet.disk).toFixed(0)}%`,
    right: `${t('peak')} ${peak(fleet.disk).toFixed(0)}%`,
    series: [{ data: fleet.disk, color: usageColor(summary.diskAvg || 0), name: t('storage') }],
  });
  updateTile('fnet', {
    value: sizeFormat(summary.netDown || 0).split(' ')[0],
    unit: `${sizeFormat(summary.netDown || 0).split(' ')[1]}/s ↓`,
    detail: `↑ ${speedFormat(summary.netUp || 0)}`,
    left: `TCP ${summary.tcpCount || 0}`,
    right: `UDP ${summary.udpCount || 0}`,
    yMax: null,
    format: speedFormat,
    series: [
      { data: fleet.up, color: 'var(--up)', name: t('upload') },
      { data: fleet.down, color: 'var(--down)', name: t('download') },
    ],
  });

  syncServerCards(servers);
}

function serverCardMarkup(server) {
  return `<div class="card hoverable srv-card" data-card="${server.id}">
    <div class="srv-head">
      <span class="dot" data-ref="dot"></span>
      <span class="srv-name">${escapeHtml(server.name)}</span>
      ${server.tag ? `<span class="srv-tag">${escapeHtml(server.tag)}</span>` : ''}
      <span class="srv-uptime" data-ref="uptime"></span>
    </div>
    <div class="srv-meta"><span data-ref="os"></span><span data-ref="ip"></span></div>
    <div class="srv-bars">
      ${meterMarkup('cpu', t('cpu'))}
      ${meterMarkup('mem', t('memory'))}
      ${meterMarkup('disk', t('storage'))}
    </div>
    <div class="srv-net">
      <span class="srv-net-item" style="color:var(--up)">${icons.up}<b data-ref="up">—</b></span>
      <span class="srv-net-item" style="color:var(--down)">${icons.down}<b data-ref="down">—</b></span>
      <span class="srv-net-conn" data-ref="conn"></span>
    </div>
    <div class="srv-chart" data-ref="chart"></div>
  </div>`;
}

function meterMarkup(key, label) {
  return `<div class="srv-meter">
    <div class="srv-meter-top"><span>${label}</span><b data-ref="${key}Pct">—</b></div>
    <div class="meter"><span data-ref="${key}Bar" style="width:0%"></span></div>
    <div class="srv-meter-sub" data-ref="${key}Sub"></div>
  </div>`;
}

// Servers are grouped by their tag, untagged ones last. A fleet with no tags at
// all renders as one plain grid — a lone "Ungrouped" heading is just noise.
function groupsOf(servers) {
  const byTag = new Map();
  for (const server of servers) {
    const tag = (server.tag || '').trim();
    if (!byTag.has(tag)) byTag.set(tag, []);
    byTag.get(tag).push(server);
  }
  return [...byTag.entries()].sort((a, b) => {
    if (a[0] === b[0]) return 0;
    if (!a[0]) return 1;
    if (!b[0]) return -1;
    return a[0].localeCompare(b[0]);
  });
}

function groupMarkup(tag, members, showHeading) {
  const cards = `<div class="srv-grid">${members.map(serverCardMarkup).join('')}</div>`;
  if (!showHeading) return cards;
  return `<section class="srv-group">
    <div class="srv-group-head">
      <span class="srv-group-name">${escapeHtml(tag || t('ungrouped'))}</span>
      <span class="srv-group-rule"></span>
      <span class="srv-group-meta" data-ref="groupMeta-${escapeHtml(tag)}"></span>
    </div>
    ${cards}
  </section>`;
}

function syncServerCards(servers) {
  const host = ref('grid');
  if (!host) return;

  const groups = groupsOf(servers);
  const showHeadings = groups.length > 1 || (groups.length === 1 && groups[0][0] !== '');
  // The signature carries tags too, so moving a server between groups rebuilds.
  const wanted = servers.map((server) => `${server.id}:${server.tag || ''}`).join(',');

  if (host.dataset.keys !== wanted) {
    host.dataset.keys = wanted;
    host.innerHTML = servers.length
      ? groups.map(([tag, members]) => groupMarkup(tag, members, showHeadings)).join('')
      : `<div class="card empty">${t('noServers')}</div>`;
    host.querySelectorAll('[data-card]').forEach((card) => {
      card.addEventListener('click', () => {
        window.location.hash = `#/server/${card.dataset.card}`;
      });
    });
  }

  for (const [tag, members] of groups) {
    if (showHeadings) {
      const online = members.filter((server) => server.online).length;
      const up = members.reduce((sum, s) => sum + (s.status && s.online ? s.status.netIO.up : 0), 0);
      const down = members.reduce((sum, s) => sum + (s.status && s.online ? s.status.netIO.down : 0), 0);
      const meta = ref(`groupMeta-${tag}`, host);
      if (meta) {
        meta.textContent = `${t('onlineOf', { online, total: members.length })} · ↑ ${speedFormat(up)} ↓ ${speedFormat(down)}`;
        meta.style.color = online < members.length ? 'var(--crit)' : '';
      }
    }
    for (const server of members) {
      const card = host.querySelector(`[data-card="${server.id}"]`);
      if (card) updateServerCard(card, server);
    }
  }
}

function updateServerCard(card, server) {
  const status = server.status;
  card.classList.toggle('srv-offline', !server.online);

  const dot = ref('dot', card);
  if (dot) dot.style.color = server.online ? 'var(--success)' : 'var(--crit)';

  setText('uptime', server.online && status ? formatSecond(status.uptime) : t('offline'), card);
  setText('os', status ? `${status.os || ''} ${status.arch || ''}`.trim() : (server.os || '—'), card);
  setText('ip', server.ipv4 || '', card);

  const cpu = status ? status.cpu : 0;
  const memPercent = status ? percentOf(status.mem) : 0;
  const diskPercent = status ? percentOf(status.disk) : 0;

  applyMeter(card, 'cpu', cpu, status ? `${status.cpuCores}C / ${status.logicalPro}T` : '');
  applyMeter(card, 'mem', memPercent,
    status ? `${sizeCompact(status.mem.current)} / ${sizeCompact(status.mem.total)}` : '');
  applyMeter(card, 'disk', diskPercent,
    status ? `${sizeCompact(status.disk.current)} / ${sizeCompact(status.disk.total)}` : '');

  setText('up', status ? speedFormat(status.netIO.up) : '—', card);
  setText('down', status ? speedFormat(status.netIO.down) : '—', card);
  setText('conn', status ? `TCP ${status.tcpCount} · UDP ${status.udpCount}` : relativeTime(server.lastSeen, t), card);

  const chart = ref('chart', card);
  if (chart) {
    const live = seriesFor(server.id);
    renderChart(chart, {
      height: 58,
      yMax: null,
      format: speedFormat,
      series: [
        { data: live.map((point) => point.netUp), color: 'var(--up)', name: t('upload') },
        { data: live.map((point) => point.netDown), color: 'var(--down)', name: t('download'), fill: 0.16 },
      ],
    });
  }
}

function applyMeter(card, key, percent, sub) {
  setText(`${key}Pct`, `${percent.toFixed(1)}%`, card);
  setText(`${key}Sub`, sub, card);
  const bar = ref(`${key}Bar`, card);
  if (!bar) return;
  bar.style.width = `${Math.min(100, Math.max(0, percent))}%`;
  bar.style.background = usageColor(percent);
}

/* ---------- server detail ---------- */

const RANGES = ['live', '1h', '6h', '24h', '7d'];

function mountDetail() {
  view.innerHTML = `<div class="ov-page">
    <div class="ov-bar">
      <button class="btn ghost small" data-ref="back">${icons.back} ${t('backToOverview')}</button>
      <h1 class="ov-title" data-ref="title">—</h1>
      <button class="btn ghost small" data-ref="rename" title="${t('edit')}" aria-label="${t('edit')}">${icons.pencil}</button>
      <span class="ov-state" data-ref="statePill"><span class="ov-state-dot"></span><span data-ref="stateText"></span></span>
      <div class="ov-bar-actions">
        ${serverTabsMarkup(state.route.id, 'metrics')}
        <div class="seg" data-ref="ranges">
          ${RANGES.map((range) => `<button data-range="${range}"${range === 'live' ? ' class="active"' : ''}>${range === 'live' ? t('rangeLive') : range}</button>`).join('')}
        </div>
      </div>
    </div>
    <div class="ov-health" data-ref="health"><span class="ov-health-mark"></span><span data-ref="healthText"></span></div>
    <hr class="ov-rule">
    <div class="ov-vitals">
      ${tileMarkup('cpu', icons.cpu, t('cpu'))}
      ${tileMarkup('mem', icons.memory, t('memory'))}
      ${tileMarkup('swap', icons.swap, t('swap'))}
      ${tileMarkup('disk', icons.disk, t('storage'))}
    </div>
    <div class="ov-mid">
      <div class="card hoverable">
        <div class="ov-wide-head">
          <div>
            <div class="ov-kicker">${t('overallSpeed')}</div>
            <div class="ov-sub" data-ref="netSub"></div>
          </div>
          <div class="ov-wide-legend">
            <div class="ov-legend-label" style="flex-direction:column;align-items:flex-end">
              <span>${icons.up} ${t('upload')}</span>
              <span class="ov-legend-num" data-ref="upNow">—</span>
            </div>
            <div class="ov-legend-label" style="flex-direction:column;align-items:flex-end">
              <span>${icons.down} ${t('download')}</span>
              <span class="ov-legend-num" data-ref="downNow">—</span>
            </div>
          </div>
        </div>
        <div class="ov-wide-chart" data-ref="netChart"></div>
        <div class="ov-wide-foot">
          <div><div class="ov-kicker">${t('sent')}</div><div class="ov-foot-value" data-ref="sent">—</div></div>
          <span class="ov-foot-sep"></span>
          <div><div class="ov-kicker">${t('received')}</div><div class="ov-foot-value" data-ref="recv">—</div></div>
          <span class="ov-foot-sep"></span>
          <div><div class="ov-kicker">${t('avgWindow')}</div><div class="ov-foot-value" data-ref="netAvg">—</div></div>
        </div>
      </div>
      <div class="card hoverable">
        <div class="ov-wide-head">
          <div>
            <div class="ov-kicker">${t('connections')}</div>
            <div class="ov-conn-total">
              <span class="ov-tile-number" data-ref="connTotal">0</span>
              <span class="ov-tile-unit">TCP + UDP</span>
            </div>
          </div>
        </div>
        <div class="ov-conn-legend">
          <div>
            <div class="ov-legend-label"><span class="ov-swatch" style="background:var(--primary)"></span>TCP</div>
            <div class="ov-legend-num" data-ref="tcpNow">0</div>
          </div>
          <div>
            <div class="ov-legend-label"><span class="ov-swatch" style="background:var(--text-3)"></span>UDP</div>
            <div class="ov-legend-num" data-ref="udpNow">0</div>
          </div>
        </div>
        <div class="ov-wide-chart" data-ref="connChart"></div>
        <div style="height:var(--ov-pad)"></div>
      </div>
    </div>
    <div class="card">
      <div class="ov-strip-grid">
        <div class="ov-strip-cell">
          <div class="ov-kicker ov-kicker-icon">${icons.clock}${t('uptime')}</div>
          <div class="ov-strip-value" data-ref="uptime">—</div>
          <div class="ov-strip-sub">${t('lastSeen')}</div>
          <div class="ov-strip-value" style="font-size:14px" data-ref="lastSeen">—</div>
        </div>
        <div class="ov-strip-cell">
          <div class="ov-kicker ov-kicker-icon">${icons.cpuChip}${t('system')}</div>
          <div class="ov-strip-split">
            <div>
              <div class="ov-strip-sub">${t('load')}</div>
              <div class="ov-strip-value" data-ref="loads">—</div>
            </div>
            <span class="ov-strip-split-sep"></span>
            <div>
              <div class="ov-strip-sub">${t('processes')}</div>
              <div class="ov-strip-value" data-ref="procs">—</div>
            </div>
          </div>
        </div>
        <div class="ov-strip-cell">
          <div class="ov-kicker ov-kicker-icon">${icons.server}${t('cpu')}</div>
          <div class="ov-strip-sub" data-ref="cpuModel">—</div>
          <div class="ov-strip-value" style="font-size:15px" data-ref="cpuSpec">—</div>
        </div>
        <div class="ov-strip-cell">
          <div class="ov-kicker ov-kicker-icon">
            ${icons.globe}${t('ipAddresses')}
            <button class="btn ghost small" data-ref="ipToggle" title="${t('toggleIp')}" style="margin-inline-start:auto">${icons.eyeOff}</button>
          </div>
          <div data-ref="ipBox" class="ov-ip ip-hidden">
            <div class="ov-mono" data-ref="ipv4">—</div>
            <div class="ov-mono ov-ip-v6" data-ref="ipv6"></div>
          </div>
        </div>
      </div>
    </div>
  </div>`;

  ref('back').addEventListener('click', () => { window.location.hash = '#/'; });
  ref('rename').addEventListener('click', () => {
    const server = serverById(state.route.id);
    // The stream repaints the name within a tick anyway; refetching just makes
    // it land the instant the dialog closes.
    if (server) editServerDialog(server, () => api('/api/dashboard').then(applyPayload).catch(() => undefined));
  });
  ref('ipToggle').addEventListener('click', () => {
    state.showIp = !state.showIp;
    ref('ipBox').classList.toggle('ip-hidden', !state.showIp);
    ref('ipToggle').innerHTML = state.showIp ? icons.eye : icons.eyeOff;
  });
  ref('ranges').addEventListener('click', (event) => {
    const button = event.target.closest('button[data-range]');
    if (!button) return;
    state.detail.range = button.dataset.range;
    ref('ranges').querySelectorAll('button').forEach((item) => {
      item.classList.toggle('active', item.dataset.range === state.detail.range);
    });
    loadDetailHistory();
  });
}

async function loadDetailHistory() {
  const range = state.detail.range;
  if (range === 'live') {
    state.detail.points = null;
    updateDetail();
    return;
  }
  state.detail.loading = true;
  try {
    const data = await api(`/api/servers/${state.route.id}/history?range=${range}`);
    state.detail.points = data.points || [];
  } catch (error) {
    toast(error.message, 'error');
    state.detail.points = [];
  } finally {
    state.detail.loading = false;
    updateDetail();
  }
}

function updateDetail() {
  const server = serverById(state.route.id);
  if (!server) return;
  const status = server.status;

  setText('title', server.name);
  const pill = ref('statePill');
  if (pill) {
    pill.dataset.state = server.online ? 'running' : 'stop';
    pill.style.color = server.online ? 'var(--success)' : 'var(--crit)';
  }
  setText('stateText', server.online ? t('online') : `${t('offline')} · ${relativeTime(server.lastSeen, t)}`);

  const points = state.detail.points || seriesFor(server.id);
  const labels = points.map((point) => formatClock(point.t));
  const series = (key) => points.map((point) => point[key] || 0);

  const cpuPercent = status ? status.cpu : 0;
  const memPercent = status ? percentOf(status.mem) : 0;
  const swapPercent = status ? percentOf(status.swap) : 0;
  const diskPercent = status ? percentOf(status.disk) : 0;

  const health = healthLine([
    { name: t('cpu'), value: cpuPercent },
    { name: t('memory'), value: memPercent },
    { name: t('swap'), value: swapPercent },
    { name: t('storage'), value: diskPercent },
  ], 'serverHealthy');
  const healthNode = ref('health');
  if (healthNode) healthNode.style.color = health.color;
  setText('healthText', health.text);

  const cpuSeries = series('cpu');
  updateTile('cpu', {
    value: cpuPercent.toFixed(1),
    detail: status ? `${coreFormat(status.cpuCores)} / ${status.logicalPro}T · ${cpuSpeedFormat(status.cpuSpeedMhz)}` : '—',
    left: `${t('avg')} ${mean(cpuSeries).toFixed(0)}%`,
    right: `${t('peak')} ${peak(cpuSeries).toFixed(0)}%`,
    series: [{ data: cpuSeries, color: usageColor(cpuPercent), name: t('cpu') }],
    refLines: cpuSeries.length > 1 ? [{ y: mean(cpuSeries), color: 'var(--text-3)' }] : [],
  });

  const memSeries = series('mem');
  updateTile('mem', {
    value: memPercent.toFixed(1),
    detail: status ? `${sizeFormat(status.mem.current)} / ${sizeFormat(status.mem.total)}` : '—',
    left: `${t('avg')} ${mean(memSeries).toFixed(0)}%`,
    right: `${t('peak')} ${peak(memSeries).toFixed(0)}%`,
    series: [{ data: memSeries, color: usageColor(memPercent), name: t('memory') }],
  });

  const swapSeries = series('swap');
  updateTile('swap', {
    value: swapPercent.toFixed(1),
    detail: status ? `${sizeFormat(status.swap.current)} / ${sizeFormat(status.swap.total)}` : '—',
    left: `${t('avg')} ${mean(swapSeries).toFixed(1)}%`,
    right: `${t('peak')} ${peak(swapSeries).toFixed(0)}%`,
    series: [{ data: swapSeries, color: usageColor(swapPercent), name: t('swap') }],
  });

  const diskSeries = series('disk');
  const freeDisk = status ? Math.max(0, status.disk.total - status.disk.current) : 0;
  updateTile('disk', {
    value: diskPercent.toFixed(1),
    detail: status ? `${sizeFormat(status.disk.current)} / ${sizeFormat(status.disk.total)}` : '—',
    left: `${t('free')} ${sizeFormat(freeDisk)}`,
    right: `${t('avg')} ${mean(diskSeries).toFixed(1)}%`,
    series: [{ data: diskSeries, color: usageColor(diskPercent), name: t('storage') }],
  });

  const upSeries = series('netUp');
  const downSeries = series('netDown');
  setText('netSub', `${t('throughputSub')} · ${t('peak')} ${speedFormat(peak(downSeries))}`);
  setText('upNow', status ? speedFormat(status.netIO.up) : '—');
  setText('downNow', status ? speedFormat(status.netIO.down) : '—');
  setText('sent', status ? sizeFormat(status.netTraffic.sent) : '—');
  setText('recv', status ? sizeFormat(status.netTraffic.recv) : '—');
  setText('netAvg', `↑ ${speedFormat(mean(upSeries))}  ↓ ${speedFormat(mean(downSeries))}`);

  const netChart = ref('netChart');
  if (netChart) {
    renderChart(netChart, {
      height: window.innerWidth < 768 ? 140 : 186,
      yMax: null,
      grid: true,
      axis: true,
      tooltip: true,
      labels,
      format: speedFormat,
      axisFormat: speedFormatShort,
      series: [
        { data: upSeries, color: 'var(--up)', name: t('upload'), width: 1.75 },
        { data: downSeries, color: 'var(--down)', name: t('download'), width: 1.75 },
      ],
    });
  }

  const tcpSeries = series('tcp');
  const udpSeries = series('udp');
  setText('connTotal', status ? String(status.tcpCount + status.udpCount) : '0');
  setText('tcpNow', status ? String(status.tcpCount) : '0');
  setText('udpNow', status ? String(status.udpCount) : '0');

  const connChart = ref('connChart');
  if (connChart) {
    renderChart(connChart, {
      height: window.innerWidth < 768 ? 120 : 150,
      yMax: null,
      grid: true,
      tooltip: true,
      labels,
      format: (v) => v.toFixed(0),
      series: [
        { data: tcpSeries, color: 'var(--primary)', name: 'TCP' },
        { data: udpSeries, color: 'var(--text-3)', name: 'UDP', fill: 0 },
      ],
    });
  }

  setText('uptime', status ? formatSecond(status.uptime) : '—');
  setText('lastSeen', formatDateTime(server.lastSeen));
  setText('loads', status && status.loads ? status.loads.map((v) => v.toFixed(2)).join(' · ') : '—');
  setText('procs', status ? String(status.procCount || 0) : '—');
  setText('cpuModel', (status && status.cpuModel) || server.kernel || '—');
  setText('cpuSpec', status ? `${coreFormat(status.cpuCores)} · ${cpuSpeedFormat(status.cpuSpeedMhz)}` : '—');
  setText('ipv4', server.ipv4 || '—');
  setText('ipv6', server.ipv6 || '');
}

/* ---------- server notes ---------- */

async function mountServerNotes() {
  const id = state.route.id;
  const server = serverById(id);

  view.innerHTML = `<div class="ov-page">
    <div class="ov-bar">
      <button class="btn ghost small" data-ref="back">${icons.back} ${t('backToOverview')}</button>
      <h1 class="ov-title">${escapeHtml(server ? server.name : '')}</h1>
      <div class="ov-bar-actions">
        ${serverTabsMarkup(id, 'notes')}
        <span class="ov-sub" data-ref="saveState"></span>
        <button class="btn primary" data-ref="save">${t('save')}</button>
      </div>
    </div>
    <p class="ov-sub" style="margin:0">${t('notesHint')}</p>
    <div class="card" data-ref="editor"></div>
  </div>`;

  ref('back').addEventListener('click', () => { window.location.hash = '#/'; });

  // What was last sent to the server, which is what "unchanged" is measured
  // against — not what the server stored, since the two differ harmlessly.
  let lastSent = '';
  let timer = null;

  const editor = createEditor(ref('editor'), {
    placeholder: t('notesPlaceholder'),
    onChange: () => {
      setText('saveState', t('unsaved'));
      clearTimeout(timer);
      timer = setTimeout(save, 1200);
    },
  });

  async function save() {
    clearTimeout(timer);
    const html = editor.html;
    if (html === lastSent) return;

    const previous = lastSent;
    lastSent = html;
    setText('saveState', t('saving'));
    try {
      const data = await api(`/api/servers/${id}/note`, {
        method: 'PUT',
        body: JSON.stringify({ html }),
      });
      // The server's copy is adopted only when the caret is elsewhere. Assigning
      // innerHTML rebuilds the editor's DOM and drops the caret back to the
      // start, and the two versions differ over nothing visible — the sanitizer
      // writes an apostrophe as &#39; — so mid-sentence it would scramble the
      // text being typed for no gain.
      if (!editor.hasFocus && data.html !== html) {
        editor.html = data.html;
        lastSent = data.html;
      }
      setText('saveState', t('savedAt', { value: formatDateTime(data.updatedAt) }));
    } catch (error) {
      lastSent = previous; // so the next attempt retries instead of going quiet
      setText('saveState', '');
      toast(error.message, 'error');
    }
  }

  ref('save').addEventListener('click', save);
  editor.onBlur(save);

  try {
    const note = await api(`/api/servers/${id}/note`);
    editor.html = note.html;
    lastSent = note.html;
    setText('saveState', note.updatedAt
      ? t('savedAt', { value: formatDateTime(note.updatedAt) })
      : t('noNoteYet'));
  } catch (error) {
    toast(error.message, 'error');
  }
}

/* ---------- note search ---------- */

function mountNoteSearch() {
  view.innerHTML = `<div class="ov-page">
    <div class="ov-bar"><h1 class="ov-title">${t('searchNotes')}</h1></div>
    <div class="card card-pad">
      <input class="input" data-ref="q" dir="auto" placeholder="${t('searchNotesPlaceholder')}" autofocus>
      <p class="hint" style="margin-top:8px">${t('searchNotesHint')}</p>
    </div>
    <div data-ref="results"></div>
  </div>`;

  const input = ref('q');
  let timer = null;

  const run = async () => {
    const query = input.value.trim();
    const results = ref('results');
    if (!query) {
      results.innerHTML = '';
      return;
    }
    try {
      const data = await api(`/api/notes/search?q=${encodeURIComponent(query)}`);
      results.innerHTML = data.hits.length
        ? data.hits.map((hit) => noteHitMarkup(hit, query)).join('')
        : `<div class="card empty">${t('noMatches')}</div>`;
    } catch (error) {
      results.innerHTML = `<div class="card empty">${escapeHtml(error.message)}</div>`;
    }
  };

  input.addEventListener('input', () => {
    clearTimeout(timer);
    timer = setTimeout(run, 250);
  });
  input.focus();
}

function noteHitMarkup(hit, query) {
  return `<a class="card hoverable note-hit" href="#/server/${hit.serverId}/notes">
    <div class="note-hit-head">
      <span class="srv-name">${escapeHtml(hit.serverName)}</span>
      ${hit.tag ? `<span class="srv-tag">${escapeHtml(hit.tag)}</span>` : ''}
      <span class="srv-uptime">${formatDateTime(hit.updatedAt)}</span>
    </div>
    <div class="note-hit-excerpt" dir="auto">${highlight(hit.excerpt, query)}</div>
  </a>`;
}

// The excerpt is plain text from the server; it is escaped here and only then
// does the marker go in, so a note can never inject markup through a search.
function highlight(text, query) {
  const escaped = escapeHtml(text);
  const needle = escapeHtml(query);
  if (!needle) return escaped;
  const pattern = new RegExp(needle.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'gi');
  return escaped.replace(pattern, (match) => `<mark>${match}</mark>`);
}

/* ---------- servers management ---------- */

async function mountServers() {
  view.innerHTML = `<div class="ov-page">
    <div class="ov-bar">
      <h1 class="ov-title">${t('servers')}</h1>
      <div class="ov-bar-actions">
        <button class="btn" data-ref="updateAll">${icons.refresh} ${t('updateAllAgents')}</button>
        <button class="btn primary" data-ref="add">${icons.plus} ${t('addServer')}</button>
      </div>
    </div>
    <div class="card table-scroll" data-ref="tableBox"></div>
  </div>`;

  ref('add').addEventListener('click', addServerDialog);
  ref('updateAll').addEventListener('click', () => {
    if (window.confirm(t('confirmUpdateAll', { version: shippedAgentVersion() }))) requestAgentUpdate('all', '');
  });
  await refreshServersTable();
}

async function refreshServersTable() {
  const box = ref('tableBox');
  if (!box) return;
  let data;
  try {
    data = await api('/api/servers');
  } catch (error) {
    box.innerHTML = `<div class="empty">${escapeHtml(error.message)}</div>`;
    return;
  }

  if (!data.servers.length) {
    box.innerHTML = `<div class="empty">${t('noServers')}</div>`;
    return;
  }

  box.innerHTML = `<table class="table">
    <thead><tr>
      <th>${t('serverName')}</th><th>${t('status')}</th><th>${t('lastSeen')}</th>
      <th>${t('agentVersion')}</th><th></th>
    </tr></thead>
    <tbody>${data.servers.map(serverRowMarkup).join('')}</tbody>
  </table>`;

  box.querySelectorAll('[data-action]').forEach((button) => {
    button.addEventListener('click', () => {
      const id = Number(button.dataset.id);
      const server = data.servers.find((item) => item.id === id);
      if (!server) return;
      if (button.dataset.action === 'agentUpdate') requestAgentUpdate(server.id, server.name);
      if (button.dataset.action === 'edit') editServerDialog(server);
      if (button.dataset.action === 'install') showInstallDialog(server);
      if (button.dataset.action === 'rotate') rotateToken(server);
      if (button.dataset.action === 'delete') deleteServer(server);
    });
  });
}

// The agent has its own version: a dashboard-only release must not mark every
// agent out of date. Compare against the build the hub ships, not the hub.
function shippedAgentVersion() {
  return (state.payload && state.payload.agentVersion) || '';
}

function agentCell(server) {
  const current = server.agentVersion || '—';
  if (server.updateTo) {
    return `<span class="pill" style="color:var(--warn)"><span class="dot"></span>${t('updating')} ${escapeHtml(server.updateTo)}</span>`;
  }
  const hub = shippedAgentVersion();
  if (!hub || !server.agentVersion || server.agentVersion === hub) {
    return escapeHtml(current);
  }
  return `${escapeHtml(current)}
    <button class="btn small" data-action="agentUpdate" data-id="${server.id}" title="${t('updateTo', { version: hub })}">
      ${icons.refresh} ${hub}
    </button>`;
}

function serverRowMarkup(server) {
  const color = server.online ? 'var(--success)' : 'var(--crit)';
  return `<tr>
    <td dir="ltr">
      <div style="font-weight:600">${escapeHtml(server.name)}</div>
      <div class="ov-sub">${escapeHtml(server.hostname || '')} ${escapeHtml(server.ipv4 || '')}</div>
    </td>
    <td><span class="pill" style="color:${color}"><span class="dot"></span>${server.online ? t('online') : t('offline')}</span></td>
    <td dir="ltr">${formatDateTime(server.lastSeen)}</td>
    <td dir="ltr" style="white-space:nowrap">${agentCell(server)}</td>
    <td style="text-align:end;white-space:nowrap">
      <button class="btn small" data-action="edit" data-id="${server.id}">${t('edit')}</button>
      <button class="btn small" data-action="install" data-id="${server.id}">${t('installCommand')}</button>
      <button class="btn small" data-action="rotate" data-id="${server.id}">${t('rotate')}</button>
      <button class="btn small danger" data-action="delete" data-id="${server.id}">${t('remove')}</button>
    </td>
  </tr>`;
}

function openModal(html) {
  const mask = document.createElement('div');
  mask.className = 'mask';
  mask.innerHTML = `<div class="modal">${html}</div>`;
  mask.addEventListener('click', (event) => {
    if (event.target === mask) mask.remove();
  });
  document.body.appendChild(mask);
  return mask;
}

function addServerDialog() {
  const mask = openModal(`<h3>${t('addServer')}</h3>
    <div class="field"><label>${t('serverName')}</label><input class="input" data-ref="name" placeholder="frankfurt-1"></div>
    <div class="field"><label>${t('group')}</label><input class="input" data-ref="tag" placeholder="europe"></div>
    <div class="modal-foot">
      <button class="btn" data-ref="cancel">${t('cancel')}</button>
      <button class="btn primary" data-ref="create">${t('save')}</button>
    </div>`);

  const nameInput = ref('name', mask);
  nameInput.focus();
  ref('cancel', mask).addEventListener('click', () => mask.remove());
  ref('create', mask).addEventListener('click', async () => {
    try {
      const server = await api('/api/servers', {
        method: 'POST',
        body: JSON.stringify({ name: nameInput.value.trim(), tag: ref('tag', mask).value.trim() }),
      });
      mask.remove();
      await refreshServersTable();
      showInstallDialog(server);
    } catch (error) {
      toast(error.message, 'error');
    }
  });
}

// The hub hands the instruction to the agent on its next push, so the table is
// reloaded a moment later to show the request rather than the old version.
async function requestAgentUpdate(id, name) {
  try {
    const data = await api(`/api/servers/${id}/update`, { method: 'POST' });
    toast(name
      ? t('updateQueuedOne', { name, version: data.version })
      : t('updateQueuedAll', { count: data.requested, version: data.version }), 'ok');
    setTimeout(refreshServersTable, 1500);
  } catch (error) {
    toast(error.message, 'error');
  }
}

function editServerDialog(server, onSaved = refreshServersTable) {
  const mask = openModal(`<h3>${t('editServer')}</h3>
    <div class="field"><label>${t('serverName')}</label>
      <input class="input" data-ref="name" value="${escapeHtml(server.name)}"></div>
    <div class="field"><label>${t('group')}</label>
      <input class="input" data-ref="tag" value="${escapeHtml(server.tag || '')}" placeholder="europe"></div>
    <p class="section-sub">${t('editServerHint')}</p>
    <div class="modal-foot">
      <button class="btn" data-ref="cancel">${t('cancel')}</button>
      <button class="btn primary" data-ref="save">${t('save')}</button>
    </div>`);

  const nameInput = ref('name', mask);
  nameInput.focus();
  nameInput.select();

  const submit = async () => {
    const name = nameInput.value.trim();
    if (!name) {
      toast(t('nameRequired'), 'error');
      return;
    }
    try {
      await api(`/api/servers/${server.id}`, {
        method: 'PATCH',
        body: JSON.stringify({ name, tag: ref('tag', mask).value.trim(), sort: server.sort }),
      });
      mask.remove();
      toast(t('saved'), 'ok');
      await onSaved();
    } catch (error) {
      toast(error.message, 'error');
    }
  };

  ref('cancel', mask).addEventListener('click', () => mask.remove());
  ref('save', mask).addEventListener('click', submit);
  mask.addEventListener('keydown', (event) => {
    if (event.key === 'Enter') submit();
    if (event.key === 'Escape') mask.remove();
  });
}

function showInstallDialog(server) {
  const mask = openModal(`<h3>${t('installTitle')}</h3>
    <p class="section-sub">${t('installSub')}</p>
    <div class="code">${escapeHtml(server.installCommand)}</div>
    <div class="field" style="margin-top:16px">
      <label>${t('token')}</label>
      <div class="code">${escapeHtml(server.token)}</div>
    </div>
    <div class="field">
      <label>${t('manageOnServer')}</label>
      <div class="code">srvmon</div>
      <span class="hint">${t('manageOnServerHint')}</span>
    </div>
    <div class="field">
      <label>${t('uninstallCommand')}</label>
      <div class="code">${escapeHtml(server.uninstallCommand || '')}</div>
    </div>
    <div class="modal-foot">
      <button class="btn" data-ref="copyUninstall">${icons.copy} ${t('uninstallCommand')}</button>
      <button class="btn" data-ref="copy">${icons.copy} ${t('installCommand')}</button>
      <button class="btn primary" data-ref="close">${t('close')}</button>
    </div>`);

  ref('close', mask).addEventListener('click', () => mask.remove());
  ref('copy', mask).addEventListener('click', () => copyText(server.installCommand));
  ref('copyUninstall', mask).addEventListener('click', () => copyText(server.uninstallCommand || ''));
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast(t('copied'), 'ok');
  } catch {
    const area = document.createElement('textarea');
    area.value = text;
    document.body.appendChild(area);
    area.select();
    document.execCommand('copy');
    area.remove();
    toast(t('copied'), 'ok');
  }
}

async function rotateToken(server) {
  if (!window.confirm(t('confirmRotate', { name: server.name }))) return;
  try {
    const data = await api(`/api/servers/${server.id}/token`, { method: 'POST' });
    await refreshServersTable();
    showInstallDialog({ ...server, token: data.token, installCommand: data.installCommand });
  } catch (error) {
    toast(error.message, 'error');
  }
}

async function deleteServer(server) {
  if (!window.confirm(t('confirmDelete', { name: server.name }))) return;
  try {
    await api(`/api/servers/${server.id}`, { method: 'DELETE' });
    await refreshServersTable();
  } catch (error) {
    toast(error.message, 'error');
  }
}

/* ---------- alerts ---------- */

async function mountAlerts() {
  view.innerHTML = `<div class="ov-page">
    <div class="ov-bar"><h1 class="ov-title">${t('alerts')}</h1></div>
    <div class="card table-scroll" data-ref="box"><div class="empty">…</div></div>
  </div>`;

  try {
    const data = await api('/api/alerts?limit=200');
    const box = ref('box');
    if (!data.events.length) {
      box.innerHTML = `<div class="empty">${t('noAlerts')}</div>`;
      return;
    }
    box.innerHTML = `<table class="table">
      <thead><tr><th>${t('when')}</th><th>${t('server')}</th><th>${t('metric')}</th><th>${t('status')}</th><th>${t('value')}</th></tr></thead>
      <tbody>${data.events.map(alertRowMarkup).join('')}</tbody>
    </table>`;
  } catch (error) {
    ref('box').innerHTML = `<div class="empty">${escapeHtml(error.message)}</div>`;
  }
}

function alertRowMarkup(event) {
  const firing = event.state === 'firing';
  const color = firing ? 'var(--crit)' : 'var(--success)';
  const value = event.kind === 'offline' ? formatSecond(event.value) : `${event.value.toFixed(1)}%`;
  return `<tr>
    <td dir="ltr">${formatDateTime(event.t)}</td>
    <td dir="ltr">${escapeHtml(event.serverName)}</td>
    <td dir="ltr">${escapeHtml(event.kind)}</td>
    <td><span class="pill" style="color:${color}"><span class="dot"></span>${firing ? t('firing') : t('cleared')}</span></td>
    <td dir="ltr">${value}</td>
  </tr>`;
}

/* ---------- settings ---------- */

async function mountSettings() {
  view.innerHTML = `<div class="ov-page">
    <div class="ov-bar"><h1 class="ov-title">${t('settings')}</h1></div>

    <div class="card card-pad">
      <h2 class="section-title">${t('alertThresholds')}</h2>
      <p class="section-sub">${t('alertThresholdsSub')}</p>
      <label class="switch" style="margin-bottom:18px">
        <input type="checkbox" data-ref="alertsEnabled"><span>${t('enableAlerts')}</span>
      </label>
      <div class="form-grid">
        <div class="field"><label>${t('cpuThreshold')}</label><input class="input" type="number" min="1" max="100" data-ref="cpuThreshold"></div>
        <div class="field"><label>${t('memThreshold')}</label><input class="input" type="number" min="1" max="100" data-ref="memThreshold"></div>
        <div class="field"><label>${t('diskThreshold')}</label><input class="input" type="number" min="1" max="100" data-ref="diskThreshold"></div>
        <div class="field"><label>${t('offlineAfter')}</label><input class="input" type="number" min="10" data-ref="offlineAfter"></div>
        <div class="field"><label>${t('sustain')}</label><input class="input" type="number" min="1" data-ref="sustain"></div>
        <div class="field"><label>${t('hubUrl')}</label><input class="input" data-ref="baseUrl" placeholder="https://monitor.example.com">
          <span class="hint">${t('hubUrlHint')}</span></div>
      </div>

      <h2 class="section-title" style="margin-top:12px">${t('telegram')}</h2>
      <p class="section-sub">${t('telegramSub')}</p>
      <div class="form-grid">
        <div class="field"><label>${t('botToken')}</label><input class="input" type="password" autocomplete="new-password" data-ref="telegramToken">
          <span class="hint" data-ref="tokenHint">${t('botTokenHint')}</span></div>
        <div class="field"><label>${t('chatId')}</label><input class="input" data-ref="telegramChatId" placeholder="123456789"></div>
      </div>
      <div style="display:flex;gap:8px">
        <button class="btn primary" data-ref="save">${t('save')}</button>
        <button class="btn" data-ref="test">${t('sendTest')}</button>
      </div>
    </div>

    <div class="card card-pad">
      <h2 class="section-title">${t('backup')}</h2>
      <p class="section-sub">${t('backupSub')}</p>
      <div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center">
        <button class="btn primary" data-ref="download">${icons.copy} ${t('downloadBackup')}</button>
        <input type="file" accept=".db,application/octet-stream" data-ref="file" style="display:none">
        <button class="btn" data-ref="pick">${t('chooseBackup')}</button>
        <span class="ov-sub" data-ref="fileName"></span>
        <button class="btn danger" data-ref="restore" disabled>${t('restoreBackup')}</button>
      </div>
      <p class="hint" style="margin-top:12px">${t('restoreWarning')}</p>
    </div>

    <div class="card card-pad">
      <h2 class="section-title">${t('account')}</h2>
      <p class="section-sub">${t('accountSub')}</p>
      <div class="form-grid">
        <div class="field"><label>${t('username')}</label><input class="input" data-ref="username" autocomplete="username"></div>
        <div class="field"><label>${t('currentPassword')}</label><input class="input" type="password" data-ref="currentPassword" autocomplete="current-password"></div>
        <div class="field"><label>${t('newPassword')}</label><input class="input" type="password" data-ref="newPassword" autocomplete="new-password"></div>
      </div>
      <button class="btn primary" data-ref="saveAccount">${t('updateAccount')}</button>
    </div>
  </div>`;

  try {
    const settings = await api('/api/settings');
    ref('alertsEnabled').checked = settings.alertsEnabled;
    ref('cpuThreshold').value = settings.cpuThreshold;
    ref('memThreshold').value = settings.memThreshold;
    ref('diskThreshold').value = settings.diskThreshold;
    ref('offlineAfter').value = settings.offlineAfter;
    ref('sustain').value = settings.sustain;
    ref('baseUrl').value = settings.baseUrl || '';
    ref('telegramChatId').value = settings.telegramChatId || '';
    if (settings.telegramConfigured) ref('telegramToken').placeholder = '••••••••••';
  } catch (error) {
    toast(error.message, 'error');
  }

  try {
    const me = await api('/api/auth/me');
    ref('username').value = me.username;
  } catch {
    /* the session guard already redirects */
  }

  ref('save').addEventListener('click', saveSettings);
  ref('test').addEventListener('click', async () => {
    try {
      await api('/api/settings/telegram/test', { method: 'POST' });
      toast(t('testSent'), 'ok');
    } catch (error) {
      toast(error.message, 'error');
    }
  });
  ref('saveAccount').addEventListener('click', saveAccount);
  wireBackup();
}

function wireBackup() {
  const file = ref('file');
  const restore = ref('restore');

  ref('download').addEventListener('click', () => {
    // A plain navigation, so the browser handles the download and the
    // Content-Disposition filename rather than buffering it in memory.
    window.location.href = '/api/backup';
  });

  ref('pick').addEventListener('click', () => file.click());
  file.addEventListener('change', () => {
    const chosen = file.files[0];
    setText('fileName', chosen ? chosen.name : '');
    restore.disabled = !chosen;
  });

  restore.addEventListener('click', async () => {
    const chosen = file.files[0];
    if (!chosen || !window.confirm(t('confirmRestore', { name: chosen.name }))) return;

    const body = new FormData();
    body.append('backup', chosen);
    restore.disabled = true;
    try {
      const response = await fetch('/api/restore', { method: 'POST', credentials: 'same-origin', body });
      const data = await response.json().catch(() => null);
      if (!response.ok) throw new Error((data && data.error) || response.statusText);

      toast(t('restored', { servers: data.servers }), 'ok');
      // The hub exits so its manager restarts it against the restored file;
      // the reload lands on the login page once it is back.
      setTimeout(() => { window.location.href = '/login'; }, 4000);
    } catch (error) {
      toast(error.message, 'error');
      restore.disabled = false;
    }
  });
}

async function saveSettings() {
  const body = {
    alertsEnabled: ref('alertsEnabled').checked,
    cpuThreshold: Number(ref('cpuThreshold').value),
    memThreshold: Number(ref('memThreshold').value),
    diskThreshold: Number(ref('diskThreshold').value),
    offlineAfter: Number(ref('offlineAfter').value),
    sustain: Number(ref('sustain').value),
    baseUrl: ref('baseUrl').value.trim(),
    telegramChatId: ref('telegramChatId').value.trim(),
    telegramToken: ref('telegramToken').value.trim(),
  };
  try {
    await api('/api/settings', { method: 'POST', body: JSON.stringify(body) });
    ref('telegramToken').value = '';
    toast(t('saved'), 'ok');
  } catch (error) {
    toast(error.message, 'error');
  }
}

async function saveAccount() {
  const body = {
    username: ref('username').value.trim(),
    currentPassword: ref('currentPassword').value,
    newPassword: ref('newPassword').value,
  };
  try {
    await api('/api/account', { method: 'POST', body: JSON.stringify(body) });
    toast(t('accountUpdated'), 'ok');
    setTimeout(() => { window.location.href = '/login'; }, 1200);
  } catch (error) {
    toast(error.message, 'error');
  }
}

/* ---------- boot ---------- */

function buildSidebar() {
  const nav = document.getElementById('nav');
  const items = [
    { route: 'overview', hash: '#/', icon: icons.network, label: t('overview') },
    { route: 'servers', hash: '#/servers', icon: icons.server, label: t('servers') },
    { route: 'notes', hash: '#/notes', icon: icons.pencil, label: t('notes') },
    { route: 'alerts', hash: '#/alerts', icon: icons.bell, label: t('alerts') },
    { route: 'settings', hash: '#/settings', icon: icons.gear, label: t('settings') },
  ];
  nav.innerHTML = items
    .map((item) => `<a class="nav-item" data-route="${item.route}" href="${item.hash}">${item.icon}<span>${item.label}</span></a>`)
    .join('');
}

function boot() {
  applyDirection();
  applyTheme(localStorage.getItem('srvmon-dark') !== 'false');
  buildSidebar();

  document.getElementById('themeToggle').addEventListener('click', () => {
    applyTheme(!isDark());
  });
  document.getElementById('langToggle').addEventListener('click', () => {
    setLang(lang === 'fa' ? 'en' : 'fa');
    window.location.reload();
  });
  document.querySelector('#logout .brand-text').textContent = t('logout');
  document.getElementById('logout').addEventListener('click', async () => {
    await api('/api/auth/logout', { method: 'POST' }).catch(() => undefined);
    window.location.href = '/login';
  });

  window.addEventListener('hashchange', navigate);
  navigate();
  connectStream();

  api('/api/dashboard').then(applyPayload).catch(() => undefined);
}

boot();
