// Accounts management JavaScript

let accounts = [];
let currentPlatform = '';
let accountHealth = {};
let pageSize = 20;
let currentPage = 1;

// DOM Cache
const domCache = {
    accountsList: null,
    paginationInfo: null,
    paginationControls: null,
    accountImportStatus: null,
};

function initDOMCache() {
    domCache.accountsList = document.getElementById("accountsList");
    domCache.paginationInfo = document.getElementById("paginationInfo");
    domCache.paginationControls = document.getElementById("paginationControls");
    domCache.accountImportStatus = document.getElementById("accountImportStatus");
}

// Load accounts from API
async function loadAccounts() {
  try {
    const res = await fetch("/api/accounts");
    if (res.status === 401) {
      window.location.href = "./login.html";
      return;
    }
    accounts = await res.json();
    sortAccounts();
    renderPlatformTabs();
    renderAccounts();
    updateStats();
    autoRefreshWarpAccounts();
  } catch (err) {
    console.error("Failed to load accounts:", err);
    showToast("Failed to load accounts", "error");
  }
}

// Sort accounts (Default by ID desc)
function sortAccounts() {
  accounts.sort((a, b) => b.id - a.id);
}

// Normalize account type
function normalizeAccountType(acc) {
  return normalizeSidebarAccountType(acc);
}

function getQuotaStats(acc) {
  if (!acc) return null;
  const base = getSidebarQuotaStats(acc);
  if (!base) return null;
  if (base.unknown) return base;
  const type = normalizeAccountType(acc);
  if (type === "warp") {
    const monthlyLimit = Math.max(0, Math.floor(acc.warp_monthly_limit || acc.usage_limit || 0));
    const monthlyRemainingRaw = acc.warp_monthly_remaining !== undefined && acc.warp_monthly_remaining !== null
      ? acc.warp_monthly_remaining
      : (monthlyLimit > 0 ? monthlyLimit - Math.floor(acc.usage_current || 0) : 0);
    const monthlyRemaining = Math.max(0, Math.floor(monthlyRemainingRaw || 0));
    const bonusRemaining = Math.max(0, Math.floor(acc.warp_bonus_remaining || 0));
    const remaining = monthlyRemaining + bonusRemaining;
    if (monthlyLimit > 0 || bonusRemaining > 0) {
      const displayTotal = monthlyLimit + bonusRemaining;
      const pctRemaining = displayTotal > 0 ? Math.min(100, Math.round((remaining / displayTotal) * 100)) : 0;
      return {
        supported: true,
        limit: monthlyLimit,
        remaining,
        used: Math.max(0, Math.floor(acc.usage_current || 0)),
        pctRemaining,
        monthlyLimit,
        monthlyRemaining,
        bonusRemaining,
        splitBonus: bonusRemaining > 0,
      };
    }
  }
  const limit = Math.max(0, Math.floor(base.limit || 0));
  const remaining = Math.max(0, Math.floor(base.remaining || 0));
  const used = Math.max(0, limit - remaining);
  const pctRemaining = limit > 0 ? Math.min(100, Math.round((remaining / limit) * 100)) : 0;
  return { ...base, limit, remaining, used, pctRemaining };
}

function getAccountToken(acc) {
  return getSidebarAccountToken(acc);
}

function normalizeAccountSubscription(acc) {
  const raw = String(acc?.subscription || "").trim().toLowerCase();
  if (!raw) return "";
  if (normalizeAccountType(acc) === "warp") {
    if (raw.includes("enterprise") || raw.includes("unlimited")) return "enterprise";
    if (raw.includes("max")) return "max";
    if (raw.includes("business")) return "build/business";
    if (raw.includes("build")) return "build/business";
    if (raw.includes("free")) return "free";
    if (raw.includes("unknown")) return "unknown";
    return raw;
  }
  if (raw.includes("heavy")) return "heavy";
  if (raw.includes("super") || raw.includes("pro")) return "super";
  if (raw.includes("lite")) return "lite";
  if (raw.includes("basic") || raw.includes("free")) return "basic";
  return raw;
}

function subscriptionBadge(acc) {
  const type = normalizeAccountType(acc);
  const level = normalizeAccountSubscription(acc);
  if (!level) {
    return { text: "-", bg: "rgba(100, 116, 139, 0.12)", color: "#94a3b8", tip: "No Subscription Level" };
  }
  if (type === "warp") {
    switch (level) {
      case "enterprise":
        return { text: "Enterprise", bg: "rgba(251, 191, 36, 0.16)", color: "#fbbf24", tip: "Warp Enterprise / Unlimited Quota Tier" };
      case "max":
        return { text: "Max", bg: "rgba(56, 189, 248, 0.16)", color: "#38bdf8", tip: "Warp Max Quota Tier" };
      case "build/business":
        return { text: "Build/Business", bg: "rgba(167, 139, 250, 0.16)", color: "#c4b5fd", tip: "Warp 1,500 credits/month, Build and Business have the same quota" };
      case "free":
        return { text: "Free", bg: "rgba(52, 211, 153, 0.14)", color: "#34d399", tip: "Warp Free Quota Tier" };
      case "unknown":
        return { text: "Unknown", bg: "rgba(100, 116, 139, 0.12)", color: "#94a3b8", tip: "Unidentified Warp Quota Tier" };
      default:
        return { text: level, bg: "rgba(100, 116, 139, 0.12)", color: "#cbd5e1", tip: `Warp Quota Tier: ${level}` };
    }
  }
  if (type !== "grok") {
    return { text: level, bg: "rgba(100, 116, 139, 0.12)", color: "#cbd5e1", tip: `Subscription Level: ${level}` };
  }
  switch (level) {
    case "heavy":
      return { text: "heavy", bg: "rgba(251, 191, 36, 0.16)", color: "#fbbf24", tip: "Grok Heavy Account Pool" };
    case "super":
      return { text: "super", bg: "rgba(56, 189, 248, 0.16)", color: "#38bdf8", tip: "Grok Super Account Pool" };
    case "lite":
      return { text: "lite", bg: "rgba(167, 139, 250, 0.16)", color: "#c4b5fd", tip: "Grok Lite Account Pool" };
    case "basic":
      return { text: "basic", bg: "rgba(52, 211, 153, 0.14)", color: "#34d399", tip: "Grok Basic Account Pool" };
    default:
      return { text: level, bg: "rgba(100, 116, 139, 0.12)", color: "#cbd5e1", tip: `Grok Account Pool: ${level}` };
  }
}

function buildSubscriptionMarkup(acc) {
  const badge = subscriptionBadge(acc);
  return `<span class="tag account-tier-tag" title="${escapeHtml(badge.tip || "")}" style="background:${badge.bg};color:${badge.color};border:none;">${escapeHtml(badge.text)}</span>`;
}

function shouldShowNSFWBadge(acc) {
  return normalizeAccountType(acc) === "grok" && !!acc?.nsfw_enabled;
}

function buildNSFWBadgeMarkup(acc) {
  if (!shouldShowNSFWBadge(acc)) return "";
  return `<span class="tag account-nsfw-tag" title="Grok NSFW Enabled" style="background:rgba(244, 114, 182, 0.14);color:#f472b6;border:none;">NSFW</span>`;
}

