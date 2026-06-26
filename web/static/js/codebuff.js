// Codebuff Telemetry — vertically rich cards with time-range filters, rolling RPM,
// per-model tokens, error labels and live-responsive refresh.
// Reads server-provided `rpm` / `errors_total` from /api/codebuff/metrics.

const RANGE_OPTIONS = [
  { id: "15m", label: "Last 15m" },
  { id: "1h",  label: "Last 1h" },
  { id: "6h",  label: "Last 6h" },
  { id: "24h", label: "Last 24h" },
  { id: "all", label: "All time" },
];

const REFRESH_OPTIONS = [
  { id: "10000", label: "10s" },
  { id: "30000", label: "30s" },
  { id: "60000", label: "1m" },
  { id: "300000", label: "5m" },
  { id: "0",     label: "off" },
];

let poolData = null;
let metricsData = null;
let expanded = new Set();
let accountFilter = "all";

let activeRange = "all";
let refreshMs = 30000;
let refreshTimer = null;
let autoSyncTimer = null;
let lastSyncedAt = null;

// Show a per-second spinner while pings are pending.
function setRefreshBtn(busy) {
  const btn = document.getElementById("refreshBtn");
  if (!btn) return;
  if (busy) { btn.disabled = true; btn.textContent = "Refreshing…"; }
  else { btn.disabled = false; btn.textContent = "Refresh"; }
}

function loadCodebuffData() {
  setRefreshBtn(true);
  const qs = (activeRange && activeRange !== "all") ? `?range=${encodeURIComponent(activeRange)}` : "";
  Promise.all([
    fetch("/api/codebuff/pool-status").then((r) => r.ok ? r.json() : null).catch(() => null),
    fetch("/api/codebuff/metrics" + qs).then((r) => r.ok ? r.json() : null).catch(() => null),
  ]).then(([pool, metrics]) => {
    poolData = pool;
    metricsData = metrics;
    renderAll();
    updateBanner();
  }).catch((err) => console.error("loadCodebuffData failed", err))
    .finally(() => setRefreshBtn(false));
}

function syncQuota() {
  const btn = document.getElementById("syncBtn");
  if (btn) { btn.disabled = true; btn.textContent = "Syncing…"; }
  if (!poolData || !poolData.accounts) {
    setTimeout(() => { if (btn) { btn.disabled = false; btn.textContent = "Sync quota"; } loadCodebuffData(); }, 200);
    return;
  }
  const syncs = poolData.accounts.map((acc) =>
    fetch("/api/accounts/" + acc.account_id + "/codebuff-sync", { method: "POST" })
      .then((r) => r.ok).catch(() => false)
  );
  Promise.all(syncs).then(() => {
    lastSyncedAt = Date.now();
    updateLastSyncLabel();
    setTimeout(() => {
      loadCodebuffData();
      if (btn) { btn.disabled = false; btn.textContent = "Sync quota"; }
    }, 500);
  });
}

function updateLastSyncLabel() {
  const el = document.getElementById("lastSync");
  if (!el) return;
  if (!lastSyncedAt) { el.textContent = "— never"; return; }
  const sec = Math.max(0, Math.floor((Date.now() - lastSyncedAt) / 1000));
  el.textContent = "last synced " + fmtDuration(sec) + " ago";
}

function renderAll() {
  renderStats();
  renderRangeBar();
  renderRefreshBar();
  renderFilter();
  renderCards();
}

function totalAcross(predicate) {
  if (!metricsData) return zero();
  const acc = zero();
  metricsData.forEach((m) => {
    if (!predicate(m)) return;
    acc.reqs += m.total.requests;
    acc.s429 += m.total.errors_429;
    acc.serr += m.total.errors_total || 0;
    acc.tokens += m.total.tokens;
    acc.latencyMs += m.total.latency_ms || 0;
    if (m.total.last_used && m.total.last_used > acc.newest) acc.newest = m.total.last_used;
    if (!acc.oldest || (m.total.first_used && m.total.first_used < acc.oldest)) acc.oldest = m.total.first_used;
    if (typeof m.rpm === "number" && m.rpm > acc.rpm) acc.rpm = m.rpm;
  });
  acc.avgMs = acc.reqs > 0 ? Math.round(acc.latencyMs / acc.reqs) : 0;
  acc.tps = acc.latencyMs > 0 ? acc.tokens / (acc.latencyMs / 1000) : 0;
  // Sum rolling RPM per account → banner total
  acc.rpm = 0;
  (metricsData || []).forEach((m) => {
    if (!predicate(m)) return;
    acc.rpm += (m.rpm || 0);
  });
  return acc;
}

