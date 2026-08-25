const $ = (id) => document.getElementById(id);

const state = {
  settings: null,
  currentJob: null,
  pollTimer: null,
  pendingStart: null,
  proxies: [],
  proxyTests: new Map(),
  adminAccounts: [],
  adminAccountTests: new Map(),
  parsedAdminToken: "",
  parsedAdminRefreshToken: "",
  history: [],
  historyPage: 1,
  historyPageSize: 10,
};

const statusText = {
  queued: "排队中", running: "执行中", completed: "已完成", failed: "失败",
  partial: "部分完成", cancelled: "已停止", cancelling: "停止中", pending: "等待中",
};

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  let payload;
  try { payload = await response.json(); } catch { throw new Error(`服务响应异常（HTTP ${response.status}）`); }
  if (!response.ok || !payload.ok) throw new Error(payload.error || `请求失败（HTTP ${response.status}）`);
  return payload.data;
}

function tokensFromInput() {
  return $("userTokens").value.split(/\s+/).map((value) => value.trim()).filter(Boolean);
}

function extractAccessTokens(value, output = [], seen = new Set()) {
  if (!value || typeof value !== "object") return output;
  if (Array.isArray(value)) {
    value.forEach((item) => extractAccessTokens(item, output, seen));
    return output;
  }
  for (const key of ["access_token", "accessToken"]) {
    if (typeof value[key] === "string" && value[key].trim() && !seen.has(value[key].trim())) {
      const token = value[key].trim();
      seen.add(token);
      output.push(token);
    }
  }
  Object.values(value).forEach((child) => extractAccessTokens(child, output, seen));
  return output;
}

function parseUserJSON(raw, showMessage = true) {
  try {
    const parsed = JSON.parse(raw);
    const tokens = extractAccessTokens(parsed);
    if (!tokens.length) throw new Error("JSON 中没有找到 credentials.access_token");
    $("userTokens").value = tokens.join("\n");
    if (showMessage) setMessage($("taskMessage"), `已从 JSON 解析出 ${tokens.length} 个 Access Token`, "success");
    return tokens;
  } catch (error) {
    if (showMessage) setMessage($("taskMessage"), `JSON 解析失败：${error.message}`, "error");
    return [];
  }
}

function parseUserJSONInput(showMessage = true) {
  const raw = $("userTokens").value.trim();
  if (!raw) {
    if (showMessage) setMessage($("taskMessage"), "请先粘贴 sub2api 导出的 JSON", "error");
    return [];
  }
  return parseUserJSON(raw, showMessage);
}

async function importUserJSONFile(event) {
  const file = event.target.files?.[0];
  event.target.value = "";
  if (!file) return;
  try {
    const raw = await file.text();
    parseUserJSON(raw);
  } catch (error) {
    setMessage($("taskMessage"), `读取 JSON 文件失败：${error.message}`, "error");
  }
}

function setMessage(element, message = "", kind = "") {
  element.textContent = message;
  element.className = `form-message${kind ? ` ${kind}` : ""}`;
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>'"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" }[char]));
}

function shortID(value) {
  if (!value) return "-";
  return value.length > 22 ? `${value.slice(0, 12)}...${value.slice(-6)}` : value;
}

function formatTime(value) {
  if (!value) return "-";
  return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(new Date(value));
}

function setTab(name) {
  document.querySelectorAll(".tab").forEach((button) => button.classList.toggle("active", button.dataset.tab === name));
  document.querySelectorAll(".tab-panel").forEach((panel) => panel.classList.add("hidden"));
  $(`tab-${name}`).classList.remove("hidden");
  if (name === "history") loadHistory();
}

async function loadSettings() {
  try {
    const [settings, proxies, adminAccounts] = await Promise.all([api("/api/settings"), api("/api/proxies"), api("/api/admin-accounts")]);
    state.settings = settings;
    state.proxies = proxies;
    state.adminAccounts = adminAccounts;
    $("settingBaseUrl").value = state.settings.base_url;
    $("settingTos").value = state.settings.accepted_tos_version;
    $("settingRole").value = state.settings.role;
    $("settingSeatType").value = state.settings.seat_type;
    $("settingConcurrency").value = 1;
    $("settingTimeout").value = state.settings.request_timeout_seconds;
    $("settingInviteDelay").value = state.settings.invite_delay_seconds;
    $("settingAcceptDelay").value = state.settings.accept_delay_seconds;
    $("settingTransferDelay").value = state.settings.transfer_delay_seconds;
    $("settingAccountInterval").value = state.settings.account_interval_seconds || 0;
    renderProxies(state.settings.proxy_url || "");
    renderAdminAccounts($("adminAccountSelect").value);
    $("settingAutoCleanup").checked = state.settings.auto_cleanup;
    $("settingStopOnFailure").checked = state.settings.stop_on_first_failure;
  } catch (error) {
    setMessage($("settingsMessage"), error.message, "error");
  }
}