function applyTokenLabels(type) {
  const label = document.getElementById("tokenLabel");
  const input = document.getElementById("clientCookie");
  const hint = document.getElementById("tokenHint");
  const warpImportActions = document.getElementById("warpLocalImportActions");
  const usageLimitGroup = document.getElementById("usageLimitGroup");
  const accountId = String(document.getElementById("accountId")?.value || "");
  if (!label || !input || !hint) return;
  if (warpImportActions) {
    warpImportActions.hidden = type !== "warp";
  }
  if (usageLimitGroup) {
    usageLimitGroup.hidden = type !== "aihubmix" && type !== "zenmux";
  }
  if (type === 'warp') {
    label.textContent = "Warp Auth";
    input.placeholder = "One id_token.refresh_token, login callback URL, or User JSON per line";
    hint.textContent = accountId
      ? "Only the first line is saved during editing; you can paste warp://auth/... callback URL / User JSON / id_token.refresh_token"
      : "Supports bulk addition for Warp. You can paste warp://auth/... callback URL / User JSON / id_token.refresh_token";
    input.required = true;
  } else if (type === 'grok') {
    label.textContent = "SSO Token";
    input.placeholder = "One sso token (or Cookie containing sso=) per line";
    hint.textContent = accountId
      ? "Only the first line SSO Token is saved during editing"
      : "Supports bulk addition for Grok. One sso token or Cookie segment per line";
  } else if (type === 'puter') {
      label.textContent = "Auth Token";
      input.placeholder = "One Puter auth_token per line";
      hint.textContent = accountId
        ? "Only the first line auth_token is saved during Puter editing. Get it at https://docs.puter.com/playground/ai-chatgpt/"
        : "Supports bulk addition for Puter. One auth_token per line; get it at https://docs.puter.com/playground/ai-chatgpt/";
      input.required = true;
    } else if (type === 'codebuff') {
      label.textContent = "Bearer Token";
      input.placeholder = "One Codebuff Bearer token per line";
      hint.textContent = accountId
        ? "Only the first line Bearer token is saved during Codebuff editing."
        : "Supports bulk addition for Codebuff. One Bearer token per line; requests are passed through unchanged.";
      input.required = true;
    } else if (type === 'aihubmix') {
    label.textContent = "Aihubmix API Key";
    input.placeholder = "One Aihubmix API key per line (sk-...)";
    hint.textContent = accountId
      ? "Only the first line API key is saved during editing. Get your key at https://aihubmix.com"
      : "Supports bulk addition for Aihubmix. One API key per line; get it at https://aihubmix.com";
    input.required = true;
  } else if (type === 'zenmux') {
    label.textContent = "Zenmux API Key";
    input.placeholder = "One Zenmux API key per line";
    hint.textContent = accountId
      ? "Only the first line API key is saved during editing. Get your key at https://zenmux.ai"
      : "Supports bulk addition for Zenmux. One API key per line; get it at https://zenmux.ai";
    input.required = true;
  } else {
    label.textContent = "Cookie / __client / __session";
    input.placeholder = "Supports raw __client, full Cookie Header, or Cookie JSON";
    hint.textContent = accountId
      ? "Supports directly pasting raw __client; full Cookie has a higher success rate"
      : "Supports raw __client, full Cookie Header, or Cookie JSON; recommend bringing __client_uat to improve completion success rate";
    input.required = true;
  }
}

function selectWarpUserFile() {
  const input = document.getElementById("warpUserFileInput");
  if (!input) return;
  input.value = "";
  input.click();
}

async function importWarpUserFile(file) {
  if (!file) return;
  const typeEl = document.getElementById("accountType");
  if (String(typeEl?.value || "").toLowerCase() !== "warp") return;

  try {
    renderAccountImportStatus("Uploading and parsing WARP User JSON / token...", "info", [file.name]);
    const form = new FormData();
    form.append("file", file, file.name || "dev.warp.Warp-User");
    const res = await fetch("/api/warp/import-user-file", {
      method: "POST",
      body: form,
    });
    if (!res.ok) throw new Error((await res.text()).trim() || "Upload import failed");
    const account = await res.json();
    renderAccountImportStatus("Parsed and saved Warp account", "info", [`Account #${account.id || ""}`.trim()]);
    showToast("Saved uploaded WARP account");
    closeModal();
    loadAccounts();
  } catch (err) {
    renderAccountImportStatus("Failed to upload User JSON / token", "error", [err.message || String(err)]);
    showToast("Upload import failed: " + (err.message || String(err)), "error");
  }
}