function zero() {
  return { reqs: 0, s429: 0, serr: 0, tokens: 0, latencyMs: 0, avgMs: 0, tps: 0, rpm: 0, oldest: 0, newest: 0 };
}

function renderStats() {
  const t = totalAcross(() => true);
  document.getElementById("statReqs").textContent = t.reqs.toLocaleString();
  document.getElementById("stat429s").textContent = t.s429.toLocaleString();
  document.getElementById("statErrors").textContent = t.serr.toLocaleString();
  document.getElementById("statErrorsFoot").textContent =
    t.reqs > 0 ? ((t.serr / t.reqs) * 100).toFixed(1) + "% error rate" : "0 errors";
  document.getElementById("statTokens").textContent = shortNumber(t.tokens);
  document.getElementById("statRPM").textContent = t.rpm.toFixed(1);
  const rangeLabel = (RANGE_OPTIONS.find((r) => r.id === activeRange) || {}).label || "All time";
  document.getElementById("statRPMFoot").textContent = `${rangeLabel} • rolling`;
  document.getElementById("statTokensFoot").textContent =
    activeRange === "all" ? "prompt + completion (lifetime)" : `prompt + completion (${rangeLabel.toLowerCase()})`;
}

function shortNumber(n) {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
  if (n >= 1_000) return (n / 1_000).toFixed(1) + "k";
  return n.toString();
}

function updateBanner() {
  const banner = document.getElementById("lastUpdate");
  if (banner) banner.textContent = "last refreshed " + new Date().toLocaleTimeString();
  const reset = document.getElementById("resetIn");
  if (reset) reset.textContent = computeResetIn();
  updateLastSyncLabel();
}

function renderRangeBar() {
  const bar = document.getElementById("rangeBar");
  if (!bar) return;
  bar.innerHTML = RANGE_OPTIONS.map((r) => {
    const sel = r.id === activeRange ? " active" : "";
    return `<button class="range-btn${sel}" data-range="${r.id}" onclick="setRange('${r.id}')">${r.label}</button>`;
  }).join("");
}

function setRange(id) {
  if (activeRange === id) return;
  activeRange = id;
  renderRangeBar();
  loadCodebuffData();
}

function renderRefreshBar() {
  const bar = document.getElementById("refreshBar");
  if (!bar) return;
  bar.innerHTML =
    `<span class="filter-label">Refresh</span>` +
    REFRESH_OPTIONS.map((r) => {
      const sel = String(refreshMs) === r.id ? " active" : "";
      return `<button class="range-btn${sel}" data-refresh="${r.id}" onclick="setRefresh(${r.id})">${r.label}</button>`;
    }).join("");
}

function setRefresh(ms) {
  refreshMs = parseInt(ms, 10) || 0;
  renderRefreshBar();
  if (refreshTimer) { clearInterval(refreshTimer); refreshTimer = null; }
  if (refreshMs > 0) refreshTimer = setInterval(loadCodebuffData, refreshMs);
}

function renderFilter() {
  const container = document.getElementById("filterBar");
  if (!container || !poolData) return;
  const options = poolData.accounts.map((acc) => {
    const sel = acc.account_id === accountFilter ? " selected" : "";
    return `<option value="${acc.account_id}"${sel}>#${acc.account_id} ${acc.name}</option>`;
  }).join("");
  container.innerHTML = `
    <span class="filter-label">Viewing</span>
    <select id="accFilter" onchange="setAccountFilter(this.value)">
      <option value="all"${accountFilter === "all" ? " selected" : ""}>All accounts</option>
      ${options}
    </select>
    <button id="expandAllBtn" onclick="toggleExpandAll()">${expanded.size ? "Collapse" : "Expand"} all</button>
  `;
}