function renderAdminAccounts(selectedID = $("adminAccountSelect").value) {
  $("adminAccountSelect").innerHTML = '<option value="">请选择母号配置</option>' + state.adminAccounts.map((account) => `<option value="${escapeHTML(account.id)}">${escapeHTML(account.label)} · ${escapeHTML(account.email)} · ${escapeHTML(shortID(account.team_account_id))}</option>`).join("");
  $("adminAccountSelect").value = state.adminAccounts.some((account) => account.id === selectedID) ? selectedID : "";
  $("adminAccountCount").textContent = `${state.adminAccounts.length} 个母号`;
  $("adminAccountRows").innerHTML = state.adminAccounts.length ? state.adminAccounts.map((account) => {
    const test = state.adminAccountTests.get(account.id);
    const testCell = test ? `<div class="proxy-test-detail"><span class="mini-status ${test.valid ? "state-completed" : "state-failed"}">${test.valid ? "有效" : "无效"}</span><small>${escapeHTML(test.message)} · ${test.latency_ms}ms</small></div>` : '<span class="mini-status state-pending">未校验</span>';
    const expiry = account.access_token_expires_at ? `AT 到期 ${escapeHTML(formatTime(account.access_token_expires_at))}` : "未记录 AT 到期时间";
    const refreshCell = `<div class="proxy-test-detail"><span class="mini-status ${account.refresh_token_present ? "state-completed" : "state-pending"}">${account.refresh_token_present ? "RT 已保存" : "无 RT"}</span><small>${expiry}</small></div>`;
    return `<tr><td>${escapeHTML(account.label)}</td><td class="cell-email" title="${escapeHTML(account.email)}">${escapeHTML(account.email)}</td><td>${escapeHTML(account.plan_type || "-")}</td><td title="${escapeHTML(account.team_account_id)}">${escapeHTML(shortID(account.team_account_id))}</td><td>${refreshCell}</td><td><div class="account-actions"><button class="button tiny secondary" type="button" data-account-action="test" data-account-id="${escapeHTML(account.id)}">校验</button><button class="button tiny secondary" type="button" data-account-action="refresh" data-account-id="${escapeHTML(account.id)}">刷新 AT/RT</button><button class="button tiny secondary" type="button" data-account-action="edit" data-account-id="${escapeHTML(account.id)}">编辑</button><button class="button tiny danger" type="button" data-account-action="delete" data-account-id="${escapeHTML(account.id)}">删除</button></div></td></tr>`;
  }).join("") : '<tr><td colspan="6" class="empty-cell">暂无母号配置</td></tr>';
}

async function reloadAdminAccounts(selectedID) {
  state.adminAccounts = await api("/api/admin-accounts");
  renderAdminAccounts(selectedID);
}

async function saveAdminAccount(event) {
  event.preventDefault();
  const id = $("adminAccountEditId").value;
  const sessionRaw = $("adminSessionInput").value.trim();
  if (!state.parsedAdminToken && (sessionRaw.startsWith("{") || sessionRaw.startsWith("["))) parseAdminSessionInput();
  const payload = { label: $("adminAccountLabel").value.trim(), access_token: state.parsedAdminToken || sessionRaw, refresh_token: state.parsedAdminRefreshToken || $("adminRefreshTokenInput").value.trim(), team_account_id: $("adminAccountTeamId").value.trim() };
  if (!payload.label || (!id && !payload.access_token)) { setMessage($("adminAccountMessage"), "母号名称和 Access Token 不能为空", "error"); return; }
  $("saveAdminAccount").disabled = true;
  try {
    const account = await api(id ? `/api/admin-accounts/${encodeURIComponent(id)}` : "/api/admin-accounts", { method: id ? "PUT" : "POST", body: JSON.stringify(payload) });
    resetAdminAccountForm();
    await reloadAdminAccounts(account.id);
    $("adminAccountSelect").value = account.id;
    setMessage($("adminAccountMessage"), `${account.label} 已加密保存并选中`, "success");
  } catch (error) { setMessage($("adminAccountMessage"), error.message, "error"); }
  finally { $("saveAdminAccount").disabled = false; }
}

function resetAdminAccountForm() {
  $("adminAccountEditId").value = ""; $("adminAccountLabel").value = ""; $("adminSessionInput").value = ""; $("adminRefreshTokenInput").value = ""; $("adminAccountTeamId").value = "";
  $("adminSessionInput").required = true; $("adminSessionInput").placeholder = ""; state.parsedAdminToken = ""; state.parsedAdminRefreshToken = "";
  $("adminParsePreview").className = "parse-preview empty-state"; $("adminParsePreview").textContent = "等待解析 Session";
  $("saveAdminAccount").textContent = "保存母号"; $("cancelAdminAccountEdit").classList.add("hidden");
}

function findCredential(value, keys, depth = 0) {
  if (!value || typeof value !== "object" || depth > 6) return "";
  for (const key of keys) {
    if (typeof value[key] === "string" && value[key].trim()) return value[key].trim();
  }
  for (const child of Object.values(value)) {
    const found = findCredential(child, keys, depth + 1);
    if (found) return found;
  }
  return "";
}