function splitBatchCredentialInput(raw) {
  const text = String(raw || "").trim();
  if (!text) return [];
  if (/^[\[{]/.test(text)) {
    return [text];
  }
  const lines = text
    .split(/\r?\n/)
    .map(line => line.trim())
    .filter(Boolean);
  if (lines.length > 1) {
    return lines;
  }
  return [text];
}

function normalizeCredentialForType(type, credential) {
  const normalizedType = String(type || "").trim().toLowerCase();
  const raw = String(credential || "").trim();
  if (!raw) return "";

  if (normalizedType === "warp") {
    try {
      const parsed = JSON.parse(raw);
      const token = findNestedWarpRefreshToken(parsed);
      if (token) return token;
    } catch (_) {
      // Not JSON; continue with URL/cookie/form extraction.
    }
    const match = raw.match(/(?:^|[?&;\s])refresh_token=([^&;\s]+)/i);
    return (match ? decodeURIComponent(match[1]) : raw).trim();
  }

  if (normalizedType === "grok") {
    const ssoMatch = raw.match(/(?:^|[;\s])sso=([^;\s]+)/i);
    return (ssoMatch ? ssoMatch[1] : raw).trim();
  }

  return raw;
}

function findNestedWarpRefreshToken(value) {
  if (!value || typeof value !== "object") return "";
  const preferred = ["id_token", "auth_tokens", "authTokens"];
  for (const key of preferred) {
    if (value[key]) {
      const token = findNestedWarpRefreshToken(value[key]);
      if (token) return token;
    }
  }
  for (const [key, item] of Object.entries(value)) {
    const normalizedKey = String(key || "").toLowerCase();
    if (normalizedKey === "refresh_token" || normalizedKey === "refreshtoken") {
      const token = String(item || "").trim();
      if (token) return token;
    }
    const token = findNestedWarpRefreshToken(item);
    if (token) return token;
  }
  return "";
}

function buildCredentialFingerprint(type, credential) {
  const normalizedType = String(type || "").trim().toLowerCase();
  const normalizedCredential = normalizeCredentialForType(normalizedType, credential);
  if (!normalizedType || !normalizedCredential) return "";
  return `${normalizedType}:${normalizedCredential}`;
}

function collectExistingCredentialFingerprints(type, excludeId = "") {
  const normalizedType = String(type || "").trim().toLowerCase();
  const excluded = String(excludeId || "").trim();
  const seen = new Set();
  (Array.isArray(accounts) ? accounts : []).forEach((acc) => {
    if (!acc) return;
    if (String(acc.id || "") === excluded) return;
    if (normalizeAccountType(acc) !== normalizedType) return;
    const token = getAccountToken(acc);
    const key = buildCredentialFingerprint(normalizedType, token);
    if (key) seen.add(key);
  });
  return seen;
}

function dedupeCredentialInputs(type, credentials) {
  const unique = [];
  const duplicates = [];
  const seen = new Set();

  (Array.isArray(credentials) ? credentials : []).forEach((credential) => {
    const trimmed = String(credential || "").trim();
    if (!trimmed) return;
    const key = buildCredentialFingerprint(type, trimmed) || `raw:${trimmed}`;
    if (seen.has(key)) {
      duplicates.push(trimmed);
      return;
    }
    seen.add(key);
    unique.push(trimmed);
  });

  return { unique, duplicates };
}

function filterExistingCredentialConflicts(type, credentials, excludeId = "") {
  const existing = collectExistingCredentialFingerprints(type, excludeId);
  const accepted = [];
  const conflicts = [];

  (Array.isArray(credentials) ? credentials : []).forEach((credential) => {
    const trimmed = String(credential || "").trim();
    if (!trimmed) return;
    const key = buildCredentialFingerprint(type, trimmed);
    if (key && existing.has(key)) {
      conflicts.push(trimmed);
      return;
    }
    accepted.push(trimmed);
  });

  return { accepted, conflicts };
}

function getAccountImportStatusNode() {
  return domCache.accountImportStatus || document.getElementById("accountImportStatus");
}

function escapeImportStatusText(text) {
  return String(text || "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;");
}

function clearAccountImportStatus() {
  const node = getAccountImportStatusNode();
  if (!node) return;
  node.hidden = true;
  node.classList.remove("is-active", "is-error");
  node.innerHTML = "";
}

function renderAccountImportStatus(message, type = "info", details = []) {
  const node = getAccountImportStatusNode();
  if (!node) return;

  const safeMessage = escapeImportStatusText(message);
  const rows = Array.isArray(details) ? details.filter(Boolean).slice(0, 8) : [];
  const detailHTML = rows.length > 0
    ? `<div style="margin-top:8px">${rows.map((item) => `<div><code>${escapeImportStatusText(item)}</code></div>`).join("")}</div>`
    : "";

  node.hidden = false;
  node.classList.toggle("is-active", type === "info");
  node.classList.toggle("is-error", type === "error");
  node.innerHTML = `<strong>${safeMessage}</strong>${detailHTML}`;
}

function buildAccountPayload(type, baseData, credential) {
  const payload = { ...baseData };
  if (type === "warp") {
    payload.refresh_token = credential;
  } else {
    payload.client_cookie = credential;
  }
  if (type === "aihubmix" || type === "zenmux") {
    const usageInput = document.getElementById("usageLimit");
    if (usageInput) {
      const raw = String(usageInput.value || "").trim();
      const parsed = raw === "" ? 0 : Number(raw);
      if (Number.isFinite(parsed) && parsed >= 0) {
        payload.usage_limit = parsed;
      }
    }
  }
  return payload;
}

function accountTypeLabel(type) {
  switch (String(type || "").trim().toLowerCase()) {
    case "puter":
      return "Puter";
    case "codebuff":
      return "Codebuff";
    case "warp":
      return "Warp";
    case "grok":
      return "Grok";
    case "aihubmix":
      return "Aihubmix";
    case "zenmux":
      return "Zenmux";
    default:
      return "Unknown";
  }
}

function getActiveAccountType() {
  const platform = String(currentPlatform || "").trim().toLowerCase();
  return platform || "puter";
}

function setAccountModalType(type) {
  const normalized = String(type || "puter").trim().toLowerCase() || "puter";
  const typeEl = document.getElementById("accountType");
  const displayEl = document.getElementById("accountTypeDisplay");
  if (typeEl) typeEl.value = normalized;
  if (displayEl) displayEl.value = accountTypeLabel(normalized);
  applyTokenLabels(normalized);
}

async function createAccount(payload) {
  const res = await fetch("/api/accounts", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Account-Sync": "async",
    },
    body: JSON.stringify(payload),
  });
  if (!res.ok) {
    throw new Error(await res.text());
  }
  return res.json();
}

function summarizeAccountCreateError(err) {
  const message = String(err && err.message ? err.message : err || "").trim();
  if (!message) return "Unknown Error";
  const compact = message.replace(/\s+/g, " ");
  return compact.length > 160 ? `${compact.slice(0, 157)}...` : compact;
}

async function runAccountCreatePool(payloads, concurrency = 6, onProgress = null) {
  let nextIndex = 0;
  let success = 0;
  let failed = 0;
  let completed = 0;
  const failures = [];
  const size = Math.max(1, Math.min(concurrency, payloads.length || 1));

  async function worker() {
    while (nextIndex < payloads.length) {
      const currentIndex = nextIndex;
      nextIndex += 1;
      const payload = payloads[currentIndex];
      try {
        await createAccount(payload);
        success += 1;
      } catch (err) {
        failed += 1;
        failures.push(`#${currentIndex + 1} ${summarizeAccountCreateError(err)}`);
        console.error("Failed to create account:", err);
      } finally {
        completed += 1;
        if (typeof onProgress === "function") {
          onProgress({
            total: payloads.length,
            completed,
            success,
            failed,
            currentIndex,
            payload,
            failures,
          });
        }
      }
    }
  }

  await Promise.all(Array.from({ length: size }, () => worker()));
  return { success, failed, failures };
}

// Render platform filter tabs
function renderPlatformTabs() {
  const container = document.getElementById("platformFilters");
  if (!container) return;
  const defaultTypes = ["puter", "codebuff"];
  const types = new Set([...defaultTypes, ...accounts.map(normalizeAccountType)]);
  const sorted = Array.from(types).sort();
  const tabs = [...sorted];

  if (currentPlatform === '' || !tabs.includes(currentPlatform)) {
    currentPlatform = tabs.length > 0 ? tabs[0] : '';
  }

  container.innerHTML = "";
  tabs.forEach(type => {
    const label = String(type || "");
    const isActive = currentPlatform === label;
    const btn = document.createElement("button");
    btn.className = `tab-item ${isActive ? 'active' : ''}`.trim();
    btn.dataset.platform = encodeURIComponent(label);
    btn.textContent = label;
    btn.addEventListener("click", () => {
      const raw = btn.dataset.platform ? decodeURIComponent(btn.dataset.platform) : "";
      filterByPlatform(raw);
    });
    container.appendChild(btn);
  });
}

// Update account health status
function updateAccountHealth(id, ok, msg = '') {
  accountHealth[id] = {
    ok,
    msg,
    checkedAt: new Date().toISOString(),
  };
}

