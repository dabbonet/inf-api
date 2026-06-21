// Codebuff Telemetry — vertically rich cards layout with sparklines, progress bars, status pills.

const POLL_INTERVAL = 30000;
let poolData = null;
let metricsData = null;
let expanded = new Set();
let accountFilter = "all"; // "all" or specific account id

function loadCodebuffData() {
  Promise.all([
    fetch("/api/codebuff/pool-status").then((r) => r.ok ? r.json() : null).catch(() => null),
    fetch("/api/codebuff/metrics").then((r) => r.ok ? r.json() : null).catch(() => null),
  ]).then(([pool, metrics]) => {
    poolData = pool;
    metricsData = metrics;
    renderAll();
    updateBanner();
  }).catch((err) => console.error("loadCodebuffData failed", err));
}

function syncQuota() {
  const btn = document.getElementById("syncBtn");
  if (btn) { btn.disabled = true; btn.textContent = "Syncing…"; }
  if (!poolData || !poolData.accounts) {
    setTimeout(() => { if (btn) { btn.disabled = false; btn.textContent = "Sync Quota"; } loadCodebuffData(); }, 200);
    return;
  }
  const syncs = poolData.accounts.map((acc) =>
    fetch("/api/accounts/" + acc.account_id + "/codebuff-sync", { method: "POST" })
      .then((r) => r.ok).catch(() => false)
  );
  Promise.all(syncs).then(() => {
    setTimeout(() => {
      loadCodebuffData();
      if (btn) { btn.disabled = false; btn.textContent = "Sync Quota"; }
    }, 500);
  });
}

function renderAll() {
  renderStats();
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
    acc.tokens += m.total.tokens;
    acc.latencyMs += m.total.latency_ms || 0;
    if (m.total.last_used > acc.newest) acc.newest = m.total.last_used;
    if (acc.oldest === 0 || (m.total.first_used && m.total.first_used < acc.oldest)) acc.oldest = m.total.first_used;
  });
  acc.avgMs = acc.reqs > 0 ? Math.round(acc.latencyMs / acc.reqs) : 0;
  acc.tps = acc.tps || (acc.latencyMs > 0 ? acc.tokens / (acc.latencyMs / 1000) : 0);
  acc.rpm = acc.oldest > 0 && acc.newest > acc.oldest ? (acc.reqs / Math.max(1, (acc.newest - acc.oldest) / 60000)) : 0;
  return acc;
}

function zero() {
  return { reqs: 0, s429: 0, tokens: 0, latencyMs: 0, avgMs: 0, tps: 0, rpm: 0, oldest: 0, newest: 0 };
}

function renderStats() {
  const t = totalAcross(() => true);
  document.getElementById("statReqs").textContent = t.reqs.toLocaleString();
  document.getElementById("stat429s").textContent = t.s429.toLocaleString();
  document.getElementById("statTokens").textContent = shortNumber(t.tokens);
  document.getElementById("statRPM").textContent = t.rpm.toFixed(1);
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
  const accTokens = t.tokens || 0;
  const accLatMs = accReqs > 0 ? Math.round((t.latency_ms || 0) / accReqs) : 0;
  const accTps = t.tokens_per_s || 0;
  const accRpm = (t.first_used && t.last_used && t.last_used > t.first_used)
    ? (accReqs / Math.max(1, (t.last_used - t.first_used) / 60000)) : 0;
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
  if (blockedModels > 0) {
    statusLabel = `${blockedModels} blocked`;
    statusClass = "pill-danger";
  } else if (totalRemaining === 0 && totalLimit > 0) {
    statusLabel = "All exhausted";
    statusClass = "pill-warn";
  } else if (accReqs === 0) {
    statusLabel = "Idle";
    statusClass = "pill-idle";
  } else {
    statusLabel = "Healthy";
    statusClass = "pill-ok";
  }

  const expandedCls = expanded.has(acc.account_id) ? "expanded" : "";

  // Per-model rows.
  const modelRows = models.map((m) => buildModelRow(acc, m, accMetric)).join("");
  const emptyModels = models.length === 0
    ? `<div class="empty-models">No models registered for this account.</div>`
    : "";

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
            <div class="stat-foot">lifetime</div>
          </div>
          <div class="stat">
            <div class="stat-label">429s</div>
            <div class="stat-num ${acc429 > 0 ? 'num-warn' : ''}">${acc429.toLocaleString()}</div>
            <div class="stat-foot">${accReqs > 0 ? ((acc429 / accReqs) * 100).toFixed(1) + '%' : '—'}</div>
          </div>
          <div class="stat">
            <div class="stat-label">Tokens</div>
            <div class="stat-num">${shortNumber(accTokens)}</div>
            <div class="stat-foot">total</div>
          </div>
          <div class="stat">
            <div class="stat-label">Avg Latency</div>
            <div class="stat-num">${accLatMs > 0 ? accLatMs + ' ms' : '—'}</div>
            <div class="stat-foot">per request</div>
          </div>
          <div class="stat">
            <div class="stat-label">Throughput</div>
            <div class="stat-num">${accTps.toFixed(1)} <span class="unit">t/s</span></div>
            <div class="stat-foot">${accTokens ? shortNumber(accTokens / Math.max(1, (t.last_used - t.first_used) / 60)) + ' t/min' : '—'}</div>
          </div>
          <div class="stat">
            <div class="stat-label">Rate</div>
            <div class="stat-num">${accRpm.toFixed(2)} <span class="unit">req/m</span></div>
            <div class="stat-foot">rate/min</div>
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
  const tps = mm && mm.tokens_per_s ? mm.tokens_per_s : 0;
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
setInterval(loadCodebuffData, POLL_INTERVAL);
setInterval(updateCountdown, 1000);
updateCountdown();