function findAccessToken(value, depth = 0) { return findCredential(value, ["accessToken", "access_token"], depth); }
function findRefreshToken(value, depth = 0) { return findCredential(value, ["refreshToken", "refresh_token"], depth); }

function decodeJWTPayload(token) {
  const parts = token.split(".");
  if (parts.length !== 3) throw new Error("没有找到有效的 Access Token");
  const normalized = parts[1].replace(/-/g, "+").replace(/_/g, "/");
  const padded = normalized + "=".repeat((4 - normalized.length % 4) % 4);
  const bytes = Uint8Array.from(atob(padded), (char) => char.charCodeAt(0));
  return JSON.parse(new TextDecoder().decode(bytes));
}

function parseAdminSessionInput() {
  const raw = $("adminSessionInput").value.trim();
  if (!raw) { setMessage($("adminAccountMessage"), "请粘贴 Session JSON 或 Access Token", "error"); return null; }
  try {
    if (!raw.startsWith("{") && !raw.startsWith("[")) state.parsedAdminRefreshToken = $("adminRefreshTokenInput").value.trim();
    let session = null;
    let token = raw;
    if (raw.startsWith("{") || raw.startsWith("[")) {
      session = JSON.parse(raw);
      token = findAccessToken(session);
      if (!token) throw new Error("Session JSON 中没有 accessToken 字段");
      state.parsedAdminRefreshToken = "";
      $("adminRefreshTokenInput").value = "";
      const refreshToken = findRefreshToken(session);
      if (refreshToken) {
        $("adminRefreshTokenInput").value = refreshToken;
        state.parsedAdminRefreshToken = refreshToken;
      }
    }
    const claims = decodeJWTPayload(token);
    const auth = claims["https://api.openai.com/auth"] || {};
    const profile = claims["https://api.openai.com/profile"] || {};
    const email = session?.user?.email || profile.email || "未知账号";
    const plan = session?.account?.planType || auth.chatgpt_plan_type || "未知计划";
    const accountID = session?.account?.id || auth.chatgpt_account_id || "";
    const expires = session?.expires || (claims.exp ? new Date(claims.exp * 1000).toISOString() : "");
    state.parsedAdminToken = token;
    if (!$("adminAccountTeamId").value && accountID) $("adminAccountTeamId").value = accountID;
    $("adminParsePreview").className = "parse-preview";
    $("adminParsePreview").innerHTML = `<strong>${escapeHTML(email)}</strong><span>${escapeHTML(plan)}</span><span>${escapeHTML(shortID(accountID))}</span><span>${expires ? `到期 ${escapeHTML(formatTime(expires))}` : "未提供到期时间"}</span>`;
    setMessage($("adminAccountMessage"), `Access Token 已解析（${token.slice(0, 8)}...${token.slice(-6)}）${state.parsedAdminRefreshToken ? "，Refresh Token 已解析" : "，未找到 Refresh Token"}`, "success");
    return token;
  } catch (error) {
    state.parsedAdminToken = "";
    state.parsedAdminRefreshToken = "";
    $("adminParsePreview").className = "parse-preview empty-state"; $("adminParsePreview").textContent = "Session 解析失败";
    setMessage($("adminAccountMessage"), error.message, "error"); return null;
  }
}

async function importAdminJSONFile(event) {
  const file = event.target.files?.[0];
  event.target.value = "";
  if (!file) return;
  try {
    $("adminSessionInput").value = await file.text();
    parseAdminSessionInput();
  } catch (error) {
    setMessage($("adminAccountMessage"), `读取母号 JSON 文件失败：${error.message}`, "error");
  }
}

async function testAdminAccountCredential(id = "", savedOnly = false) {
  const formToken = state.parsedAdminToken;
  const useSaved = savedOnly || (id && !formToken);
  const payload = { id: useSaved ? id : "", access_token: useSaved ? "" : formToken, team_account_id: useSaved ? "" : $("adminAccountTeamId").value.trim() };
  if (!payload.id && !payload.access_token) { setMessage($("adminAccountMessage"), "请输入 Access Token", "error"); return; }
  $("testAdminAccountInput").disabled = true;
  setMessage($("adminAccountMessage"), "正在校验母号凭据...");
  try {
    const result = await api("/api/admin-accounts/test", { method: "POST", body: JSON.stringify(payload) });
    if (id) { state.adminAccountTests.set(id, result); renderAdminAccounts(); }
    setMessage($("adminAccountMessage"), `${result.message}，耗时 ${result.latency_ms}ms`, result.valid ? "success" : "error");
  } catch (error) { setMessage($("adminAccountMessage"), error.message, "error"); }
  finally { $("testAdminAccountInput").disabled = false; }
}