function evaluateAccountStatus(acc) {
  const health = accountHealth[acc.id];
  if (health && !health.ok) {
    return { normal: false, text: 'Error', color: '#fb7185', bg: 'rgba(251, 113, 133, 0.16)', tip: health.msg || 'Status sync failed' };
  }
  if (!acc.enabled) {
    return { normal: false, text: 'Disabled', color: '#fb7185', bg: 'rgba(251, 113, 133, 0.16)', tip: 'Account disabled' };
  }
  const statusCode = normalizeSidebarStatusCode(acc.status_code);
  if (isQuotaOnlyStatus(acc)) {
    const quota = getQuotaStats(acc);
    const limitText = quota && quota.limit > 0 ? quota.limit.toLocaleString() : 'Unknown';
    const type = normalizeAccountType(acc);
    const providerName = accountTypeLabel(type);
    return {
      normal: true,
      text: 'Out of Quota',
      color: '#f59e0b',
      bg: 'rgba(245, 158, 11, 0.16)',
      tip: providerName + ' quota exhausted or insufficient balance, scheduler will temporarily skip this account (Remaining 0 / ' + limitText + ')',
      quotaOnly: true,
    };
  }
  if (statusCode) {
    switch (statusCode) {
      case '429':
        return { normal: false, text: 'Rate Limited', color: '#f59e0b', bg: 'rgba(245, 158, 11, 0.16)', tip: 'Too Many Requests (429)' };
      case 401:
        return { normal: false, text: 'Unauthorized', color: '#fb7185', bg: 'rgba(251, 113, 133, 0.16)', tip: 'Authentication failed (401)' };
      case 403:
        return { normal: false, text: 'Forbidden', color: '#fb7185', bg: 'rgba(251, 113, 133, 0.16)', tip: 'Access denied (403)' };
      case 404:
        return { normal: false, text: 'Not Found', color: '#fb7185', bg: 'rgba(251, 113, 133, 0.16)', tip: 'Resource not found (404)' };
      default:
        return { normal: false, text: 'Error', color: '#fb7185', bg: 'rgba(251, 113, 133, 0.16)', tip: 'Status error: ' + statusCode };
    }
  }

  const type = normalizeAccountType(acc);
  if (type === 'warp') {
    if (!getAccountToken(acc)) {
      return { normal: false, text: 'Incomplete', color: '#f59e0b', bg: 'rgba(245, 158, 11, 0.16)', tip: 'Missing Refresh Token' };
    }
  } else if (type === 'grok') {
    if (!getAccountToken(acc)) {
      return { normal: false, text: 'Incomplete', color: '#f59e0b', bg: 'rgba(245, 158, 11, 0.16)', tip: 'Missing SSO Token' };
    }
  } else if (type === 'puter') {
    if (!getAccountToken(acc)) {
      return { normal: false, text: 'Incomplete', color: '#f59e0b', bg: 'rgba(245, 158, 11, 0.16)', tip: 'Missing Puter auth_token' };
    }
  } else if (type === 'aihubmix' || type === 'zenmux') {
    if (!getAccountToken(acc)) {
      return { normal: false, text: 'Incomplete', color: '#f59e0b', bg: 'rgba(245, 158, 11, 0.16)', tip: 'Missing API key' };
    }
  }

  const quota = getQuotaStats(acc);
  if (quota && quota.limit > 0 && quota.remaining <= 0) {
    if (normalizeAccountType(acc) === 'puter' || normalizeAccountType(acc) === 'warp') {
      const providerName = normalizeAccountType(acc) === 'warp' ? 'Warp' : 'Puter';
      return {
        normal: true,
        text: 'Out of Quota',
        color: '#f59e0b',
        bg: 'rgba(245, 158, 11, 0.16)',
        tip: providerName + ' quota exhausted or insufficient balance, scheduler will temporarily skip this account (Remaining 0 / ' + quota.limit.toLocaleString() + ')',
        quotaOnly: true,
      };
    }
    return { normal: false, text: 'Quota Full', color: '#fb7185', bg: 'rgba(251, 113, 133, 0.16)', tip: 'Quota exhausted (Remaining 0 / ' + quota.limit.toLocaleString() + ')' };
  }

  return { normal: true, text: 'Normal', color: '#34d399', bg: 'rgba(52, 211, 153, 0.16)', tip: 'Status normal' };
}

function isAccountAbnormal(acc) {
  return !evaluateAccountStatus(acc).normal;
}

function matchesCurrentPlatform(acc) {
  if (!currentPlatform) return true;
  const key = String(currentPlatform || "").toLowerCase();
  return normalizeAccountType(acc).includes(key);
}

// Get status badge for account
function statusBadge(acc) {
  return evaluateAccountStatus(acc);
}

// Refresh single account via the shared check endpoint.
async function checkAccount(id, silent = false, actionText = "Refresh") {
  const action = "check";
  try {
    const res = await fetch(`/api/accounts/${id}/${action}`);
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const updated = await res.json();
    accounts = accounts.map(a => (a.id === id ? updated : a));
    updateAccountHealth(id, true);
    if (!silent) showToast(`Account ${updated.name || updated.email || id} ${actionText} successful`, "success");
  } catch (err) {
    try {
      const latestRes = await fetch(`/api/accounts/${id}`);
      if (latestRes.ok) {
        const latest = await latestRes.json();
        accounts = accounts.map(a => (a.id === id ? latest : a));
        delete accountHealth[id];
      } else {
        updateAccountHealth(id, false, err.message || String(err));
      }
    } catch (_) {
      updateAccountHealth(id, false, err.message || String(err));
    }
    if (!silent) showToast(`Account ${id} ${actionText} failed`, "error");
  } finally {
    renderAccounts();
    updateStats();
  }
}

async function autoRefreshWarpAccounts() {
  const warpAccounts = accounts.filter(acc => normalizeAccountType(acc) === 'warp');
  if (!warpAccounts.length) return;
  for (const acc of warpAccounts) {
    if (acc.token) continue;
    await checkAccount(acc.id, true);
  }
}

// Delete all accounts
async function deleteAllAccounts() {
  if (!accounts.length) return;
  if (!confirm(`Are you sure you want to delete all ${accounts.length} accounts? This action cannot be undone.`)) return;
  for (const acc of accounts) {
    await fetch(`/api/accounts/${acc.id}`, { method: "DELETE" });
  }
  await loadAccounts();
  showToast("All accounts deleted", "success");
}

// Clear abnormal accounts
async function clearAbnormalAccounts() {
  const abnormal = accounts.filter((acc) => matchesCurrentPlatform(acc) && isAccountAbnormal(acc));
  if (abnormal.length === 0) {
    showToast(currentPlatform ? `No abnormal accounts found on current ${currentPlatform} page` : "No abnormal accounts found", "info");
    return;
  }
  const scopeText = currentPlatform ? `in current ${currentPlatform} page ` : "";
  if (confirm(`Are you sure you want to clear ${abnormal.length} abnormal accounts ${scopeText}?`)) {
    for (const acc of abnormal) {
      await fetch(`/api/accounts/${acc.id}`, { method: "DELETE" });
    }
    loadAccounts();
    showToast(`Cleared ${scopeText}abnormal accounts`);
  }
}

// Batch delete accounts
async function batchDeleteAccounts() {
  const selected = Array.from(document.querySelectorAll(".row-checkbox:checked")).map(cb => cb.dataset.id);
  if (selected.length === 0) return;
  if (confirm(`Are you sure you want to delete ${selected.length} selected accounts?`)) {
    for (const id of selected) {
      await fetch(`/api/accounts/${id}`, { method: "DELETE" });
    }
    loadAccounts();
    showToast(`Successfully deleted ${selected.length} accounts`);
  }
}

function getSelectedAccountIDs() {
  return Array.from(document.querySelectorAll(".row-checkbox:checked"))
    .map((cb) => parseDataId(cb.dataset.id || ""))
    .map((id) => Number(id))
    .filter((id) => Number.isFinite(id) && id > 0);
}