function setAccountFilter(id) {
  accountFilter = (id === "all") ? "all" : Number(id);
  renderCards();
}

function toggleExpandAll() {
  if (!poolData) return;
  if (expanded.size) expanded.clear();
  else poolData.accounts.forEach((a) => expanded.add(a.account_id));
  renderCards();
}

function toggleExpand(accID) {
  if (expanded.has(accID)) expanded.delete(accID);
  else expanded.add(accID);
  const card = document.getElementById("card-" + accID);
  if (card) card.classList.toggle("expanded");
  const detail = document.getElementById("detail-" + accID);
  if (detail) detail.style.display = expanded.has(accID) ? "block" : "none";
  const btn = document.getElementById("expand-btn-" + accID);
  if (btn) btn.textContent = expanded.has(accID) ? "Hide models" : "Show models";
}

function renderCards() {
  const container = document.getElementById("cardContainer");
  if (!container || !poolData) return;
  const allAccounts = poolData.accounts || [];
  const accounts = allAccounts.filter((a) => accountFilter === "all" || a.account_id === accountFilter);
  const models = poolData.all_models || [];

  if (!accounts.length) {
    container.innerHTML = `<div class="empty">No accounts configured.</div>`;
    return;
  }

  container.innerHTML = accounts.map((acc) => buildCard(acc, models)).join("");
  updateCountdown();
}