async function handleAdminAccountAction(event) {
  const button = event.target.closest("[data-account-action]");
  if (!button) return;
  const account = state.adminAccounts.find((item) => item.id === button.dataset.accountId);
  if (!account) return;
  if (button.dataset.accountAction === "test") { await testAdminAccountCredential(account.id, true); return; }
  if (button.dataset.accountAction === "edit") {
    $("adminAccountEditId").value = account.id; $("adminAccountLabel").value = account.label; $("adminSessionInput").value = ""; $("adminRefreshTokenInput").value = ""; $("adminAccountTeamId").value = account.team_account_id; state.parsedAdminToken = ""; state.parsedAdminRefreshToken = "";
    $("adminSessionInput").required = false; $("adminSessionInput").placeholder = "留空保留已保存的 AT，粘贴新 Session 可替换";
    $("adminParsePreview").className = "parse-preview"; $("adminParsePreview").innerHTML = `<strong>${escapeHTML(account.email)}</strong><span>${escapeHTML(account.plan_type || "-")}</span><span>${escapeHTML(shortID(account.team_account_id))}</span><span>已保存加密凭据</span>`;
    $("saveAdminAccount").textContent = "保存修改"; $("cancelAdminAccountEdit").classList.remove("hidden");
    $("adminAccountLabel").focus(); return;
  }
  if (button.dataset.accountAction === "refresh") {
    button.disabled = true;
    setMessage($("adminAccountMessage"), "正在通过全局代理刷新母号 AT/RT...");
    try {
      const result = await api(`/api/admin-accounts/${encodeURIComponent(account.id)}/refresh`, { method: "POST", body: "{}" });
      await reloadAdminAccounts(account.id);
      setMessage($("adminAccountMessage"), result.message, "success");
    } catch (error) { setMessage($("adminAccountMessage"), error.message, "error"); }
    finally { button.disabled = false; }
    return;
  }
  if (button.dataset.accountAction === "delete") {
    if (!confirm(`确认删除母号“${account.label}”？`)) return;
    try {
      await api(`/api/admin-accounts/${encodeURIComponent(account.id)}`, { method: "DELETE" });
      state.adminAccountTests.delete(account.id); await reloadAdminAccounts();
      setMessage($("adminAccountMessage"), `${account.label} 已删除`, "success");
    } catch (error) { setMessage($("adminAccountMessage"), error.message, "error"); }
  }
}

function maskProxyURL(value) {
  return String(value || "").replace(/:\/\/([^/@:]+):([^@]+)@/, "://$1:***@");
}

function renderProxies(selectedURL = $("settingProxy").value) {
  $("settingProxy").innerHTML = '<option value="">服务器直连</option>' + state.proxies.map((profile) => `<option value="${escapeHTML(profile.url)}">${escapeHTML(profile.name)} · ${escapeHTML(maskProxyURL(profile.url))}</option>`).join("");
  $("settingProxy").value = state.proxies.some((profile) => profile.url === selectedURL) ? selectedURL : "";
  $("proxyCount").textContent = `${state.proxies.length} 个配置`;
  $("proxyRows").innerHTML = state.proxies.length ? state.proxies.map((profile) => {
    const test = state.proxyTests.get(profile.id);
    const testCell = test ? `<div class="proxy-test-detail"><span class="mini-status ${test.reachable ? "state-completed" : "state-failed"}">${test.reachable ? "可用" : "不可用"}</span><small>${escapeHTML(test.message)} · ${test.latency_ms}ms</small></div>` : '<span class="mini-status state-pending">未测试</span>';
    return `<tr><td>${escapeHTML(profile.name)}</td><td class="proxy-address" title="${escapeHTML(maskProxyURL(profile.url))}">${escapeHTML(maskProxyURL(profile.url))}</td><td>${testCell}</td><td><div class="proxy-actions"><button class="button tiny secondary" type="button" data-proxy-action="test" data-proxy-id="${escapeHTML(profile.id)}">测试</button><button class="button tiny secondary" type="button" data-proxy-action="edit" data-proxy-id="${escapeHTML(profile.id)}">编辑</button><button class="button tiny danger" type="button" data-proxy-action="delete" data-proxy-id="${escapeHTML(profile.id)}">删除</button></div></td></tr>`;
  }).join("") : '<tr><td colspan="4" class="empty-cell">暂无代理配置</td></tr>';
}

async function reloadProxies(selectedURL) {
  state.proxies = await api("/api/proxies");
  renderProxies(selectedURL);
}

async function saveProxy(event) {
  event.preventDefault();
  const id = $("proxyEditId").value;
  const parsed = parseProxyInput(false);
  if (!parsed) return;
  const payload = { name: $("proxyName").value.trim(), url: parsed };
  if (!payload.name || !payload.url) { setMessage($("proxyMessage"), "代理名称和地址不能为空", "error"); return; }
  $("saveProxy").disabled = true;
  try {
    const profile = await api(id ? `/api/proxies/${encodeURIComponent(id)}` : "/api/proxies", { method: id ? "PUT" : "POST", body: JSON.stringify(payload) });
    resetProxyForm();
    await reloadProxies(profile.url);
    $("settingProxy").value = profile.url;
    await saveSettings();
    setMessage($("proxyMessage"), `${profile.name} 已保存并选中`, "success");
  } catch (error) { setMessage($("proxyMessage"), error.message, "error"); }
  finally { $("saveProxy").disabled = false; }
}