// Enable NSFW for selected Grok accounts, or all Grok accounts when nothing selected.
async function enableNSFW() {
  const selectedIDs = getSelectedAccountIDs();
  const selectedGrokIDs = selectedIDs.filter((id) => {
    const acc = accounts.find((item) => item.id === id);
    return !!acc && normalizeAccountType(acc) === "grok";
  });

  const payload = { concurrency: 5 };
  let targetText = "All Grok Accounts";

  if (selectedIDs.length > 0) {
    if (selectedGrokIDs.length === 0) {
      showToast("No Grok accounts in the selected accounts", "info");
      return;
    }
    payload.account_ids = selectedGrokIDs;
    targetText = `selected ${selectedGrokIDs.length} Grok accounts`;
    if (!confirm(`Are you sure you want to enable NSFW for ${targetText}?`)) return;
  } else if (!confirm("No accounts selected, NSFW will be enabled for all Grok accounts, continue?")) {
    return;
  }

  try {
    showToast(`Enabling NSFW for ${targetText}...`, "info");
    const res = await fetch("/api/v1/admin/tokens/nsfw/enable", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });

    if (res.status === 401) {
      window.location.href = "./login.html";
      return;
    }
    if (!res.ok) {
      throw new Error(await res.text());
    }

    const out = await res.json();
    const summary = out && out.summary ? out.summary : {};
    const total = Number(summary.total || 0);
    const ok = Number(summary.ok || 0);
    const fail = Number(summary.fail || Math.max(0, total - ok));

    if (ok > 0) {
      await loadAccounts();
    }

    if (fail > 0) {
      const failedList = Object.entries(out && out.results ? out.results : {})
        .filter(([, item]) => !item || item.success !== true)
        .slice(0, 3)
        .map(([token, item]) => {
          const msg = item && item.error ? item.error : `HTTP ${item && item.http_status ? item.http_status : 0}`;
          return `${token}: ${msg}`;
        });
      if (failedList.length > 0) {
        console.warn("NSFW enable failures:", failedList.join(" | "));
      }
      showToast(`NSFW enabling complete: ${ok} successful, ${fail} failed`, "error");
      return;
    }

    showToast(`NSFW enabled successfully: ${ok}/${total} total`, "success");
  } catch (err) {
    showToast(`NSFW enable failed: ${err.message || err}`, "error");
  }
}

// Render accounts table
function renderAccounts() {
  const container = domCache.accountsList || document.getElementById("accountsList");
  const filtered = accounts.filter(matchesCurrentPlatform);

  const total = filtered.length;
  const totalPages = Math.ceil(total / pageSize) || 1;
  if (currentPage > totalPages) currentPage = totalPages;
  if (currentPage < 1) currentPage = 1;

  const start = (currentPage - 1) * pageSize;
  const end = start + pageSize;
  const pageItems = filtered.slice(start, end);

  if (pageItems.length === 0) {
    container.innerHTML = "";
    const empty = document.createElement("div");
    empty.className = "empty-state empty-state-panel";
    const icon = document.createElement("span");
    icon.className = "empty-state-mark";
    icon.textContent = "EMPTY";
    const text = document.createElement("p");
    text.textContent = `No ${currentPlatform ? currentPlatform : ''} account data`;
    empty.appendChild(icon);
    empty.appendChild(text);
    container.appendChild(empty);
    const paginationInfo = domCache.paginationInfo || document.getElementById("paginationInfo");
    paginationInfo.textContent = `Total 0 records, Page 1/1`;
    renderPagination(1, 1);
    return;
  }

  if (window.matchMedia("(max-width: 640px)").matches) {
    renderAccountsMobile(container, pageItems, total, totalPages);
    return;
  }

  container.innerHTML = "";
  const wrap = document.createElement("div");
  wrap.className = "table-wrap";
  const table = document.createElement("table");
  table.className = "accounts-table";
  const thead = document.createElement("thead");
  const headRow = document.createElement("tr");
  const headers = [
    { label: "", style: "width: 40px;" },
    { label: "ID", style: "width: 60px;" },
    { label: "Token" },
    { label: "Level", style: "width: 90px;" },
    { label: "Quota", style: "width: 140px;" },
    { label: "Status" },
    { label: "Calls" },
    { label: "Last Call" },
    { label: "Action", style: "text-align: right;" },
  ];
  headers.forEach((h, idx) => {
    const th = document.createElement("th");
    if (h.style) th.style.cssText = h.style;
    if (h.label === "Token") th.classList.add("col-token");
    if (idx === 0) {
      const selectAll = document.createElement("input");
      selectAll.type = "checkbox";
      selectAll.dataset.action = "select-all";
      th.appendChild(selectAll);
    } else {
      th.textContent = h.label;
    }
    headRow.appendChild(th);
  });
  thead.appendChild(headRow);
  table.appendChild(thead);

  const tbody = document.createElement("tbody");
  
  // Use DocumentFragment to batch build table rows
  const fragment = document.createDocumentFragment();
  pageItems.forEach((acc) => {
    const badge = statusBadge(acc);
    const tokenDisplay = formatTokenDisplay(acc);
    const tr = document.createElement("tr");

    const tdCheck = document.createElement("td");
    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.className = "row-checkbox";
    cb.dataset.action = "row-select";
    cb.dataset.id = encodeData(acc.id);
    tdCheck.appendChild(cb);
    tr.appendChild(tdCheck);

    const tdID = document.createElement("td");
    tdID.style.color = "#64748b";
    tdID.style.fontSize = "0.9rem";
    tdID.textContent = acc.id === null || acc.id === undefined ? "" : String(acc.id);
    tr.appendChild(tdID);

    const tdToken = document.createElement("td");
    tdToken.className = "col-token";
    const tokenSpan = document.createElement("span");
    tokenSpan.className = "token-text";
    tokenSpan.title = tokenDisplay;
    tokenSpan.style.fontFamily = "monospace";
    tokenSpan.style.color = "#94a3b8";
    tokenSpan.textContent = tokenDisplay;
    tdToken.appendChild(tokenSpan);
    tr.appendChild(tdToken);

    const tdTier = document.createElement("td");
    tdTier.innerHTML = buildSubscriptionMarkup(acc);
    tr.appendChild(tdTier);

    const tdQuota = document.createElement("td");
    tdQuota.style.fontSize = "0.85rem";
    const quota = getQuotaStats(acc);
    if (quota && quota.unknown) {
      tdQuota.style.color = "#64748b";
      const hint = quota.hint || "Puter has no stable quota API";
      tdQuota.innerHTML = `<span>Unknown</span> <span style="color:#64748b;font-size:0.75rem">(${hint})</span>`;
    } else if (quota) {
      const pct = quota.pctRemaining;
      const color = pct <= 10 ? "#fb7185" : pct <= 30 ? "#f59e0b" : "#34d399";
      if (normalizeAccountType(acc) === "warp" && quota.splitBonus) {
        tdQuota.innerHTML = `<span style="color:${color}">${quota.remaining.toLocaleString()}</span> <span style="color:#64748b;font-size:0.75rem">(Remaining)</span><div style="color:#64748b;font-size:0.75rem">${quota.monthlyRemaining.toLocaleString()} Monthly + ${quota.bonusRemaining.toLocaleString()} Bonus</div>`;
      } else {
        tdQuota.innerHTML = `<span style="color:${color}">${quota.remaining.toLocaleString()} / ${quota.limit.toLocaleString()}</span> <span style="color:#64748b;font-size:0.75rem">(Remaining)</span>`;
      }
    } else {
      tdQuota.style.color = "#64748b";
      tdQuota.textContent = "-";
    }
    tr.appendChild(tdQuota);

    const tdStatus = document.createElement("td");
    const statusWrap = document.createElement("div");
    statusWrap.style.display = "flex";
    statusWrap.style.alignItems = "center";
    statusWrap.style.gap = "6px";

    const statusSpan = document.createElement("span");
    statusSpan.className = "tag tag-status-normal";
    statusSpan.title = badge.tip || "";
    statusSpan.style.background = badge.bg;
    statusSpan.style.color = badge.color;
    statusSpan.style.border = "none";
    statusSpan.textContent = badge.text;
    statusWrap.appendChild(statusSpan);

    if (shouldShowNSFWBadge(acc)) {
      const nsfwSpan = document.createElement("span");
      nsfwSpan.className = "tag account-nsfw-tag";
      nsfwSpan.title = "Grok NSFW Enabled";
      nsfwSpan.style.background = "rgba(244, 114, 182, 0.14)";
      nsfwSpan.style.color = "#f472b6";
      nsfwSpan.style.border = "none";
      nsfwSpan.textContent = "NSFW";
      statusWrap.appendChild(nsfwSpan);
    }

    tdStatus.appendChild(statusWrap);
    tr.appendChild(tdStatus);

    const tdCount = document.createElement("td");
    tdCount.style.fontSize = "0.9rem";
    tdCount.style.color = "#e2e8f0";
    tdCount.style.fontWeight = "500";
    tdCount.textContent = String(acc.request_count || 0);
    tr.appendChild(tdCount);

    const tdLast = document.createElement("td");
    tdLast.style.fontSize = "0.8rem";
    tdLast.style.color = "#64748b";
    tdLast.textContent = acc.last_used_at && !acc.last_used_at.startsWith('0001') ? formatTime(acc.last_used_at) : "-";
    tr.appendChild(tdLast);

    const tdActions = document.createElement("td");
    tdActions.style.textAlign = "right";
    const actionWrap = document.createElement("div");
    actionWrap.style.display = "flex";
    actionWrap.style.justifyContent = "flex-end";
    actionWrap.style.gap = "12px";

    const edit = document.createElement("i");
    edit.className = "action-icon";
    edit.dataset.action = "edit";
    edit.dataset.id = encodeData(acc.id);
    edit.title = "Edit";
    edit.textContent = "Edit";

    const refresh = document.createElement("i");
    refresh.className = "action-icon";
    refresh.dataset.action = "refresh";
    refresh.dataset.id = encodeData(acc.id);
    refresh.title = "Refresh";
    refresh.textContent = "Sync";

    const del = document.createElement("i");
    del.className = "action-icon";
    del.dataset.action = "delete";
    del.dataset.id = encodeData(acc.id);
    del.title = "Delete";
    del.textContent = "Del";

    actionWrap.appendChild(edit);
    actionWrap.appendChild(refresh);
    actionWrap.appendChild(del);
    tdActions.appendChild(actionWrap);
    tr.appendChild(tdActions);

    // Add row to fragment instead of directly to tbody
    fragment.appendChild(tr);
  });
  
  // Insert all rows into tbody at once
  tbody.appendChild(fragment);
  table.appendChild(tbody);
  wrap.appendChild(table);
  container.appendChild(wrap);

  const paginationInfo = domCache.paginationInfo || document.getElementById("paginationInfo");
  paginationInfo.textContent = `Total ${total} records, Page ${currentPage}/${totalPages}`;
  renderPagination(currentPage, totalPages);
  updateSelectedCount();

  container.onclick = (e) => {
    const actionEl = e.target.closest("[data-action]");
    if (!actionEl || !container.contains(actionEl)) return;
    const action = actionEl.dataset.action;
    const idRaw = actionEl.dataset.id || "";
    const id = parseDataId(idRaw);
    if (action === "edit") editAccount(id);
    if (action === "refresh") refreshToken(id);
    if (action === "delete") deleteAccount(id);
  };

  container.onchange = (e) => {
    const target = e.target;
    if (!(target instanceof HTMLInputElement)) return;
    const action = target.dataset.action;
    if (action === "row-select") {
      updateSelectedCount();
      return;
    }
    if (action === "select-all") {
      toggleSelectAll(target.checked);
    }
  };
}