function buildCard(acc, models) {
  const accMetric = (metricsData || []).find((m) => m.account_id === acc.account_id);
  const t = accMetric ? accMetric.total : zero();

  const accReqs = t.requests || 0;
  const acc429 = t.errors_429 || 0;
  const accErr = t.errors_total || 0;
  const accTokens = t.tokens || 0;
  const accLatMs = accReqs > 0 ? Math.round((t.latency_ms || 0) / accReqs) : 0;
  // Tokens/s labelled clearly: divisor is wall_ms (server wall clock serving), not seconds-since-first-request.
  const wall = t.wall_ms || t.latency_ms || 0;
  const accTps = wall > 0 ? (accTokens / (wall / 1000)) : 0;
  const accRpm = (typeof t.rpm === "number" && t.rpm > 0) ? t.rpm :
                  (accMetric && typeof accMetric.rpm === "number" ? accMetric.rpm : 0);
  const lastUsed = t.last_used ? fmtTimeLeft(t.last_used) : "—";

  // Quota totals
  let totalRemaining = 0, totalLimit = 0, healthyModels = 0, exhaustedModels = 0, blockedModels = 0;
  models.forEach((m) => {
    const cell = acc.models[m] || {};
    totalRemaining += (cell.remaining !== undefined ? cell.remaining : (cell.limit || 0));
    totalLimit += (cell.limit || 0);
    if (cell.blocked) blockedModels++;
    else if ((cell.remaining !== undefined ? cell.remaining : (cell.limit || 0)) === 0) exhaustedModels++;
    else healthyModels++;
  });
  const totalConsumed = totalLimit - totalRemaining;
  const usagePct = totalLimit > 0 ? Math.min(100, Math.round((totalConsumed / totalLimit) * 100)) : 0;

  let statusLabel, statusClass;
  if (blockedModels > 0) { statusLabel = `${blockedModels} blocked`; statusClass = "pill-danger"; }
  else if (totalRemaining === 0 && totalLimit > 0) { statusLabel = "All exhausted"; statusClass = "pill-warn"; }
  else if (accReqs === 0) { statusLabel = "Idle"; statusClass = "pill-idle"; }
  else { statusLabel = "Healthy"; statusClass = "pill-ok"; }

  const expandedCls = expanded.has(acc.account_id) ? "expanded" : "";

  const modelRows = models.map((m) => buildModelRow(acc, m, accMetric)).join("");
  const emptyModels = models.length === 0 ? `<div class="empty-models">No models registered for this account.</div>` : "";

  const rangeLabel = (RANGE_OPTIONS.find((r) => r.id === activeRange) || {}).label || "All time";
  const reset = acc.models[models[0]]?.reset_at && new Date(acc.models[models[0]].reset_at).getFullYear() > 1
    ? fmtTimeLeft(new Date(acc.models[models[0]].reset_at)) : "—";

  return `
    <article class="card ${expandedCls}" id="card-${acc.account_id}">
      <header class="card-head">
        <div class="card-title">
          <div class="acc-id">#${acc.account_id}</div>
          <div class="acc-name">${acc.name}</div>
        </div>
        <div class="card-status">
          <span class="pill ${statusClass}">${statusLabel}</span>
          <span class="muted">${healthyModels}/${models.length} models healthy</span>
        </div>
        <button class="expand-btn" id="expand-btn-${acc.account_id}" onclick="toggleExpand(${acc.account_id})">
          ${expanded.has(acc.account_id) ? "Hide models" : "Show models"}
        </button>
      </header>

      <section class="card-body">
        <div class="quota-block">
          <div class="quota-meta">
            <div>
              <div class="meta-label">Quota</div>
              <div class="meta-value">${totalConsumed} / ${totalLimit}</div>
            </div>
            <div class="meta-pct">${usagePct}% used</div>
          </div>
          <div class="quota-bar">
            <div class="quota-fill ${usagePct > 80 ? 'fill-warn' : ''} ${usagePct === 100 ? 'fill-danger' : ''}" style="width: ${usagePct}%"></div>
          </div>
          <div class="quota-foot">
            <span>${totalRemaining} requests left today</span>
            <span>Resets in ${reset}</span>
          </div>
        </div>

        <div class="stat-grid">
          <div class="stat">
            <div class="stat-label">Requests</div>
            <div class="stat-num">${accReqs.toLocaleString()}</div>
            <div class="stat-foot">${rangeLabel.toLowerCase()}</div>
          </div>
          <div class="stat">
            <div class="stat-label">Errors</div>
            <div class="stat-num ${accErr > 0 ? 'num-warn' : ''}">${accErr.toLocaleString()}</div>
            <div class="stat-foot">${accReqs > 0 ? ((acc429 > 0 ? acc429 + ' × 429 · ' : '') + ((accErr / accReqs) * 100).toFixed(1) + '% fail') : '—'}</div>
          </div>
          <div class="stat">
            <div class="stat-label">Tokens</div>
            <div class="stat-num">${shortNumber(accTokens)}</div>
            <div class="stat-foot">${rangeLabel.toLowerCase()}</div>
          </div>
          <div class="stat">
            <div class="stat-label">Avg Latency</div>
            <div class="stat-num">${accLatMs > 0 ? accLatMs + ' ms' : '—'}</div>
            <div class="stat-foot">per request</div>
          </div>
          <div class="stat">
            <div class="stat-label">Throughput</div>
            <div class="stat-num">${accTps.toFixed(1)} <span class="unit">t/s (wall)</span></div>
            <div class="stat-foot">tokens per serving-second</div>
          </div>
          <div class="stat">
            <div class="stat-label">Rate (60s)</div>
            <div class="stat-num">${accRpm.toFixed(2)} <span class="unit">req/m</span></div>
            <div class="stat-foot">${rangeLabel.toLowerCase()} • rolling</div>
          </div>
          <div class="stat">
            <div class="stat-label">Last Used</div>
            <div class="stat-num">${lastUsed}</div>
            <div class="stat-foot">ago</div>
          </div>
        </div>
      </section>

      <section class="model-detail" id="detail-${acc.account_id}" style="display:${expanded.has(acc.account_id) ? 'block' : 'none'}">
        <div class="model-table-head">
          <span>Model</span>
          <span>Quota</span>
          <span>Reqs</span>
          <span>429s</span>
          <span>Tokens</span>
          <span>T/s</span>
          <span>Avg ms</span>
        </div>
        ${emptyModels || modelRows}
      </section>
    </article>
  `;
}