function parseProxyInput(showMessage = true) {
  const raw = $("proxyUrl").value.trim();
  if (!raw) { if (showMessage) setMessage($("proxyMessage"), "请输入代理地址", "error"); return null; }
  if (raw.includes("://")) return raw;
  const parts = raw.split(":");
  if (parts.length < 4 || !parts[0] || !parts[1] || !parts[2] || !parts.slice(3).join(":")) {
    if (showMessage) setMessage($("proxyMessage"), "线路格式应为 host:port:username:password", "error");
    return null;
  }
  const port = Number(parts[1]);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    if (showMessage) setMessage($("proxyMessage"), "代理端口必须是 1 到 65535 的数字", "error");
    return null;
  }
  const host = parts[0].trim();
  if (!host || /[/?#@\[\]]/.test(host)) {
    if (showMessage) setMessage($("proxyMessage"), "代理主机格式无效", "error");
    return null;
  }
  const username = parts[2];
  const password = parts.slice(3).join(":");
  const encodedUser = encodeURIComponent(username);
  const encodedPassword = encodeURIComponent(password);
  const normalized = `http://${encodedUser}:${encodedPassword}@${host}:${port}`;
  $("proxyUrl").value = normalized;
  if (showMessage) setMessage($("proxyMessage"), "已解析为 HTTP 代理格式", "success");
  return normalized;
}

function resetProxyForm() {
  $("proxyEditId").value = ""; $("proxyName").value = ""; $("proxyUrl").value = "";
  $("saveProxy").textContent = "保存代理"; $("cancelProxyEdit").classList.add("hidden");
}

async function testProxyURL(url, profileID = "") {
  if (!url) { setMessage($("proxyMessage"), "请输入代理地址", "error"); return; }
  $("testProxyInput").disabled = true;
  setMessage($("proxyMessage"), "正在测试代理链路...");
  try {
    const result = await api("/api/proxies/test", { method: "POST", body: JSON.stringify({ url }) });
    if (profileID) { state.proxyTests.set(profileID, result); renderProxies(); }
    setMessage($("proxyMessage"), `${result.message}，耗时 ${result.latency_ms}ms`, result.reachable ? "success" : "error");
  } catch (error) { setMessage($("proxyMessage"), error.message, "error"); }
  finally { $("testProxyInput").disabled = false; }
}

async function handleProxyAction(event) {
  const button = event.target.closest("[data-proxy-action]");
  if (!button) return;
  const profile = state.proxies.find((item) => item.id === button.dataset.proxyId);
  if (!profile) return;
  if (button.dataset.proxyAction === "test") { await testProxyURL(profile.url, profile.id); return; }
  if (button.dataset.proxyAction === "edit") {
    $("proxyEditId").value = profile.id; $("proxyName").value = profile.name; $("proxyUrl").value = profile.url;
    $("saveProxy").textContent = "保存修改"; $("cancelProxyEdit").classList.remove("hidden");
    $("proxyName").focus(); return;
  }
  if (button.dataset.proxyAction === "delete") {
    if (!confirm(`确认删除代理“${profile.name}”？`)) return;
    try {
      await api(`/api/proxies/${encodeURIComponent(profile.id)}`, { method: "DELETE" });
      state.proxyTests.delete(profile.id);
      if (state.settings.proxy_url === profile.url) state.settings.proxy_url = "";
      await reloadProxies();
      setMessage($("proxyMessage"), `${profile.name} 已删除`, "success");
    } catch (error) { setMessage($("proxyMessage"), error.message, "error"); }
  }
}

async function saveSettings() {
  const payload = {
    base_url: $("settingBaseUrl").value.trim(),
    accepted_tos_version: $("settingTos").value.trim(),
    role: $("settingRole").value.trim(),
    seat_type: $("settingSeatType").value.trim(),
    concurrency: 1,
    request_timeout_seconds: Number($("settingTimeout").value),
    invite_delay_seconds: Number($("settingInviteDelay").value),
    accept_delay_seconds: Number($("settingAcceptDelay").value),
    transfer_delay_seconds: Number($("settingTransferDelay").value),
    account_interval_seconds: Number($("settingAccountInterval").value),
    proxy_url: $("settingProxy").value.trim(),
    auto_cleanup: $("settingAutoCleanup").checked,
    stop_on_first_failure: $("settingStopOnFailure").checked,
  };
  try {
    state.settings = await api("/api/settings", { method: "PUT", body: JSON.stringify(payload) });
    setMessage($("settingsMessage"), "设置已保存", "success");
    return true;
  } catch (error) { setMessage($("settingsMessage"), error.message, "error"); return false; }
}