function buildQuotaMarkup(acc) {
  const quota = getQuotaStats(acc);
  if (quota && quota.unknown) {
    return `<span>Unknown</span> <span style="color:#64748b;font-size:0.75rem">(Puter has no stable quota API)</span>`;
  }
  if (quota) {
    const pct = quota.pctRemaining;
    const color = pct <= 10 ? "#fb7185" : pct <= 30 ? "#f59e0b" : "#34d399";
    if (normalizeAccountType(acc) === "warp" && quota.splitBonus) {
      return `<span style="color:${color}">${quota.remaining.toLocaleString()}</span> <span style="color:#64748b;font-size:0.75rem">(Remaining)</span><div style="color:#64748b;font-size:0.75rem">${quota.monthlyRemaining.toLocaleString()} Monthly + ${quota.bonusRemaining.toLocaleString()} Bonus</div>`;
    }
    return `<span style="color:${color}">${quota.remaining.toLocaleString()} / ${quota.limit.toLocaleString()}</span> <span style="color:#64748b;font-size:0.75rem">(Remaining)</span>`;
  }
  return `<span style="color:#64748b">-</span>`;
}

function buildStatusMarkup(acc, badge) {
  return `<span class="tag" title="${escapeHtml(badge.tip || "")}" style="background:${badge.bg};color:${badge.color};border:none;">${escapeHtml(badge.text)}</span>${buildNSFWBadgeMarkup(acc)}`;
}