function buildModelRow(acc, m, accMetric) {
  const cell = acc.models[m] || {};
  const mm = (accMetric && accMetric.models && accMetric.models[m]) || null;

  const remaining = cell.remaining !== undefined ? cell.remaining : (cell.limit || 0);
  const limit = cell.limit || 0;
  const exhausted = remaining === 0 && limit > 0;
  const blocked = cell.blocked;
  const reqs = mm ? mm.requests : 0;
  const e429 = mm ? mm.errors_429 : 0;
  const tokens = mm ? mm.tokens : 0;
  const wall = (mm && (mm.wall_ms || mm.latency_ms)) || 0;
  const tps = wall > 0 ? (tokens / (wall / 1000)) : 0;
  const avgMs = mm ? mm.avg_latency_ms : 0;

  let state, stateCls;
  if (blocked) { state = "429 blocked"; stateCls = "danger"; }
  else if (exhausted) { state = "exhausted"; stateCls = "warn"; }
  else if (reqs === 0) { state = "untouched"; stateCls = "idle"; }
  else { state = "alive"; stateCls = "ok"; }

  const reset = cell.reset_at && new Date(cell.reset_at).getFullYear() > 1
    ? fmtTimeLeft(new Date(cell.reset_at)) : "—";
  const shortModel = m.split("/").slice(-1)[0];
  const used = limit - remaining;
  const pct = limit > 0 ? Math.min(100, Math.round((used / limit) * 100)) : 0;

  return `
    <div class="model-row state-${stateCls}">
      <div class="m-cell m-name">
        <div class="m-short">${shortModel}</div>
        <div class="m-full" title="${m}">${m}</div>
      </div>
      <div class="m-cell m-quota">
        <div class="m-quota-line">
          <span class="m-quota-count">${used} / ${limit}</span>
          <span class="m-quota-state state-${stateCls}">${state}</span>
        </div>
        <div class="m-quota-bar">
          <div class="m-fill ${pct >= 80 ? 'fill-warn' : ''} ${pct === 100 ? 'fill-danger' : ''} ${stateCls === 'danger' ? 'fill-danger' : ''}" style="width: ${pct}%"></div>
        </div>
        <div class="m-quota-foot">Resets in ${reset}</div>
      </div>
      <div class="m-cell m-num">${reqs.toLocaleString()}</div>
      <div class="m-cell m-num m-warn">${e429.toLocaleString()}</div>
      <div class="m-cell m-num">${shortNumber(tokens)}</div>
      <div class="m-cell m-num">${tps.toFixed(1)}</div>
      <div class="m-cell m-num">${avgMs > 0 ? avgMs + ' ms' : '—'}</div>
    </div>
  `;
}

function computeResetIn() {
  const now = new Date();
  const utc = new Date(now.toLocaleString("en-US", { timeZone: "UTC" }));
  const next = new Date(utc);
  next.setUTCHours(7, 0, 0, 0);
  if (next <= now) next.setUTCDate(next.getUTCDate() + 1);
  return fmtDuration(Math.max(0, Math.floor((next - now) / 1000)));
}

function updateCountdown() {
  const now = new Date();
  const utc = new Date(now.toLocaleString("en-US", { timeZone: "UTC" }));
  const next = new Date(utc);
  next.setUTCHours(7, 0, 0, 0);
  if (next <= now) next.setUTCDate(next.getUTCDate() + 1);
  let sec = Math.max(0, Math.floor((next - now) / 1000));
  const h = Math.floor(sec / 3600); sec -= h * 3600;
  const m = Math.floor(sec / 60); sec -= m * 60;
  const set = (id, v) => { const el = document.getElementById(id); if (el) el.textContent = String(v).padStart(2, "0"); };
  set("cdH", h); set("cdM", m); set("cdS", sec);
}

function fmtDuration(sec) {
  const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${sec}s`;
}

function fmtTimeLeft(input) {
  const now = Date.now();
  const t = (input instanceof Date) ? input.getTime() : Number(input);
  if (!t || t < 946684800000) return "—";
  const sec = Math.max(0, Math.floor((t - now) / 1000));
  return fmtDuration(sec);
}

loadCodebuffData();
setRefresh(refreshMs);
setInterval(updateCountdown, 1000);
updateCountdown();

(function setupAutoSync() {
  // Auto-trigger one sync 1s after the first data load completes, then every 5min.
  setTimeout(() => { syncQuota(); }, 1000);
  autoSyncTimer = setInterval(syncQuota, 5 * 60 * 1000);
})();