async function selectProxyForAllRequests() {
  const select = $("settingProxy");
  select.disabled = true;
  try {
    const saved = await saveSettings();
    if (saved) setMessage($("settingsMessage"), select.value ? "代理已启用：后续上游测试和完整流程统一经此代理" : "已切换为服务器直连", "success");
  } finally {
    select.disabled = false;
  }
}

async function inspectTokens(showErrors = true) {
  const adminAccountID = $("adminAccountSelect").value;
  const rawUsers = $("userTokens").value.trim();
  if (rawUsers.startsWith("{") || rawUsers.startsWith("[")) {
    const parsedTokens = parseUserJSON(rawUsers, showErrors);
    if (!parsedTokens.length) return null;
  }
  const userTokens = tokensFromInput();
  if (!adminAccountID && !userTokens.length) {
    if (showErrors) setMessage($("taskMessage"), "请输入母号和子号 Access Token", "error");
    return null;
  }
  $("inspectTokens").disabled = true;
  try {
    const result = await api("/api/tokens/inspect", { method: "POST", body: JSON.stringify({ admin_account_id: adminAccountID, admin_token: "", user_tokens: userTokens }) });
    renderInspection(result);
    const invalid = result.users.filter((item) => item.error).length + (result.admin_error ? 1 : 0);
    setMessage($("taskMessage"), invalid ? `解析完成，发现 ${invalid} 项无效凭据` : "凭据解析完成", invalid ? "error" : "success");
    return result;
  } catch (error) {
    if (showErrors) setMessage($("taskMessage"), error.message, "error");
    return null;
  } finally { $("inspectTokens").disabled = false; }
}

function renderInspection(result) {
  if (result.admin) {
    const admin = result.admin;
    $("adminPreview").classList.remove("empty-state");
    $("adminPreview").innerHTML = `<div class="identity-name"><strong>${escapeHTML(admin.name || admin.email)}</strong><span class="mini-status state-valid">有效</span></div><div class="identity-meta"><span>${escapeHTML(admin.email)}</span><span>${escapeHTML(admin.plan_type || "未知计划")}</span><span title="${escapeHTML(admin.account_id)}">${escapeHTML(shortID(admin.account_id))}</span></div>`;
    $("statAdmin").textContent = admin.name || "有效";
    $("statAdminMeta").textContent = admin.email;
    if (!$("teamAccountId").value && admin.account_id) $("teamAccountId").placeholder = admin.account_id;
  } else {
    $("adminPreview").classList.add("empty-state");
    $("adminPreview").textContent = result.admin_error || "未输入母号 AT";
    $("statAdmin").textContent = "无效";
    $("statAdminMeta").textContent = result.admin_error || "尚未输入";
  }
  const valid = result.users.filter((item) => !item.error).length;
  const invalid = result.users.length - valid;
  $("statUsers").textContent = String(result.users.length);
  $("statUsersMeta").textContent = `${valid} 有效 · ${invalid} 无效`;
  $("previewCount").textContent = `${result.users.length} 个子号`;
  $("credentialState").textContent = result.admin && invalid === 0 ? "校验通过" : "存在异常";
  $("credentialState").className = `mini-status ${result.admin && invalid === 0 ? "state-valid" : "state-invalid"}`;
  $("previewRows").innerHTML = result.users.length ? result.users.map((item) => {
    if (item.error) return `<tr><td>${item.index}</td><td class="cell-email">第 ${item.index} 行</td><td>-</td><td><span class="mini-status state-invalid" title="${escapeHTML(item.error)}">无效</span></td></tr>`;
    return `<tr><td>${item.index}</td><td class="cell-email" title="${escapeHTML(item.user.email)}">${escapeHTML(item.user.email)}</td><td>${escapeHTML(item.user.plan_type || "-")}</td><td><span class="mini-status state-valid">有效</span></td></tr>`;
  }).join("") : '<tr><td colspan="4" class="empty-cell">未输入子号 AT</td></tr>';
}

async function prepareStart(event) {
  event.preventDefault();
  if (state.currentJob && ["queued", "running", "cancelling"].includes(state.currentJob.status)) {
    setMessage($("taskMessage"), "当前已有任务正在执行", "error");
    return;
  }
  const adminAccountID = $("adminAccountSelect").value;
  const userTokens = tokensFromInput();
  if (!adminAccountID || !userTokens.length) { setMessage($("taskMessage"), "请选择母号并输入子号 Access Token", "error"); return; }
  const inspection = await inspectTokens();
  if (!inspection || !inspection.admin || inspection.users.some((item) => item.error)) {
    setMessage($("taskMessage"), "请先修正无效凭据", "error"); return;
  }
  state.pendingStart = { admin_account_id: adminAccountID, admin_token: "", user_tokens: userTokens, team_account_id: $("teamAccountId").value.trim() };
  const selectedAdmin = state.adminAccounts.find((account) => account.id === adminAccountID);
  const team = state.pendingStart.team_account_id || selectedAdmin?.team_account_id || inspection.admin.account_id;
  $("confirmText").textContent = `将使用团队 ${shortID(team)} 依次处理 ${userTokens.length} 个子号。`;
  $("confirmDialog").classList.remove("hidden");
}