function renderAccountsMobile(container, pageItems, total, totalPages) {
  container.innerHTML = "";
  const list = document.createElement("div");
  list.className = "accounts-mobile-list";

  const fragment = document.createDocumentFragment();
  pageItems.forEach((acc) => {
    const badge = statusBadge(acc);
    const tokenDisplay = formatTokenDisplay(acc);
    const card = document.createElement("article");
    card.className = "account-mobile-card";
    card.innerHTML = `
      <div class="account-mobile-head">
        <label class="account-mobile-check">
          <input type="checkbox" class="row-checkbox" data-action="row-select" data-id="${encodeData(acc.id)}">
          <span>#${escapeHtml(acc.id === null || acc.id === undefined ? "" : String(acc.id))}</span>
        </label>
        <div class="account-mobile-actions">
          <button type="button" class="action-icon" data-action="edit" data-id="${encodeData(acc.id)}" title="Edit">Edit</button>
          <button type="button" class="action-icon" data-action="refresh" data-id="${encodeData(acc.id)}" title="Sync">Sync</button>
          <button type="button" class="action-icon" data-action="delete" data-id="${encodeData(acc.id)}" title="Delete">Del</button>
        </div>
      </div>
      <div class="account-mobile-token">
        <span class="token-text" title="${escapeHtml(tokenDisplay)}">${escapeHtml(tokenDisplay)}</span>
      </div>
      <div class="account-mobile-grid">
        <div class="account-mobile-item">
          <span class="account-mobile-label">Status</span>
          <div class="account-mobile-inline">${buildStatusMarkup(acc, badge)}</div>
        </div>
        <div class="account-mobile-item">
          <span class="account-mobile-label">Level</span>
          <div class="account-mobile-inline">${buildSubscriptionMarkup(acc)}</div>
        </div>
        <div class="account-mobile-item">
          <span class="account-mobile-label">Quota</span>
          <div class="account-mobile-value">${buildQuotaMarkup(acc)}</div>
        </div>
        <div class="account-mobile-item">
          <span class="account-mobile-label">Calls</span>
          <span class="account-mobile-value">${escapeHtml(String(acc.request_count || 0))}</span>
        </div>
        <div class="account-mobile-item">
          <span class="account-mobile-label">Last Call</span>
          <span class="account-mobile-value">${escapeHtml(acc.last_used_at && !acc.last_used_at.startsWith("0001") ? formatTime(acc.last_used_at) : "-")}</span>
        </div>
      </div>
    `;
    fragment.appendChild(card);
  });

  list.appendChild(fragment);
  container.appendChild(list);

  const paginationInfo = domCache.paginationInfo || document.getElementById("paginationInfo");
  paginationInfo.textContent = `Total ${total} records, Page ${currentPage}/${totalPages}`;
  renderPagination(currentPage, totalPages);
  updateSelectedCount();

  container.onclick = (e) => {
    const actionEl = e.target.closest("[data-action]");
    if (!actionEl || !container.contains(actionEl)) return;
    const action = actionEl.dataset.action;
    const id = parseDataId(actionEl.dataset.id || "");
    if (action === "edit") editAccount(id);
    if (action === "refresh") refreshToken(id);
    if (action === "delete") deleteAccount(id);
  };

  container.onchange = (e) => {
    const target = e.target;
    if (!(target instanceof HTMLInputElement)) return;
    if (target.dataset.action === "row-select") {
      updateSelectedCount();
    }
  };
}

function renderPagination(current, total) {
  const container = domCache.paginationControls || document.getElementById("paginationControls");
  if (!container) return;

  container.innerHTML = "";
  const appendButton = (label, page, disabled, activeClass, extraStyle) => {
    const btn = document.createElement("button");
    btn.className = `btn ${activeClass}`.trim();
    btn.dataset.page = String(page);
    btn.disabled = disabled;
    btn.textContent = label;
    btn.style.padding = "4px 10px";
    if (extraStyle) {
      Object.keys(extraStyle).forEach((key) => {
        btn.style[key] = extraStyle[key];
      });
    }
    container.appendChild(btn);
  };

  // First & Prev
  appendButton("First", 1, current === 1, "btn-outline");
  appendButton("Prev", current - 1, current === 1, "btn-outline");

  // Page Numbers (simplified logic: show surrounding)
  let startPage = Math.max(1, current - 2);
  let endPage = Math.min(total, startPage + 4);
  if (endPage - startPage < 4) {
    startPage = Math.max(1, endPage - 4);
  }

  for (let i = startPage; i <= endPage; i++) {
    const activeClass = i === current ? 'btn-primary' : 'btn-outline';
    appendButton(String(i), i, false, activeClass, { minWidth: "32px", justifyContent: "center" });
  }

  // Next & Last
  appendButton("Next", current + 1, current === total, "btn-outline");
  appendButton("Last", total, current === total, "btn-outline");
  container.onclick = (e) => {
    const btn = e.target.closest("button[data-page]");
    if (!btn || !container.contains(btn) || btn.disabled) return;
    const page = parseInt(btn.dataset.page, 10);
    if (!Number.isNaN(page)) goToPage(page);
  };
}

function goToPage(page) {
  if (page < 1) return;
  // We can't check 'total' easily here without storing it or querying DOM
  // But renderAccounts will clamp it.
  currentPage = page;
  renderAccounts();
}

// Filter by platform
function filterByPlatform(platform) {
  currentPlatform = platform;
  currentPage = 1; // Reset to first page
  document.querySelectorAll("#platformFilters .tab-item").forEach(btn => {
    btn.classList.toggle("active", btn.textContent === platform);
  });
  const subtitle = document.getElementById("pageSubtitle");
  if (subtitle) {
    subtitle.textContent = currentPlatform ? `Manage your ${currentPlatform} API credentials` : "Manage all your API credentials";
  }
  renderAccounts();
}

// Update page size
function updatePageSize(size) {
  pageSize = parseInt(size);
  currentPage = 1;
  renderAccounts();
}

// Update statistics
function updateStats() {
  const total = accounts.length;
  const abnormal = accounts.filter(isAccountAbnormal).length;
  const normal = Math.max(0, total - abnormal);

  document.getElementById("totalAccounts").textContent = total;
  document.getElementById("enabledAccounts").textContent = normal;
  document.getElementById("disabledAccounts").textContent = abnormal;

  // Attempt to update selected if element exists (it should)
  updateSelectedCount();

  // Update sidebar footer
  const footerTotal = document.getElementById("footerTotal");
  if (footerTotal) footerTotal.textContent = total;

  const footerNormal = document.getElementById("footerNormal");
  if (footerNormal) footerNormal.textContent = normal;

  const footerAbnormal = document.getElementById("footerAbnormal");
  if (footerAbnormal) footerAbnormal.textContent = abnormal;
}

// Update selected count
function updateSelectedCount() {
  const checked = document.querySelectorAll(".row-checkbox:checked").length;
  const el = document.getElementById("selectedCount");
  if (el) el.textContent = checked;
  const batchBtn = document.getElementById("batchDeleteBtn");
  if (batchBtn) {
    batchBtn.disabled = checked === 0;
    batchBtn.style.color = checked === 0 ? "#94a3b8" : "#fb7185";
    batchBtn.style.borderColor = checked === 0 ? "rgba(148,163,184,0.2)" : "rgba(251,113,133,0.45)";
  }
}

// Toggle select all
function toggleSelectAll(checked) {
  document.querySelectorAll(".row-checkbox").forEach(cb => cb.checked = checked);
  updateSelectedCount();
}

// Open modal
function openModal(account = null) {
  const modal = document.getElementById("accountModal");
  const title = document.getElementById("modalTitle");
  const form = document.getElementById("accountForm");
  const typeEl = document.getElementById("accountType");
  clearAccountImportStatus();

  const finalizeModal = () => {
    applyTokenLabels(typeEl ? typeEl.value : getActiveAccountType());
    modal.classList.add("active");
    modal.style.display = "flex";
  };

  const applyValues = () => {
    if (account) {
      title.textContent = "Edit Account";
      document.getElementById("accountId").value = account.id;
      setAccountModalType(normalizeAccountType(account));
      document.getElementById("clientCookie").value = getAccountToken(account);
      document.getElementById("enabled").checked = account.enabled;
      const usageInput = document.getElementById("usageLimit");
      if (usageInput) {
        const lim = account.usage_limit || account.warp_monthly_limit || 0;
        usageInput.value = lim > 0 ? String(lim) : "";
      }
    } else {
      title.textContent = "Add Account";
      form.reset();
      document.getElementById("accountId").value = "";
      setAccountModalType(getActiveAccountType());
      document.getElementById("enabled").checked = true;
      document.getElementById("clientCookie").value = "";
      const usageInput = document.getElementById("usageLimit");
      if (usageInput) usageInput.value = "";
    }
  };

  applyValues();
  finalizeModal();
}