async function startTask() {
  $("confirmDialog").classList.add("hidden");
  if (!state.pendingStart) return;
  $("startTask").disabled = true;
  try {
    const job = await api("/api/jobs", { method: "POST", body: JSON.stringify(state.pendingStart) });
    state.pendingStart = null;
    state.currentJob = job;
    $("jobPanel").classList.remove("hidden");
    renderJob(job);
    schedulePoll();
    setMessage($("taskMessage"), `任务 ${job.id} 已启动`, "success");
    $("jobPanel").scrollIntoView({ behavior: "smooth", block: "start" });
  } catch (error) {
    setMessage($("taskMessage"), error.message, "error");
  } finally { $("startTask").disabled = false; }
}

function schedulePoll() {
  clearTimeout(state.pollTimer);
  state.pollTimer = setTimeout(pollJob, 700);
}

async function pollJob() {
  if (!state.currentJob) return;
  try {
    const job = await api(`/api/jobs/${encodeURIComponent(state.currentJob.id)}`);
    state.currentJob = job;
    renderJob(job);
    if (["queued", "running", "cancelling"].includes(job.status)) schedulePoll();
    else loadHistory();
  } catch (error) {
    setMessage($("taskMessage"), error.message, "error");
  }
}

function renderJob(job) {
  const progress = job.total ? Math.round((job.completed / job.total) * 100) : 0;
  $("jobTitle").textContent = `任务 ${job.id}`;
  $("jobStatus").textContent = statusText[job.status] || job.status;
  $("jobStatus").className = `mini-status state-${job.status}`;
  $("jobProgressBar").style.width = `${progress}%`;
  $("jobProgressText").textContent = `${job.completed} / ${job.total}（${progress}%）`;
  $("jobTeamId").textContent = `团队 ${shortID(job.team_account_id)}`;
  $("statProgress").textContent = `${job.completed} / ${job.total}`;
  $("statProgressMeta").textContent = statusText[job.status] || job.status;
  $("statOutcome").textContent = `${job.succeeded} / ${job.failed}`;
  $("statOutcomeMeta").textContent = "成功 / 失败";
  $("cancelTask").disabled = !["queued", "running"].includes(job.status);
  $("jobRows").innerHTML = job.results.map((result) => {
    const steps = Object.fromEntries(result.steps.map((step) => [step.key, step]));
    const message = result.error || [...result.steps].reverse().find((step) => step.message)?.message || "-";
    return `<tr><td>${result.index}</td><td class="cell-email" title="${escapeHTML(result.user.email)}">${escapeHTML(result.user.email || `第 ${result.index} 行`)}</td><td><span class="mini-status state-${escapeHTML(result.status)}">${escapeHTML(statusText[result.status] || result.status)}</span></td>${["invite", "accept", "transfer", "kick"].map((key) => renderStep(steps[key])).join("")}<td class="result-message" title="${escapeHTML(message)}">${escapeHTML(message)}</td></tr>`;
  }).join("");
}

function renderStep(step) {
  if (!step) return '<td><span class="step-state">·</span></td>';
  const symbol = step.status === "completed" ? "✓" : step.status === "failed" ? "×" : step.status === "running" ? "…" : step.status === "cancelled" ? "■" : "·";
  const tooltip = [step.name, step.http_status ? `HTTP ${step.http_status}` : "", step.message].filter(Boolean).join(" · ");
  return `<td><span class="step-state ${escapeHTML(step.status)}" title="${escapeHTML(tooltip)}">${symbol}</span></td>`;
}

async function cancelTask() {
  if (!state.currentJob || !confirm("确认停止当前任务？")) return;
  try {
    await api(`/api/jobs/${encodeURIComponent(state.currentJob.id)}/cancel`, { method: "POST", body: "{}" });
    $("cancelTask").disabled = true;
    schedulePoll();
  } catch (error) { setMessage($("taskMessage"), error.message, "error"); }
}

async function loadHistory() {
  try {
    state.history = await api("/api/history");
    renderHistory();
  } catch (error) { setMessage($("historyMessage"), error.message, "error"); }
}