// Close modal
function closeModal() {
  const modal = document.getElementById("accountModal");
  modal.classList.remove("active");
  modal.style.display = "none";
  clearAccountImportStatus();
}

// Save account
async function saveAccount(e) {
  e.preventDefault();
  const id = document.getElementById("accountId").value;
  const type = document.getElementById("accountType").value;
  const token = document.getElementById("clientCookie").value;
  const splitCredentials = splitBatchCredentialInput(token);
  const { unique: dedupedCredentials, duplicates: duplicateInputs } = dedupeCredentialInputs(type, splitCredentials);
  const { accepted: credentials, conflicts: existingConflicts } = filterExistingCredentialConflicts(type, dedupedCredentials, id);
  const existing = id ? accounts.find((a) => String(a.id) === String(id)) : null;
  const data = {
    account_type: type,
    weight: existing ? (parseInt(existing.weight, 10) || 1) : 1,
    enabled: document.getElementById("enabled").checked,
  };

  if (credentials.length === 0) {
    if (duplicateInputs.length > 0 || existingConflicts.length > 0) {
      const details = []
        .concat(duplicateInputs.slice(0, 4).map((item) => `Duplicate input: ${item}`))
        .concat(existingConflicts.slice(0, 4).map((item) => `Already exists: ${item}`));
      renderAccountImportStatus("No new credentials to add, duplicates have been fully filtered", "error", details);
      showToast("No new credentials to add, duplicates have been fully filtered", "error");
    } else {
      showToast("Please fill in at least one account credential", "error");
    }
    return;
  }
  try {
    clearAccountImportStatus();
    if (duplicateInputs.length > 0 || existingConflicts.length > 0) {
      const details = []
        .concat(duplicateInputs.slice(0, 4).map((item) => `Duplicate input: ${item}`))
        .concat(existingConflicts.slice(0, 4).map((item) => `Account already exists: ${item}`));
      showToast(
        `Filtered duplicate credentials: ${duplicateInputs.length} input duplicates, ${existingConflicts.length} existing duplicates`,
        "info",
        details,
      );
    }
    if (id) {
      const payload = buildAccountPayload(type, data, credentials[0]);
      const res = await fetch(`/api/accounts/${id}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      if (!res.ok) throw new Error(await res.text());
      closeModal();
      loadAccounts();
      showToast("Saved successfully");
      return;
    }

    if (credentials.length > 1) {
      const payloads = credentials.map((item) => buildAccountPayload(type, data, item));
      renderAccountImportStatus(`Batch adding accounts 0/${payloads.length}`, "info");
      const { success, failed, failures } = await runAccountCreatePool(payloads, 6, (progress) => {
        renderAccountImportStatus(
          `Batch adding accounts ${progress.completed}/${progress.total}, successful ${progress.success}, failed ${progress.failed}`,
          progress.failed > 0 ? "error" : "info",
          progress.failures,
        );
      });
      if (failed > 0) {
        renderAccountImportStatus(`Batch add complete: successful ${success}, failed ${failed}`, "error", failures);
      } else {
        renderAccountImportStatus(`Batch add complete: successful ${success}, failed ${failed}`, "info");
      }
      loadAccounts();
      if (failed === 0) {
        closeModal();
      }
      showToast(
        failed > 0 ? `Batch add complete: successful ${success}, failed ${failed}` : `Batch add complete: successful ${success}`,
        failed > 0 ? "error" : "success",
      );
      return;
    }

    await createAccount(buildAccountPayload(type, data, credentials[0]));
    closeModal();
    loadAccounts();
    showToast("Save successful");
  } catch (err) {
    showToast("Save failed: " + err.message, "error");
  }
}

// Edit account
function editAccount(id) {
  const account = accounts.find((a) => a.id === id);
  if (account) openModal(account);
}

// Refresh token
async function refreshToken(id) {
  const actionText = "Refresh";
  showToast(`Starting ${actionText} for account...`, "info");
  await checkAccount(id, false, actionText);
}

// Delete account
async function deleteAccount(id) {
  if (!confirm("Are you sure you want to delete this account?")) return;
  try {
    const res = await fetch(`/api/accounts/${id}`, { method: "DELETE" });
    if (!res.ok) throw new Error(await res.text());
    showToast("Delete successful");
    loadAccounts();
  } catch (err) {
    showToast("Delete failed: " + err.message, "error");
  }
}

function parseDataId(value) {
  const decoded = decodeData(value);
  if (decoded === "") return "";
  const num = Number(decoded);
  return Number.isNaN(num) ? decoded : num;
}

function formatTokenDisplay(acc) {
  const type = normalizeAccountType(acc);
  const token = acc.token;
  if (token) {
    if (token.length > 30) {
      if (type === 'warp') {
        // Warp tokens (JWTs) have long common prefixes, so show more of the end
        return token.substring(0, 10) + '...' + token.substring(token.length - 10);
      } else if (type === 'grok') {
        return token.substring(0, 8) + '...' + token.substring(token.length - 8);
      }
      return token.substring(0, 30) + '...';
    }
    return token;
  }
  if (type === 'grok' && getAccountToken(acc)) {
    const sso = getAccountToken(acc);
    return sso.length > 20 ? sso.substring(0, 8) + '...' + sso.substring(sso.length - 8) : sso;
  }
  if (type === 'warp' && getAccountToken(acc)) {
    const rt = getAccountToken(acc);
    return rt.length > 30 ? rt.substring(0, 10) + '...' + rt.substring(rt.length - 10) : rt;
  }
  if (type === 'puter' && getAccountToken(acc)) {
    const token = getAccountToken(acc);
    return token.length > 24 ? token.substring(0, 8) + '...' + token.substring(token.length - 8) : token;
  }
  if (acc.session_id) {
    return acc.session_id.substring(0, 30) + '...';
  }
  return '-';
}


// Export accounts
function exportAccounts() {
  window.location.href = "/api/export";
}

// Import accounts
async function importAccounts(event) {
  const file = event.target.files[0];
  if (!file) return;
  try {
    const text = await file.text();
    const res = await fetch("/api/import", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: text,
    });
    const result = await res.json();
    showToast(`Import complete: ${result.imported} successful, ${result.skipped} skipped`);
    loadAccounts();
  } catch (err) {
    showToast("Import failed: " + err.message, "error");
  }
  event.target.value = "";
}

// Load accounts on page load
document.addEventListener('DOMContentLoaded', () => {
  initDOMCache();
  loadAccounts();
  const typeSelect = document.getElementById("accountType");
  if (typeSelect) {
    applyTokenLabels(typeSelect.value);
  }
  const warpUserFileInput = document.getElementById("warpUserFileInput");
  if (warpUserFileInput) {
    warpUserFileInput.addEventListener("change", () => {
      const file = warpUserFileInput.files && warpUserFileInput.files[0];
      importWarpUserFile(file);
    });
  }
});