function renderHistory() {
  const total = state.history.length;
  const pageCount = Math.max(1, Math.ceil(total / state.historyPageSize));
  state.historyPage = Math.min(Math.max(1, state.historyPage), pageCount);
  const start = (state.historyPage - 1) * state.historyPageSize;
  const page = state.history.slice(start, start + state.historyPageSize);
  $("historyRows").innerHTML = page.length ? page.map((entry) => {
    const emails = (entry.results || []).map((result) => result.email).filter(Boolean);
    const emailHTML = emails.length ? `<div class="history-emails">${emails.map((email) => `<span title="${escapeHTML(email)}">${escapeHTML(email)}</span>`).join("")}</div>` : "-";
    return `<tr><td>${escapeHTML(entry.id)}</td><td class="cell-email" title="${escapeHTML(entry.admin_email)}">${escapeHTML(entry.admin_email)}</td><td title="${escapeHTML(entry.team_account_id)}">${escapeHTML(shortID(entry.team_account_id))}</td><td><span class="mini-status state-${escapeHTML(entry.status)}">${escapeHTML(statusText[entry.status] || entry.status)}</span></td><td>${emailHTML}</td><td>${entry.total}</td><td>${entry.succeeded}</td><td>${entry.failed}</td><td>${formatTime(entry.completed_at)}</td></tr>`;
  }).join("") : '<tr><td colspan="9" class="empty-cell">暂无执行记录</td></tr>';
  $("historyPageInfo").textContent = total ? `第 ${state.historyPage} / ${pageCount} 页 · 共 ${total} 条记录` : "暂无记录";
  $("historyPrev").disabled = state.historyPage <= 1;
  $("historyNext").disabled = state.historyPage >= pageCount;
  setMessage($("historyMessage"), total ? `当前页显示 ${page.length} 条，已保存子号邮箱` : "");
}

async function clearHistory() {
  if (!confirm("确认清空本机执行历史？")) return;
  try { await api("/api/history", { method: "DELETE" }); state.historyPage = 1; await loadHistory(); }
  catch (error) { setMessage($("historyMessage"), error.message, "error"); }
}

function resetTask() {
  if (state.currentJob && ["queued", "running", "cancelling"].includes(state.currentJob.status)) { setMessage($("taskMessage"), "请先停止当前任务", "error"); return; }
  $("adminAccountSelect").value = "";
  $("userTokens").value = "";
  $("teamAccountId").value = "";
  $("adminPreview").className = "identity-block empty-state";
  $("adminPreview").textContent = "母号信息将在这里显示";
  $("previewRows").innerHTML = '<tr><td colspan="4" class="empty-cell">等待解析</td></tr>';
  $("jobPanel").classList.add("hidden");
  $("statAdmin").textContent = "待解析"; $("statAdminMeta").textContent = "尚未输入";
  $("statUsers").textContent = "0"; $("statUsersMeta").textContent = "等待输入";
  $("statProgress").textContent = "0 / 0"; $("statProgressMeta").textContent = "尚未开始";
  $("statOutcome").textContent = "0 / 0";
  $("credentialState").textContent = "未校验"; $("credentialState").className = "mini-status state-pending";
  setMessage($("taskMessage")); state.currentJob = null;
}

document.querySelectorAll(".tab").forEach((button) => button.addEventListener("click", () => setTab(button.dataset.tab)));
$("runtimeAddress").textContent = location.host;
$("inspectTokens").addEventListener("click", () => inspectTokens());
$("importUserJSON").addEventListener("click", () => $("userTokensFile").click());
$("userTokensFile").addEventListener("change", importUserJSONFile);
$("parseUserJSON").addEventListener("click", () => parseUserJSONInput());
$("taskForm").addEventListener("submit", prepareStart);
$("confirmCancel").addEventListener("click", () => { state.pendingStart = null; $("confirmDialog").classList.add("hidden"); });
$("confirmStart").addEventListener("click", startTask);
$("confirmDialog").addEventListener("click", (event) => { if (event.target === $("confirmDialog")) $("confirmCancel").click(); });
$("cancelTask").addEventListener("click", cancelTask);
$("saveSettings").addEventListener("click", saveSettings);
$("settingProxy").addEventListener("change", selectProxyForAllRequests);
$("adminAccountForm").addEventListener("submit", saveAdminAccount);
$("parseAdminSession").addEventListener("click", parseAdminSessionInput);
$("importAdminJSON").addEventListener("click", () => $("adminSessionFile").click());
$("adminSessionFile").addEventListener("change", importAdminJSONFile);
$("testAdminAccountInput").addEventListener("click", () => testAdminAccountCredential($("adminAccountEditId").value));
$("cancelAdminAccountEdit").addEventListener("click", resetAdminAccountForm);
$("adminAccountRows").addEventListener("click", handleAdminAccountAction);
$("proxyForm").addEventListener("submit", saveProxy);
$("parseProxyInput").addEventListener("click", () => parseProxyInput(true));
$("testProxyInput").addEventListener("click", () => testProxyURL($("proxyUrl").value.trim(), $("proxyEditId").value));
$("cancelProxyEdit").addEventListener("click", resetProxyForm);
$("proxyRows").addEventListener("click", handleProxyAction);
$("refreshHistory").addEventListener("click", loadHistory);
$("historyPrev").addEventListener("click", () => { state.historyPage -= 1; renderHistory(); });
$("historyNext").addEventListener("click", () => { state.historyPage += 1; renderHistory(); });
$("clearHistory").addEventListener("click", clearHistory);
$("resetTask").addEventListener("click", resetTask);

loadSettings();
loadHistory();
