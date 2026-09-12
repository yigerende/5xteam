<script setup>
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  reactive,
  ref,
  watch,
} from "vue";
import {
  AlertTriangle,
  ArrowRight,
  BadgeCheck,
  Check,
  CheckCircle2,
  ChevronDown,
  Circle,
  Copy,
  Crown,
  Download,
  Eye,
  EyeOff,
  FileKey2,
  Inbox,
  KeyRound,
  LoaderCircle,
  Mail,
  Plus,
  RefreshCw,
  MoreHorizontal,
  Search,
  ListChecks,
  Trash2,
  Upload,
  X,
  XCircle,
} from "lucide-vue-next";
import { api, downloadFile } from "../api";
import { parseMailAccountText } from "../mailImport.js";
import { formatTime } from "../utils";
import IconButton from "./IconButton.vue";
import MessageBar from "./MessageBar.vue";
import StatusPill from "./StatusPill.vue";
import Pagination from "./Pagination.vue";

const emit = defineEmits(["open-team", "pro-changed"]);
const props = defineProps({ defaultPageSize: { type: Number, default: 10 } });
const activeTab = ref("accounts");
const accounts = ref([]);
const pipelineAccounts = ref([]);
const messages = ref([]);
const counts = ref({
  all: 0,
  outlook: 0,
  totp: 0,
  mailtoken: 0,
  directurl: 0,
  mailcom: 0,
  none: 0,
});
const service = ref({ available: false, url: "" });
const accountQuery = ref("");
const spaceFilter = ref("all");
const messageQuery = ref("");
const mailType = ref("all");
const activeMessage = ref(null);
const importOpen = ref(false);
const loginConfirmAccount = ref(null);
const actionMenuEmail = ref("");
const actionMenuStyle = ref({});
const sensitiveMasked = ref(false);
let actionMenuCloseTimer;
const importForm = reactive({ raw: "", group: "free" });
const importProgress = reactive({ total: 0, processed: 0, imported: 0, updated: 0, skipped: 0, complete: false, error: "" });
const importProgressPercent = computed(() => importProgress.total ? Math.round((importProgress.processed / importProgress.total) * 100) : 0);
const message = reactive({ text: "", type: "" });
const busy = ref("");
const fetchJob = ref(null);
const loginJobs = reactive({});
const loginLogList = ref(null);
const loginDialog = reactive({
  open: false,
  mode: "login",
  email: "",
  jobId: "",
  status: "queued",
  state: "queued",
  logs: [],
  error: "",
  errorHint: "",
});
const batchLoginDialog = reactive({
  open: false,
  mode: "oauth",
  total: 0,
  finished: 0,
  succeeded: 0,
  failed: 0,
  items: [],
});
const atCheckDialog = reactive({
  open: false,
  running: false,
  total: 0,
  finished: 0,
  valid: 0,
  invalid: 0,
  failed: 0,
  items: [],
});
const credentialDialog = reactive({
  open: false,
  email: "",
  gptPassword: "",
  accessToken: "",
  refreshToken: "",
  chatgptSession: "",
  accountID: "",
  loading: false,
  exporting: "",
  copied: "",
  error: "",
});
const timers = new Set();
const accountPage = ref(1);
const accountPageSize = ref(props.defaultPageSize);
const accountTotal = ref(0);
const selectedEmails = ref(new Set());
const pagedFilteredAccounts = computed(() => accounts.value);
const selectedAccounts = computed(() =>
  [...selectedEmails.value].map((email) => ({ email })),
);
const allVisibleSelected = computed(() =>
  pagedFilteredAccounts.value.length > 0 && pagedFilteredAccounts.value.every((item) => selectedEmails.value.has(String(item.email || '').toLowerCase())),
);
const messagePage = ref(1);
const messagePageSize = ref(props.defaultPageSize);
const messageTotal = ref(0);
const loginDialogTerminal = computed(
  () =>
    ["success", "failed", "cancelled", "challenge"].includes(
      loginDialog.status,
    ) ||
    ["success", "failed", "cancelled", "challenge"].includes(loginDialog.state),
);
const loginDialogTone = computed(() =>
  loginDialog.status === "success"
    ? "success"
    : loginDialogTerminal.value
      ? "danger"
      : "running",
);
const loginDialogStatus = computed(() =>
  loginDialog.status === "success"
    ? loginDialog.mode === "oauth" ? "RT / AT 获取成功" : "AT 获取成功"
    : loginDialogTerminal.value
      ? "执行失败"
      : "正在执行",
);
const loginDialogLatest = computed(
  () =>
    [...loginDialog.logs].reverse().find((item) => item?.message)?.message ||
    "正在创建登录任务",
);
const batchLoginTerminal = computed(
  () => batchLoginDialog.total > 0 && batchLoginDialog.finished >= batchLoginDialog.total,
);
const batchLoginTitle = computed(() =>
  batchLoginDialog.mode === "oauth" ? "批量登录并获取 RT / AT" : "批量获取临时 AT",
);
const atCheckTerminal = computed(
  () => atCheckDialog.total > 0 && atCheckDialog.finished >= atCheckDialog.total,
);
const atCheckPercent = computed(() =>
  atCheckDialog.total ? Math.round((atCheckDialog.finished / atCheckDialog.total) * 100) : 0,
);
const pipelineByEmail = computed(
  () =>
    new Map(
      pipelineAccounts.value.map((item) => [
        String(item.email || "").toLowerCase(),
        item,
      ]),
    ),
);
const pipelineStages = [
  ["invite", "邀请"],
  ["accept", "进入"],
  ["oauth", "授权"],
  ["push", "推送"],
  ["quota", "额度"],
  ["remove", "移出"],
];

function setMessage(text = "", type = "") {
  Object.assign(message, { text, type });
}
function terminalTaskStatus(task) {
  return ["success", "failed", "cancelled", "challenge"].includes(task?.status) ||
    ["success", "failed", "cancelled", "challenge"].includes(task?.state);
}
function batchTaskTone(task) {
  if (task.status === "success") return "success";
  if (terminalTaskStatus(task)) return "danger";
  if (task.status === "queued" || task.state === "queued") return "pending";
  return "running";
}
function batchTaskStatus(task) {
  if (task.status === "success") return "成功";
  if (task.status === "failed") return "失败";
  if (task.status === "cancelled") return "已取消";
  if (task.status === "challenge") return "需要处理";
  if (task.state === "retry_wait") return "等待重试";
  if (task.status === "queued" || task.state === "queued") return "等待中";
  return "执行中";
}
function batchTaskLatest(task) {
  return [...(task.logs || [])].reverse().find((item) => item?.message)?.message ||
    task.error || "等待任务返回执行步骤";
}
function methodLabel(account) {
  if (account.mailcom_fetch) return "mail.com";
  return (
    {
      outlook: "Outlook OAuth",
      totp: "密码 + TOTP",
      mailtoken: "接码链接",
      directurl: "单封链接",
    }[account.login_method] || "待补凭证"
  );
}
function statusTone(account) {
  if (
    account.mail_last_status === "error" ||
    account.mail_last_status === "failed"
  )
    return "danger";
  if (account.mail_last_status === "success") return "success";
  return "pending";
}
function hasAT(account) {
  return (
    ["AT", "RT"].includes(account.auth_type) || account.access_token_present
  );
}
function maskSensitive(value) {
  const text = String(value || '').trim();
  if (!text || !sensitiveMasked.value) return text;
  const at = text.indexOf('@');
  if (at > 0) {
    const local = text.slice(0, at);
    const domain = text.slice(at);
    if (local.length <= 2) return `${local[0] || ''}***${domain}`;
    return `${local.slice(0, Math.min(3, local.length - 1))}***${local.slice(-1)}${domain}`;
  }
  if (text.length <= 4) return `${text.slice(0, 1)}***`;
  const keep = Math.max(1, Math.floor((text.length - 3) / 2));
  return `${text.slice(0, keep)}***${text.slice(-keep)}`;
}
function displayAccountEmail(account) {
  return maskSensitive(account?.email);
}
function displayAccountLabel(account) {
  return maskSensitive(account?.label || '未分组');
}
function displaySpace(account) {
  const pipeline = pipelineFor(account);
  if (!pipeline || pipeline.remove_status === 'completed') return pipelineSpaceLabel(account);
  const raw = pipeline.admin_email || pipeline.admin_label || pipeline.team_account_id || '';
  return raw ? maskSensitive(raw) : pipelineSpaceLabel(account);
}
function hasRegistered(account) {
  return (
    account.registration_status === "success" ||
    account.access_token_present ||
    account.chatgpt_session_present ||
    account.gpt_password_present ||
    account.totp_secret_present
  );
}
function registrationState(account) {
  if (account.chatgpt_status === "dead" || account.registration_status === "dead") return "死号";
  if (account.registration_status === "failed") return "失败";
  return hasRegistered(account) ? "已完成" : "未执行";
}
function isDeadAccount(account) {
  return account?.chatgpt_status === "dead" || account?.registration_status === "dead" || pipelineFor(account)?.dead;
}
function chatGPTStatusTone(account) {
  if (isDeadAccount(account)) return "danger";
  if (account.at_checked_at) return account.at_valid ? "success" : "danger";
  if (hasAT(account)) return "success";
  if (loginJobFor(account)) return "running";
  return "pending";
}
function chatGPTStatusLabel(account) {
  if (isDeadAccount(account)) return "死号";
  if (loginJobFor(account)) return "登录中";
  if (account.at_checked_at) return account.at_valid ? "AT 有效" : "AT 无效";
  return hasAT(account) ? "凭证已保存" : "未登录";
}
function pipelineFor(account) {
  return pipelineByEmail.value.get(String(account.email || "").toLowerCase());
}
function mailSpaceStatus(account) {
  const pipeline = pipelineFor(account);
  if (pipeline?.remove_status === "completed") return "removed";
  if (pipeline?.accept_status === "completed") return "inside";
  return "outside";
}
const spaceFilterCounts = reactive({ outside: 0, inside: 0, removed: 0 });
let accountSearchTimer;
function isInTeamPipeline(account) {
  return !!pipelineFor(account);
}
function accountEmailKey(account) {
  return String(account?.email || '').trim().toLowerCase();
}
function isSelected(account) {
  return selectedEmails.value.has(accountEmailKey(account));
}
function toggleSelected(account) {
  const email = accountEmailKey(account);
  if (!email) return;
  const next = new Set(selectedEmails.value);
  if (next.has(email)) next.delete(email); else next.add(email);
  selectedEmails.value = next;
}
function toggleAllVisible() {
  const next = new Set(selectedEmails.value);
  if (allVisibleSelected.value) {
    pagedFilteredAccounts.value.forEach((item) => next.delete(accountEmailKey(item)));
  } else {
    pagedFilteredAccounts.value.forEach((item) => next.add(accountEmailKey(item)));
  }
  selectedEmails.value = next;
}
function clearSelection() {
  selectedEmails.value = new Set();
}
async function selectOutsideInvalidATAccounts() {
  busy.value = "select-invalid-at";
  try {
    const result = await api("/api/mail/accounts/invalid-at-outside");
    selectedEmails.value = new Set((result.emails || []).map((email) => accountEmailKey({ email })).filter(Boolean));
    setMessage(selectedEmails.value.size
      ? `已选中 ${selectedEmails.value.size} 个未进入空间且 AT 无效的账号（包含所有分页）`
      : "没有未进入空间且 AT 无效的账号", "success");
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}
function pipelineStageStatus(account, stage) {
  return pipelineFor(account)?.[`${stage}_status`] || "pending";
}
function pipelineStatusLabel(account) {
  const pipeline = pipelineFor(account);
  if (!pipeline) return "未进入 Team";
  if (pipeline.dead) return pipeline.remove_status === "completed" ? "死号 · 已移出" : "死号";
  if (pipeline.remove_status === "completed") return "已移出空间";
  if (pipeline.push_status === "completed") return "Sub2 运行中";
  if (pipeline.oauth_status === "completed") return "OAuth 已就绪";
  if (pipeline.accept_status === "completed") return "已进入空间";
  if (pipeline.invite_status === "completed") return "已邀请";
  return "等待执行";
}
function pipelineStatusTone(account) {
  const pipeline = pipelineFor(account);
  if (!pipeline) return "pending";
  if (pipeline.dead) return "danger";
  if (Object.values(pipeline).includes("failed")) return "danger";
  if (Object.values(pipeline).includes("running")) return "running";
  return ["oauth_ready", "monitoring", "removed"].includes(pipeline.status)
    ? "success"
    : "pending";
}
function pipelineSpaceLabel(account) {
  const pipeline = pipelineFor(account);
  if (!pipeline) return "未进入空间";
  if (pipeline.remove_status === "completed") return "已移出空间";
  const name = pipeline.admin_email || pipeline.admin_label || "";
  const team = pipeline.team_account_id || "";
  return name || team || "已进入空间";
}
function pipelineSpaceTone(account) {
  const pipeline = pipelineFor(account);
  if (!pipeline) return "pending";
  return pipeline.remove_status === "completed"
    ? "danger"
    : pipeline.accept_status === "completed"
      ? "success"
      : "pending";
}
function loginJobFor(account) {
  return loginJobs[account.email];
}
function loginProgress(account) {
  const job = loginJobFor(account);
  if (!job) return "";
  const latest = [...(job.logs || [])].reverse().find((item) => item?.message);
  return (
    latest?.message ||
    { queued: "等待执行", running: "正在登录", waiting_code: "等待邮箱验证码" }[
      job.state
    ] ||
    "正在登录"
  );
}
function startTimer(callback, interval = 1200) {
  const id = window.setInterval(callback, interval);
  timers.add(id);
  return id;
}
function stopTimer(id) {
  window.clearInterval(id);
  timers.delete(id);
}
function logTime(value) {
  if (!value) return "--:--:--";
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? value
    : date.toLocaleTimeString("zh-CN", { hour12: false, timeZone: "Asia/Shanghai" });
}
function logStep(value) {
  return (
    {
      start: "启动任务",
      mail_credentials: "加载邮箱",
      proxy_check: "代理检测",
      rate_limit: "限流重试",
      login_method: "登录方式",
      api_accounts_authorize_continue: "提交账号",
      api_accounts_email_otp_validate: "验证邮箱验证码",
      api_accounts_password_verify: "验证密码",
      api_accounts_mfa_issue_challenge: "发起 2FA 验证",
      api_accounts_mfa_verify: "验证 2FA",
      strategy: "选择登录方式",
      egress: "确认出口",
      oauth_init: "初始化 OAuth",
      authorize: "建立授权会话",
      sentinel: "生成安全令牌",
      identifier: "提交账号",
      send_code: "发送验证码",
      waiting_code: "等待验证码",
      mail_code_poll: "查收邮箱",
      verify_code: "提交验证码",
      callback: "完成登录回调",
      session: "获取 ChatGPT AT",
      token: "交换 OAuth Token",
      oauth: "生成 OAuth 凭证",
      persist_success: "保存凭证",
      done: "执行完成",
      phone_skipped: "已阻止手机接码",
      raw_error: "原始错误",
      failed: "执行失败",
      verification_code_missing: "验证码失败",
    }[value] ||
    value ||
    "执行步骤"
  );
}
function updateLoginDialog(email, job) {
  if (!loginDialog.open || loginDialog.email !== email) return;
  Object.assign(loginDialog, {
    jobId: job.job_id || loginDialog.jobId,
    status: job.status || loginDialog.status,
    state: job.state || job.status || loginDialog.state,
    logs: Array.isArray(job.logs) ? job.logs : loginDialog.logs,
    error: job.error || "",
    errorHint: job.error_hint || "",
  });
  nextTick(() => {
    if (loginLogList.value)
      loginLogList.value.scrollTop = loginLogList.value.scrollHeight;
  });
}
function closeLoginDialog() {
  if (loginDialogTerminal.value) loginDialog.open = false;
}
function retryLogin() {
  const account = accounts.value.find(
    (item) => item.email === loginDialog.email,
  );
  if (account) {
    if (loginDialog.mode === 'oauth') startAccountOAuthTask(account);
    else startAccountTask(account, loginDialog.mode);
  }
}
async function continueToTeam() {
  const account = accounts.value.find(
    (item) => item.email === loginDialog.email,
  );
  if (!account) return;
  loginDialog.open = false;
  await openTeam(account);
}

async function loadStatus() {
  try {
    service.value = await api("/api/mail/status");
  } catch (error) {
    service.value = { available: false, error: error.message };
  }
}
async function loadAccounts() {
  const query = new URLSearchParams({
    page: String(accountPage.value),
    page_size: String(accountPageSize.value),
    query: accountQuery.value.trim(),
    space_state: spaceFilter.value === "all" ? "" : spaceFilter.value,
  });
  const data = await api(`/api/mail/accounts?${query}`);
  accounts.value = data.items || [];
  pipelineAccounts.value = data.pipelines || [];
  accountTotal.value = Number(data.total || 0);
  const lastPage = Math.max(1, Math.ceil(accountTotal.value / accountPageSize.value));
  if (accountPage.value > lastPage) {
    accountPage.value = lastPage;
    return loadAccounts();
  }
  counts.value = { ...counts.value, ...(data.counts || {}) };
  Object.assign(spaceFilterCounts, data.space_counts || {});
}
function loadAccountsHandled() {
  loadAccounts().catch((error) => setMessage(error.message, "error"));
}
function setAccountPage(value) {
  accountPage.value = value;
  loadAccountsHandled();
}
function setAccountPageSize(value) {
  accountPageSize.value = value;
  accountPage.value = 1;
  loadAccountsHandled();
}
function setSpaceFilter(value) {
  spaceFilter.value = spaceFilter.value === value ? "all" : value;
  accountPage.value = 1;
  clearSelection();
  loadAccountsHandled();
}
watch(accountQuery, () => {
  window.clearTimeout(accountSearchTimer);
  accountPage.value = 1;
  clearSelection();
  accountSearchTimer = window.setTimeout(loadAccountsHandled, 250);
});
async function checkAT(emails = []) {
  const targets = [...new Set(emails.map((email) => String(email || "").trim().toLowerCase()).filter(Boolean))];
  if (!targets.length) return setMessage("请先选择需要检测 AT 的邮箱账号", "error");
  busy.value = "check-at";
  Object.assign(atCheckDialog, {
    open: true,
    running: true,
    total: targets.length,
    finished: 0,
    valid: 0,
    invalid: 0,
    failed: 0,
    items: targets.map((email) => ({ email, status: "queued", message: "等待检测" })),
  });
  let nextIndex = 0;
  const worker = async () => {
    while (nextIndex < atCheckDialog.items.length) {
      const item = atCheckDialog.items[nextIndex++];
      item.status = "running";
      item.message = "正在请求 OpenAI 验证 AT";
      try {
        const result = await api(`/api/mail/accounts/${encodeURIComponent(item.email)}/check-at`, {
          method: "POST",
          body: {},
        });
        const valid = !!result.result?.valid;
        item.status = valid ? "valid" : "invalid";
        item.message = result.result?.message || (valid ? "AT 有效" : "AT 无效");
        if (valid) atCheckDialog.valid += 1;
        else atCheckDialog.invalid += 1;
      } catch (error) {
        item.status = "failed";
        item.message = error.message;
        atCheckDialog.failed += 1;
      } finally {
        atCheckDialog.finished += 1;
      }
    }
  };
  try {
    const concurrency = Math.min(5, targets.length);
    await Promise.all(Array.from({ length: concurrency }, () => worker()));
    await loadAccounts();
    setMessage(
      `AT 检测完成：有效 ${atCheckDialog.valid}，无效 ${atCheckDialog.invalid}${atCheckDialog.failed ? `，失败 ${atCheckDialog.failed}` : ""}`,
      atCheckDialog.invalid || atCheckDialog.failed ? "error" : "success",
    );
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    atCheckDialog.running = false;
    busy.value = "";
  }
}
function atCheckTone(item) {
  if (item.status === "valid") return "success";
  if (item.status === "invalid" || item.status === "failed") return "danger";
  return item.status === "running" ? "running" : "pending";
}
function atCheckLabel(item) {
  return { queued: "等待", running: "检测中", valid: "有效", invalid: "无效", failed: "失败" }[item.status] || item.status;
}
async function loadMessages() {
  const query = new URLSearchParams({
    page: String(messagePage.value),
    page_size: String(messagePageSize.value),
    query: messageQuery.value.trim(),
    mail_type: mailType.value,
  });
  const data = await api(`/api/mail/messages?${query}`);
  messages.value = data.messages || [];
  messageTotal.value = Number(data.total || 0);
  const lastPage = Math.max(1, Math.ceil(messageTotal.value / messagePageSize.value));
  if (messagePage.value > lastPage) {
    messagePage.value = lastPage;
    return loadMessages();
  }
  if (activeMessage.value)
    activeMessage.value =
      messages.value.find(
        (item) => mailKey(item) === mailKey(activeMessage.value),
      ) || null;
}
function setMessagePage(value) {
  messagePage.value = value;
  loadMessages().catch((error) => setMessage(error.message, "error"));
}
function setMessagePageSize(value) {
  messagePageSize.value = value;
  messagePage.value = 1;
  loadMessages().catch((error) => setMessage(error.message, "error"));
}
async function refreshAll() {
  busy.value = "refresh";
  try {
    await Promise.all([loadStatus(), activeTab.value === "inbox" ? loadMessages() : loadAccounts()]);
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}

function parseImport() {
  return parseMailAccountText(importForm.raw);
}
function openImportDialog() {
  Object.assign(importProgress, { total: 0, processed: 0, imported: 0, updated: 0, skipped: 0, complete: false, error: "" });
  importOpen.value = true;
}
function closeImportDialog() {
  if (busy.value !== "import") importOpen.value = false;
}
async function importAccounts() {
  busy.value = "import";
  try {
    const items = parseImport();
    Object.assign(importProgress, { total: items.length, processed: 0, imported: 0, updated: 0, skipped: 0, complete: false, error: "" });
    const batchSize = 25;
    for (let start = 0; start < items.length; start += batchSize) {
      const batch = items.slice(start, start + batchSize);
      const result = await api("/api/mail/accounts/import", {
        method: "POST",
        body: { accounts: batch, group: importForm.group.trim() },
      });
      importProgress.imported += Number(result.imported || 0);
      importProgress.updated += Number(result.updated || 0);
      importProgress.skipped += Number(result.skipped || 0);
      importProgress.processed += batch.length;
    }
    importProgress.complete = true;
    importForm.raw = "";
    await loadAccounts();
    setMessage(
      `导入完成：新增 ${importProgress.imported}，更新 ${importProgress.updated}，跳过 ${importProgress.skipped}`,
      "success",
    );
  } catch (error) {
    importProgress.error = error.message;
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}
async function removeAccount(account) {
  if (!window.confirm(`确认删除邮件账号“${account.email}”？`)) return;
  busy.value = account.email;
  try {
    const result = await api(
      `/api/mail/accounts/${encodeURIComponent(account.email)}`,
      { method: "DELETE" },
    );
    selectedEmails.value.delete(accountEmailKey(account));
    await loadAccounts();
    setMessage(
      `邮件账号已删除${result.pipeline_deleted ? `，同时删除 ${result.pipeline_deleted} 条 Team 轮转记录` : ""}`,
      "success",
    );
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}
async function removeSelectedAccounts() {
  const targets = [...selectedAccounts.value];
  if (!targets.length) return setMessage("请先勾选要删除的邮件账号", "error");
  if (!window.confirm(`确认删除已勾选的 ${targets.length} 个邮件账号？此操作不可撤销。`)) return;
  busy.value = "remove-batch";
  try {
    const results = await Promise.allSettled(targets.map((account) => api(`/api/mail/accounts/${encodeURIComponent(account.email)}`, { method: "DELETE" })));
    const failed = results.filter((item) => item.status === "rejected").length;
    clearSelection();
    await loadAccounts();
    setMessage(`批量删除完成：成功 ${targets.length - failed}，失败 ${failed}`, failed ? "error" : "success");
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}

async function exportSelectedCredentials(format) {
  const targets = [...selectedAccounts.value];
  if (!targets.length) return setMessage("请先勾选要导出的邮件账号", "error");
  const label = format === "cpa" ? "CPA" : "Sub2";
  busy.value = `export-${format}`;
  try {
    await downloadFile(
      "/api/mail/accounts/credentials/export-batch",
      format === "cpa" ? "cpa-accounts.zip" : "sub2-accounts.json",
      {
        method: "POST",
        body: { emails: targets.map((account) => account.email), format },
      },
    );
    setMessage(`已导出 ${targets.length} 个账号的 ${label} 凭据`, "success");
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}

async function openCredentialDialog(account) {
  Object.assign(credentialDialog, {
    open: true,
    email: account.email,
    gptPassword: "",
    accessToken: "",
    refreshToken: "",
    chatgptSession: "",
    accountID: "",
    loading: true,
    exporting: "",
    copied: "",
    error: "",
  });
  try {
    const credentials = await api(
      `/api/mail/accounts/${encodeURIComponent(account.email)}/credentials`,
    );
    Object.assign(credentialDialog, {
      gptPassword: credentials.gpt_password || "",
      accessToken: credentials.access_token || "",
      refreshToken: credentials.refresh_token || "",
      chatgptSession: formatChatGPTSession(credentials.chatgpt_session),
      accountID: credentials.chatgpt_account_id || "",
    });
  } catch (error) {
    credentialDialog.error = error.message;
  } finally {
    credentialDialog.loading = false;
  }
}
function closeCredentialDialog() {
  Object.assign(credentialDialog, {
    open: false,
    email: "",
    gptPassword: "",
    accessToken: "",
    refreshToken: "",
    chatgptSession: "",
    accountID: "",
    loading: false,
    exporting: "",
    copied: "",
    error: "",
  });
}
function formatChatGPTSession(value) {
  if (!value) return "";
  if (typeof value === "string") {
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      return value;
    }
  }
  return JSON.stringify(value, null, 2);
}
async function copyAccountForImport(account) {
  if (!account?.access_token_present) {
    setMessage(`${account?.email || "该账号"} 尚未获取 AT，无法复制导入`, "error");
    return;
  }
  busy.value = `copy:${account.email}`;
  try {
    const credentials = await api(
      `/api/mail/accounts/${encodeURIComponent(account.email)}/credentials`,
    );
    const accessToken = String(credentials.access_token || "").trim();
    if (!accessToken) throw new Error("该账号没有可复制的 AT");
    await navigator.clipboard.writeText(accessToken);
    setMessage(`${account.email} 的 AT 已复制，可直接粘贴到 Team 轮转导入`, "success");
  } catch (error) {
    setMessage(`${account.email}：${error.message}`, "error");
  } finally {
    busy.value = "";
  }
}
async function copyCredential(kind) {
  const value =
    kind === "at"
      ? credentialDialog.accessToken
      : kind === "password"
        ? credentialDialog.gptPassword
        : kind === "session"
          ? credentialDialog.chatgptSession
          : credentialDialog.refreshToken;
  if (!value) return;
  try {
    await navigator.clipboard.writeText(value);
    credentialDialog.copied = kind;
    window.setTimeout(() => {
      if (credentialDialog.copied === kind) credentialDialog.copied = "";
    }, 1600);
  } catch {
    credentialDialog.error = "复制失败，请选中凭证后手动复制";
  }
}
function downloadJSON(filename, value) {
  const blob = new Blob([`${JSON.stringify(value, null, 2)}\n`], {
    type: "application/json;charset=utf-8",
  });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}
async function exportCredentialFormat(format) {
  credentialDialog.exporting = format;
  credentialDialog.error = "";
  try {
    const credentials = await api(
      `/api/mail/accounts/${encodeURIComponent(credentialDialog.email)}/credentials?format=${format}`,
    );
    const safeEmail = credentialDialog.email.replace(/[^a-z0-9@._-]+/gi, "_");
    const suffix = format === "cpa" ? "cpa-auth" : "sub2api-account";
    downloadJSON(`${safeEmail}-${suffix}.json`, credentials);
    setMessage(
      `${credentialDialog.email}：${format === "cpa" ? "CPA" : "Sub2"} JSON 已导出`,
      "success",
    );
  } catch (error) {
    credentialDialog.error = error.message;
  } finally {
    credentialDialog.exporting = "";
  }
}

async function startFetch(emails = []) {
  busy.value = "fetch";
  try {
    const started = await api("/api/mail/fetch", {
      method: "POST",
      body: { emails },
    });
    const job = started.job || started;
    if (!job.job_id) throw new Error("收件任务没有返回任务 ID");
    fetchJob.value = job;
    setMessage(
      `正在收件 0/${job.total || emails.length || accounts.value.length}`,
    );
    const timer = startTimer(async () => {
      try {
        const result = await api(
          `/api/mail/fetch/${encodeURIComponent(job.job_id)}`,
        );
        fetchJob.value = result.job || result;
        const current = fetchJob.value;
        setMessage(
          `正在收件 ${current.processed || 0}/${current.total || 0}${current.current_email ? ` · ${current.current_email}` : ""}`,
        );
        if (current.status === "success" || current.status === "failed") {
          stopTimer(timer);
          busy.value = "";
          if (current.status === "failed")
            throw new Error(current.error || "收件失败");
          await Promise.all([loadMessages(), loadAccounts()]);
          const summary = current.result?.summary || {};
          setMessage(
            `收件完成：成功 ${summary.ok || 0}，获取 ${summary.messages || 0} 封`,
            "success",
          );
        }
      } catch (error) {
        stopTimer(timer);
        busy.value = "";
        setMessage(error.message, "error");
      }
    });
  } catch (error) {
    busy.value = "";
    setMessage(error.message, "error");
  }
}

async function startAccountTask(account, mode = "login") {
  const taskName = "登录";
  busy.value = account.email;
  Object.assign(loginDialog, {
    open: true,
    mode,
    email: account.email,
    jobId: "",
    status: "queued",
    state: "queued",
    error: "",
    errorHint: "",
    logs: [
      {
        time: new Date().toISOString(),
        level: "info",
        step: "start",
        message: `正在创建${taskName}任务`,
      },
    ],
  });
  try {
    const started = await api(
      `/api/mail/accounts/${encodeURIComponent(account.email)}/login`,
      { method: "POST", body: {} },
    );
    const job = started.job || started;
    if (!job.job_id) throw new Error(`${taskName}任务没有返回任务 ID`);
    loginJobs[account.email] = {
      ...job,
      taskMode: mode,
      startedAt: Date.now(),
    };
    updateLoginDialog(account.email, job);
    setMessage(`${account.email}：正在登录并获取 AT`);
    const timer = startTimer(async () => {
      try {
        const result = await api(
          `/api/mail/login/${encodeURIComponent(job.job_id)}`,
        );
        const current = result.job || result;
        loginJobs[account.email] = {
          ...current,
          taskMode: mode,
          startedAt: loginJobs[account.email]?.startedAt || Date.now(),
        };
        updateLoginDialog(account.email, current);
        if (
          ["success", "failed", "cancelled", "challenge"].includes(
            current.status,
          ) ||
          ["success", "failed", "cancelled", "challenge"].includes(
            current.state,
          )
        ) {
          stopTimer(timer);
          busy.value = "";
          delete loginJobs[account.email];
          if (current.status === "success") {
            await loadAccounts();
            setMessage(`${account.email}：AT 已获取并保存`, "success");
          } else {
            setMessage(
              `${account.email}：${current.error || current.error_hint || `${taskName}失败`}`,
              "error",
            );
          }
        }
      } catch (error) {
        stopTimer(timer);
        busy.value = "";
        delete loginJobs[account.email];
        Object.assign(loginDialog, {
          status: "failed",
          state: "failed",
          error: error.message,
        });
        setMessage(`${account.email}：${error.message}`, "error");
      }
    }, 1800);
  } catch (error) {
    busy.value = "";
    Object.assign(loginDialog, {
      status: "failed",
      state: "failed",
      error: error.message,
    });
    setMessage(error.message, "error");
  }
}
async function startAccountOAuthTask(account) {
  const taskName = "登录并获取 RT / AT";
  busy.value = account.email;
  Object.assign(loginDialog, {
    open: true,
    mode: "oauth",
    email: account.email,
    jobId: "",
    status: "queued",
    state: "queued",
    error: "",
    errorHint: "",
    logs: [{ time: new Date().toISOString(), level: "info", step: "start", message: `正在创建${taskName}任务` }],
  });
  try {
    const started = await api(`/api/mail/accounts/${encodeURIComponent(account.email)}/oauth`, { method: "POST", body: {} });
    const job = started.job || started;
    if (!job.job_id) throw new Error(`${taskName}任务没有返回任务 ID`);
    loginJobs[account.email] = { ...job, taskMode: "oauth", startedAt: Date.now() };
    updateLoginDialog(account.email, job);
    setMessage(`${account.email}：正在登录并获取 RT / AT`);
    const timer = startTimer(async () => {
      try {
        const result = await api(`/api/mail/oauth/${encodeURIComponent(job.job_id)}`);
        const current = result.job || result;
        loginJobs[account.email] = { ...current, taskMode: "oauth", startedAt: loginJobs[account.email]?.startedAt || Date.now() };
        updateLoginDialog(account.email, current);
        if (["success", "failed", "cancelled", "challenge"].includes(current.status) || ["success", "failed", "cancelled", "challenge"].includes(current.state)) {
          stopTimer(timer);
          busy.value = "";
          delete loginJobs[account.email];
          if (current.status === "success") {
            await loadAccounts();
            setMessage(`${account.email}：RT / AT 已获取并保存`, "success");
          } else {
            setMessage(`${account.email}：${current.error || current.error_hint || `${taskName}失败`}`, "error");
          }
        }
      } catch (error) {
        stopTimer(timer);
        busy.value = "";
        delete loginJobs[account.email];
        Object.assign(loginDialog, { status: "failed", state: "failed", error: error.message });
        setMessage(`${account.email}：${error.message}`, "error");
      }
    }, 1800);
  } catch (error) {
    busy.value = "";
    Object.assign(loginDialog, { status: "failed", state: "failed", error: error.message });
    setMessage(`${account.email}：${error.message}`, "error");
  }
}
async function runMailTaskSilently(account, mode, onUpdate = () => {}) {
  const endpoint = mode === 'oauth'
    ? `/api/mail/accounts/${encodeURIComponent(account.email)}/oauth`
    : `/api/mail/accounts/${encodeURIComponent(account.email)}/login`;
  const started = await api(endpoint, { method: 'POST', body: {} });
  const first = started.job || started;
  if (!first.job_id) throw new Error('任务没有返回任务 ID');
  onUpdate(first);
  const statusURL = mode === 'oauth'
    ? `/api/mail/oauth/${encodeURIComponent(first.job_id)}`
    : `/api/mail/login/${encodeURIComponent(first.job_id)}`;
  let job = first;
  while (!["success", "failed", "cancelled", "challenge"].includes(job.status) && !["success", "failed", "cancelled", "challenge"].includes(job.state)) {
    await new Promise((resolve) => window.setTimeout(resolve, 1800));
    const result = await api(statusURL);
    job = result.job || result;
    onUpdate(job);
  }
  if (job.status !== 'success') throw new Error(job.error || job.error_hint || '任务执行失败');
  return job;
}
async function runSelectedMailTask(mode) {
  const targets = [...selectedAccounts.value];
  if (!targets.length) return setMessage("请先勾选邮件账号", "error");
  clearSelection();
  busy.value = `batch:${mode}`;
  Object.assign(batchLoginDialog, {
    open: true,
    mode,
    total: targets.length,
    finished: 0,
    succeeded: 0,
    failed: 0,
    items: targets.map((account, index) => ({
      email: account.email,
      status: "queued",
      state: "queued",
      logs: [],
      error: "",
      expanded: targets.length <= 5 || index === 0,
    })),
  });
  setMessage(`正在批量${mode === 'oauth' ? '登录并获取 RT / AT' : '获取临时 AT'}：0/${targets.length}`);
  const results = await Promise.all(targets.map(async (account) => {
    const task = batchLoginDialog.items.find((item) => item.email === account.email);
    try {
      await runMailTaskSilently(account, mode, (job) => {
        Object.assign(task, {
          ...job,
          logs: Array.isArray(job.logs) ? job.logs : task.logs,
          error: job.error || "",
        });
      });
      batchLoginDialog.succeeded += 1;
      return true;
    } catch (error) {
      Object.assign(task, { status: "failed", state: "failed", error: error.message });
      batchLoginDialog.failed += 1;
      setMessage(`${account.email}：${error.message}`, 'error');
      return false;
    } finally {
      batchLoginDialog.finished += 1;
      setMessage(`正在批量${mode === 'oauth' ? '登录并获取 RT / AT' : '获取临时 AT'}：${batchLoginDialog.finished}/${targets.length}`);
    }
  }));
  busy.value = '';
  await loadAccounts();
  const succeeded = results.filter(Boolean).length;
  setMessage(`批量${mode === 'oauth' ? '登录并获取 RT / AT' : '获取临时 AT'}完成：成功 ${succeeded}，失败 ${targets.length - succeeded}`, succeeded === targets.length ? 'success' : 'error');
}
function startLogin(account) {
  return startAccountTask(account, "login");
}
function requestLogin(account) {
  loginConfirmAccount.value = account;
}
function showActionMenu(account, hoverEvent) {
  window.clearTimeout(actionMenuCloseTimer);
  const email = account.email;
  const target = hoverEvent?.currentTarget;
  if (target?.getBoundingClientRect) {
    const rect = target.getBoundingClientRect();
    const menuWidth = 190;
    const menuHeight = 150;
    const left = Math.max(8, Math.min(window.innerWidth - menuWidth - 8, rect.right - menuWidth));
    const top = window.innerHeight - rect.bottom >= menuHeight + 8
      ? rect.bottom + 7
      : Math.max(8, rect.top - menuHeight - 7);
    actionMenuStyle.value = { left: `${left}px`, top: `${top}px` };
  }
  actionMenuEmail.value = email;
}
function scheduleCloseActionMenu() {
  window.clearTimeout(actionMenuCloseTimer);
  actionMenuCloseTimer = window.setTimeout(() => closeActionMenu(), 180);
}
function cancelCloseActionMenu() {
  window.clearTimeout(actionMenuCloseTimer);
}
function closeActionMenu() {
  window.clearTimeout(actionMenuCloseTimer);
  actionMenuEmail.value = "";
  actionMenuStyle.value = {};
}
function confirmLogin() {
  const account = loginConfirmAccount.value;
  loginConfirmAccount.value = null;
  if (account) startLogin(account);
}

async function openTeam(account) {
  busy.value = account.email;
  try {
    const profile = await api(
      `/api/mail/accounts/${encodeURIComponent(account.email)}/team`,
      { method: "POST", body: {} },
    );
    setMessage(`${account.email} 已加入 Team 轮转`, "success");
    // Keep the user on Mail Management. Refresh the local pipeline projection
    // so this button immediately becomes disabled and shows 已进轮转.
    await loadAccounts();
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}

async function moveToPro(account) {
  if (!account.access_token_present) return setMessage('该账号尚未保存 AT，不能进入 Pro 管理', 'error');
  if (!window.confirm(`确认将“${account.email}”移入 Pro 管理？移入后将不再显示在邮件账号列表。`)) return;
  busy.value = account.email;
  try {
    await api(`/api/mail/accounts/${encodeURIComponent(account.email)}/management-scope`, {
      method: 'PUT', body: { scope: 'pro' },
    });
    selectedEmails.value.delete(accountEmailKey(account));
    await loadAccounts();
    emit('pro-changed');
    setMessage(`${account.email} 已移入 Pro 管理`, 'success');
  } catch (error) {
    setMessage(error.message, 'error');
  } finally {
    busy.value = '';
  }
}

function mailKey(item) {
  return [
    item.source,
    item.account,
    item.folder,
    item.mid,
    item.subject,
    item.received_at,
  ].join("|");
}
function messageBody(item) {
  return item?.text || item?.body || item?.content || item?.snippet || "";
}
function firstCode(item) {
  return Array.isArray(item?.codes) ? item.codes[0] : "";
}

onMounted(() => {
  refreshAll();
  document.addEventListener("click", closeActionMenu);
});
onBeforeUnmount(() => [...timers].forEach(stopTimer));
onBeforeUnmount(() => window.clearTimeout(accountSearchTimer));
onBeforeUnmount(() => window.clearTimeout(actionMenuCloseTimer));
onBeforeUnmount(() => document.removeEventListener("click", closeActionMenu));
</script>

<template>
  <section class="view-stack">
    <header class="page-heading">
      <div>
        <span class="overline">MAIL OPERATIONS</span>
        <h1>邮件管理</h1>
        <p>邮件账号、验证码收件与 Team 轮转入口</p>
      </div>
      <div class="heading-actions">
        <StatusPill :tone="service.available ? 'success' : 'danger'">{{
          service.available ? "邮件服务正常" : "邮件服务离线"
        }}</StatusPill
        ><button
          class="btn ghost"
          type="button"
          :disabled="!!busy"
          @click="refreshAll"
        >
          <RefreshCw :class="{ spin: busy === 'refresh' }" :size="15" />刷新
        </button>
      </div>
    </header>

    <div class="mail-tabs" role="tablist">
      <button
        type="button"
        :class="{ active: activeTab === 'accounts' }"
        @click="activeTab = 'accounts'"
      >
        <Mail :size="15" />邮件账号
        <span>{{ counts.all || accounts.length }}</span>
      </button>
      <button
        type="button"
        :class="{ active: activeTab === 'inbox' }"
        @click="
          activeTab = 'inbox';
          loadMessages();
        "
      >
        <Inbox :size="15" />收件箱 <span>{{ messageTotal }}</span>
      </button>
    </div>

    <MessageBar :message="message" />

    <template v-if="activeTab === 'accounts'">
      <section class="panel list-panel mail-accounts-panel">
        <div class="panel-title responsive">
          <div>
            <span>MAIL ACCOUNTS</span>
            <h2>邮件账号</h2>
          </div>
          <div class="heading-actions">
            <label class="compact-search">
              <Search :size="14" /><input
                v-model="accountQuery"
                placeholder="搜索邮箱或分组" />
            </label>
            <button class="btn ghost" type="button" :disabled="!!busy" @click="selectOutsideInvalidATAccounts" title="选中所有分页中未进入空间且已标记 AT 无效的账号">
              <ListChecks :size="15" />一键选中未入空间 AT 无效账号
            </button>
            <button
              class="btn ghost"
              type="button"
              :disabled="!!busy || !selectedAccounts.length"
              @click="checkAT(selectedAccounts.map((item) => item.email))"
            >
              <BadgeCheck :size="15" />检测 AT<span v-if="selectedAccounts.length">（{{ selectedAccounts.length }}）</span>
            </button>
            <button class="btn ghost" type="button" @click="sensitiveMasked = !sensitiveMasked">
              <EyeOff v-if="!sensitiveMasked" :size="15" /><Eye v-else :size="15" />{{ sensitiveMasked ? '一键显示' : '一键脱敏' }}
            </button>
            <button class="btn danger" type="button" :disabled="!!busy || !selectedAccounts.length" @click="removeSelectedAccounts">
              <Trash2 :size="15" />批量删除<span v-if="selectedAccounts.length">（{{ selectedAccounts.length }}）</span>
            </button>
            <button class="btn ghost" type="button" :disabled="!!busy || !selectedAccounts.length" @click="exportSelectedCredentials('cpa')">
              <Download :size="15" />批量导出 CPA<span v-if="selectedAccounts.length">（{{ selectedAccounts.length }}）</span>
            </button>
            <button class="btn ghost" type="button" :disabled="!!busy || !selectedAccounts.length" @click="exportSelectedCredentials('sub2')">
              <Download :size="15" />批量导出 Sub2<span v-if="selectedAccounts.length">（{{ selectedAccounts.length }}）</span>
            </button>
            <button class="btn ghost" type="button" :disabled="!!busy || !selectedAccounts.length" @click="runSelectedMailTask('login')">
              <KeyRound :size="15" />临时获取 AT<span v-if="selectedAccounts.length">（{{ selectedAccounts.length }}）</span>
            </button>
            <button class="btn ghost" type="button" :disabled="!!busy || !selectedAccounts.length" @click="runSelectedMailTask('oauth')">
              <FileKey2 :size="15" />登录并获取 RT / AT<span v-if="selectedAccounts.length">（{{ selectedAccounts.length }}）</span>
            </button>
            <button
              class="btn primary"
              type="button"
              @click="openImportDialog"
            >
              <Plus :size="15" />导入账号
            </button>
          </div>
        </div>
        <div class="space-filter-tabs" role="tablist" aria-label="空间状态筛选">
          <button type="button" :class="{ active: spaceFilter === 'outside' }" @click="setSpaceFilter('outside')">未进入空间 <span>{{ spaceFilterCounts.outside }}</span></button>
          <button type="button" :class="{ active: spaceFilter === 'inside' }" @click="setSpaceFilter('inside')">在空间里面 <span>{{ spaceFilterCounts.inside }}</span></button>
          <button type="button" :class="{ active: spaceFilter === 'removed' }" @click="setSpaceFilter('removed')">已移出空间 <span>{{ spaceFilterCounts.removed }}</span></button>
        </div>
        <div class="mail-method-summary">
          <span v-if="selectedAccounts.length">已选 {{ selectedAccounts.length }}</span>
          <button v-if="selectedAccounts.length" class="btn ghost" type="button" :disabled="!!busy" @click="clearSelection"><X :size="13" />清空选择</button>
          <span>Outlook {{ counts.outlook || 0 }}</span
          ><span>mail.com {{ counts.mailcom || 0 }}</span
          ><span>TOTP {{ counts.totp || 0 }}</span
          ><span
            >接码链接
            {{ (counts.mailtoken || 0) + (counts.directurl || 0) }}</span
          ><span>待补凭证 {{ counts.none || 0 }}</span>
        </div>
        <div class="table-shell">
          <table class="mail-account-table">
            <thead>
              <tr>
                <th class="select-column"><input type="checkbox" :checked="allVisibleSelected" :disabled="!pagedFilteredAccounts.length || !!busy" aria-label="选择当前页账号" @change="toggleAllVisible" /></th>
                <th>账号</th>
                <th>进入时间</th>
                <th>收件方式</th>
                <th>凭证</th>
                <th>ChatGPT</th>
                <th>母号空间</th>
                <th>完整状态</th>
                <th class="actions-column">操作</th>
              </tr>
            </thead>
            <tbody>
                        <tr v-if="!pagedFilteredAccounts.length">
                <td colspan="9" class="empty-cell">暂无邮件账号</td>
              </tr>
              <tr
                v-for="account in pagedFilteredAccounts"
                :key="account.email"
                :class="{ 'row-running': loginJobFor(account) }"
              >
                <td class="select-column"><input type="checkbox" :checked="isSelected(account)" :disabled="!!busy" :aria-label="`选择 ${account.email}`" @change="toggleSelected(account)" /></td>
                <td class="account-cell">
                  <strong
                    class="copy-account-trigger"
                    :title="account.access_token_present ? '点击复制 AT，可粘贴到 Team 轮转导入' : '尚未获取 AT'"
                    @click.stop="copyAccountForImport(account)"
                    >{{ displayAccountEmail(account) }}</strong
                  ><small>{{ displayAccountLabel(account) }}</small>
                </td>
                <td><small class="table-note">{{ account.created_at ? formatTime(account.created_at) : "未知" }}</small></td>
                <td>
                  <StatusPill
                    :tone="
                      account.login_method || account.mailcom_fetch
                        ? 'success'
                        : 'pending'
                    "
                    >{{ methodLabel(account) }}</StatusPill
                  >
                </td>
                <td class="credentials-cell">
                  <span class="credential-grid"
                    ><small
                      :class="{
                        ready:
                          account.mail_password_present ||
                          account.mail_refresh_token_present ||
                          account.pickup_url_present,
                      }"
                      >收件：{{
                        account.mail_password_present ||
                        account.mail_refresh_token_present ||
                        account.pickup_url_present
                          ? "已配置"
                          : "缺失"
                      }}</small
                    ><small :class="{ ready: account.gpt_password_present }"
                      >ChatGPT密码：{{
                        account.gpt_password_present ? "已保存" : "未设置"
                      }}</small
                    ><small :class="{ ready: account.chatgpt_session_present }"
                      >Session：{{
                        account.chatgpt_session_present ? "已保存" : "未保存"
                      }}</small
                    ><small :class="{ ready: account.totp_secret_present }"
                      >2FA：{{
                        account.totp_secret_present ? "已配置" : "未配置"
                      }}</small
                    ><small :class="{ ready: hasRegistered(account) }"
                      >注册：{{ registrationState(account) }}</small
                    ></span
                  >
                </td>
                <td>
                  <StatusPill
                    :tone="chatGPTStatusTone(account)"
                    ><LoaderCircle
                      v-if="loginJobFor(account) && !isDeadAccount(account)"
                      class="spin"
                      :size="11"
                    />{{ chatGPTStatusLabel(account) }}</StatusPill
                  ><span
                    v-if="!loginJobFor(account) && !isDeadAccount(account)"
                    class="credential-grid token-state"
                    ><small :class="{ ready: account.access_token_present }"
                      >AT
                      {{
                        account.access_token_present ? "已保存" : "未获取"
                      }}</small
                    ><small :class="{ ready: account.refresh_token_present }"
                      >RT
                      {{
                        account.refresh_token_present ? "已保存" : "未获取"
                      }}</small
                    ></span
                  ><small
                    v-if="loginJobFor(account) && !isDeadAccount(account)"
                    class="table-note login-progress"
                    :title="loginProgress(account)"
                    >{{ loginProgress(account) }}</small
                  ><small
                    v-else-if="isDeadAccount(account)"
                    class="table-note danger-text"
                    :title="account.chatgpt_status_message || pipelineFor(account)?.dead_reason"
                    >{{ account.chatgpt_status_message || pipelineFor(account)?.dead_reason || "OpenAI 账号已停用" }}</small
                  ><small
                    v-else-if="account.refresh_last_error"
                    class="table-note danger-text"
                    :title="account.refresh_last_error"
                    >{{ account.refresh_last_error }}</small
                  >
                </td>
                <td class="pipeline-status-cell"><StatusPill :tone="pipelineSpaceTone(account)">{{ displaySpace(account) }}</StatusPill></td>
                <td class="pipeline-status-cell">
                  <StatusPill :tone="pipelineStatusTone(account)">{{
                    pipelineStatusLabel(account)
                  }}</StatusPill>
                  <div v-if="pipelineFor(account)" class="pipeline-mini-stages">
                    <span
                      v-for="[stage, label] in pipelineStages"
                      :key="stage"
                      :class="`tone-${pipelineStageStatus(account, stage)}`"
                      :title="`${label}：${pipelineStageStatus(account, stage)}`"
                      >{{ label }}</span
                    >
                  </div>
                  <small v-else class="table-note">尚无轮转记录</small>
                </td>
                <td class="actions-cell">
                  <div class="row-actions">
                    <button
                      class="btn primary compact"
                      type="button"
                      :disabled="
                        !!busy || !hasAT(account) || isInTeamPipeline(account)
                      "
                      @click="openTeam(account)"
                    >
                      {{ isInTeamPipeline(account) ? "已进轮转" : "Team 轮转"
                      }}<ArrowRight :size="14" />
                    </button>
                    <button
                      class="btn ghost compact"
                      type="button"
                      :disabled="!!busy || !account.access_token_present"
                      :title="account.access_token_present ? '移入 Pro 管理' : '请先获取 AT'"
                      @click="moveToPro(account)"
                    >
                      <Crown :size="14" />Pro 管理
                    </button>
                    <div class="action-menu" @mouseenter="showActionMenu(account, $event)" @mouseleave="scheduleCloseActionMenu">
                      <button
                        class="icon-button"
                        type="button"
                        title="更多操作"
                        :aria-expanded="actionMenuEmail === account.email"
                        @mouseenter="showActionMenu(account, $event)"
                      >
                        <MoreHorizontal :size="17" />
                      </button>
                      <Teleport to="body"><div
                        v-if="actionMenuEmail === account.email"
                        class="action-menu-popover"
                        :style="actionMenuStyle"
                        @mouseenter="cancelCloseActionMenu"
                        @mouseleave="scheduleCloseActionMenu"
                      >
                        <button class="action-menu-item" type="button" :disabled="!!busy" @click="closeActionMenu(); startFetch([account.email])"><Inbox :size="14" />收取该邮箱</button>
                        <button class="action-menu-item" type="button" :disabled="!!busy || !account.access_token_present" @click="closeActionMenu(); openCredentialDialog(account)"><FileKey2 :size="14" />查看、复制和导出 AT / RT</button>
                        <button
                          class="action-menu-item"
                          type="button"
                          :disabled="!!busy || !!loginJobFor(account)"
                          @click="
                            closeActionMenu();
                            requestLogin(account);
                          "
                        >
                          <LoaderCircle
                            v-if="loginJobFor(account)?.taskMode === 'login'"
                            class="spin"
                            :size="14"
                          /><KeyRound v-else :size="14" />获取临时 AT</button
                        ><button
                          class="action-menu-item"
                          type="button"
                          :disabled="!!loginJobFor(account)"
                          @click="
                            closeActionMenu();
                            startAccountOAuthTask(account);
                          "
                        >
                          <FileKey2 :size="14" />登录并获取 RT / AT</button
                        ><button
                          class="action-menu-item"
                          type="button"
                          :disabled="!!busy || !account.access_token_present"
                          @click="
                            closeActionMenu();
                            checkAT([account.email]);
                          "
                        >
                          <BadgeCheck :size="14" />检测 AT 是否有效
                        </button><button
                          class="action-menu-item danger"
                          type="button"
                          :disabled="!!busy"
                          @click="
                            closeActionMenu();
                            removeAccount(account);
                          "
                        >
                          <Trash2 :size="14" />删除邮件账号
                        </button>
                      </div></Teleport>
                    </div>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination :page="accountPage" :page-size="accountPageSize" :total="accountTotal" @update:page="setAccountPage" @update:page-size="setAccountPageSize" />
      </section>
    </template>

    <template v-else>
      <section class="panel inbox-toolbar">
        <div class="inbox-filters">
          <label class="compact-search grow"
            ><Search :size="14" /><input
              v-model="messageQuery"
              placeholder="搜索主题、正文、验证码或邮箱"
              @keyup.enter="loadMessages" /></label
          ><select v-model="mailType" @change="messagePage = 1; loadMessages()">
            <option value="all">全部类型</option>
            <option value="verification">验证码</option>
            <option value="invite">邀请</option>
            <option value="security">安全</option>
            <option value="banned">封禁</option>
            <option value="other">其他</option></select
          ><button
            class="btn primary"
            type="button"
            :disabled="!!busy || !accounts.length"
            @click="startFetch([])"
          >
            <LoaderCircle
              v-if="busy === 'fetch'"
              class="spin"
              :size="15"
            /><RefreshCw v-else :size="15" />收取全部
          </button>
        </div>
        <div v-if="fetchJob" class="mail-progress">
          <div>
            <strong>{{
              fetchJob.status === "success" ? "收件完成" : "正在收件"
            }}</strong
            ><span
              >{{ fetchJob.processed || 0 }} / {{ fetchJob.total || 0 }}
              {{ fetchJob.current_email || "" }}</span
            >
          </div>
          <div class="progress-line">
            <i
              :style="{
                width: `${Math.min(100, ((fetchJob.processed || 0) / Math.max(1, fetchJob.total || 1)) * 100)}%`,
              }"
            ></i>
          </div>
        </div>
      </section>
      <div class="inbox-grid">
        <section class="panel inbox-list">
          <div class="panel-title">
            <div>
              <span>INBOX</span>
              <h2>{{ messageTotal }} 封邮件</h2>
            </div>
          </div>
          <div class="message-list">
            <button
              v-for="item in messages"
              :key="mailKey(item)"
              type="button"
              :class="{
                active: mailKey(item) === mailKey(activeMessage || {}),
              }"
              @click="activeMessage = item"
            >
              <span
                ><strong>{{ item.subject || "无主题" }}</strong
                ><b v-if="firstCode(item)">{{ firstCode(item) }}</b></span
              ><small
                >{{ item.account }} · {{ formatTime(item.received_at) }}</small
              >
            </button>
            <div v-if="!messages.length" class="empty-cell">暂无邮件</div>
          </div>
          <Pagination :page="messagePage" :page-size="messagePageSize" :total="messageTotal" @update:page="setMessagePage" @update:page-size="setMessagePageSize" />
        </section>
        <section class="panel message-detail">
          <template v-if="activeMessage"
            ><div class="panel-title">
              <div>
                <span>{{ activeMessage.mail_type || "MESSAGE" }}</span>
                <h2>{{ activeMessage.subject || "无主题" }}</h2>
              </div>
              <StatusPill v-if="firstCode(activeMessage)" tone="success">{{
                firstCode(activeMessage)
              }}</StatusPill>
            </div>
            <dl>
              <div>
                <dt>收件账号</dt>
                <dd>{{ activeMessage.account }}</dd>
              </div>
              <div>
                <dt>发件人</dt>
                <dd>{{ activeMessage.sender || activeMessage.from || "-" }}</dd>
              </div>
              <div>
                <dt>收到时间</dt>
                <dd>{{ formatTime(activeMessage.received_at) }}</dd>
              </div>
            </dl>
            <pre>{{ messageBody(activeMessage) || "邮件正文为空" }}</pre>
          </template>
          <div v-else class="message-placeholder">
            <Mail :size="28" /><span>选择邮件查看正文</span>
          </div>
        </section>
      </div>
    </template>

    <div
      v-if="importOpen"
      class="modal-backdrop"
      @click.self="closeImportDialog"
    >
      <form class="modal mail-import-modal" @submit.prevent="importAccounts">
        <span class="overline">IMPORT MAIL ACCOUNTS</span>
        <h2>导入邮件账号</h2>
        <label class="field"
          ><span>分组</span
          ><input
            v-model="importForm.group"
            maxlength="64"
            placeholder="free" /></label
        ><label class="field"
          ><span>账号数据</span
          ><textarea
            v-model="importForm.raw"
            rows="9"
            spellcheck="false"
            required
            placeholder="iCloud：邮箱----接码链接&#10;iCloud：邮箱----密码或查询码----接码链接&#10;带临时 AT：邮箱----接码链接----Session JSON&#10;Outlook：邮箱----邮箱密码----client_id----邮箱RT&#10;TOTP：邮箱----ChatGPT密码----2FA密钥&#10;TOTP + AT：邮箱----ChatGPT密码----2FA密钥----AT"
          ></textarea
          ><small
            >支持 msg.linlanyu.com 的 /messages/ 链接；如果末尾追加 ChatGPT
            Session JSON，系统会提取其中的 accessToken 作为临时 AT
            保存，不会保存 sessionToken。</small
          ></label
        >
        <section v-if="importProgress.total" class="mail-import-progress" aria-live="polite">
          <div class="mail-import-progress-heading">
            <span>{{ importProgress.error ? '导入中断' : importProgress.complete ? '导入完成' : '正在导入' }}</span>
            <strong>{{ importProgressPercent }}%</strong>
          </div>
          <div class="mail-import-progress-track" role="progressbar" :aria-valuenow="importProgress.processed" aria-valuemin="0" :aria-valuemax="importProgress.total">
            <i :class="{ danger: importProgress.error }" :style="{ width: `${importProgressPercent}%` }"></i>
          </div>
          <div class="mail-import-progress-stats">
            <span>已处理 <strong>{{ importProgress.processed }} / {{ importProgress.total }}</strong></span>
            <span>新增 <strong>{{ importProgress.imported }}</strong></span>
            <span>更新 <strong>{{ importProgress.updated }}</strong></span>
            <span>跳过 <strong>{{ importProgress.skipped }}</strong></span>
          </div>
          <small v-if="importProgress.error" class="danger-text">{{ importProgress.error }}</small>
        </section>
        <div class="panel-actions">
          <button class="btn ghost" type="button" :disabled="busy === 'import'" @click="closeImportDialog">
            {{ importProgress.complete || importProgress.error ? '关闭' : '取消' }}</button
          ><button
            class="btn primary"
            type="submit"
            :disabled="busy === 'import' || !importForm.raw.trim()"
          >
            <LoaderCircle v-if="busy === 'import'" class="spin" :size="15" /><Upload v-else :size="15" />{{ busy === 'import' ? `正在导入 ${importProgress.processed}/${importProgress.total}` : '确认导入' }}
          </button>
        </div>
      </form>
    </div>

    <div
      v-if="loginConfirmAccount"
      class="modal-backdrop"
      @click.self="loginConfirmAccount = null"
    >
      <section
        class="modal login-confirm-dialog"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="login-confirm-title"
      >
        <span class="overline">CONFIRM CHATGPT LOGIN</span>
        <h2 id="login-confirm-title">确认获取临时 AT</h2>
        <p>
          将使用全局代理登录 <strong>{{ loginConfirmAccount.email }}</strong
          >，并从该邮箱读取本轮验证码。
        </p>
        <div class="panel-actions">
          <button
            class="btn ghost"
            type="button"
            @click="loginConfirmAccount = null"
          >
            取消</button
          ><button class="btn primary" type="button" @click="confirmLogin">
            <KeyRound :size="15" />确认获取
          </button>
        </div>
      </section>
    </div>

    <div
      v-if="credentialDialog.open"
      class="modal-backdrop credential-dialog-backdrop"
      @click.self="closeCredentialDialog"
    >
      <section
        class="modal credential-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="credential-dialog-title"
      >
        <header class="credential-dialog-header">
          <div>
            <span class="overline">OPENAI OAUTH CREDENTIALS</span>
            <h2 id="credential-dialog-title">查看与导出凭证</h2>
            <p>{{ credentialDialog.email }}</p>
          </div>
          <IconButton label="关闭凭证窗口" @click="closeCredentialDialog"
            ><X :size="16"
          /></IconButton>
        </header>
        <div v-if="credentialDialog.loading" class="credential-loading">
          <LoaderCircle class="spin" :size="20" /><span>正在读取加密凭证</span>
        </div>
        <template v-else>
          <div v-if="credentialDialog.error" class="credential-error">
            {{ credentialDialog.error }}
          </div>
          <label class="credential-token-field"
            ><span
              ><strong>ChatGPT 密码</strong
              ><button
                type="button"
                :disabled="!credentialDialog.gptPassword"
                @click="copyCredential('password')"
              >
                <Check
                  v-if="credentialDialog.copied === 'password'"
                  :size="14"
                /><Copy v-else :size="14" />{{
                  credentialDialog.copied === "password" ? "已复制" : "复制密码"
                }}
              </button></span
            ><input :value="credentialDialog.gptPassword || '未设置'" readonly
          /></label>
          <label class="credential-token-field"
            ><span
              ><strong>Access Token (AT)</strong
              ><button
                type="button"
                :disabled="!credentialDialog.accessToken"
                @click="copyCredential('at')"
              >
                <Check
                  v-if="credentialDialog.copied === 'at'"
                  :size="14"
                /><Copy v-else :size="14" />{{
                  credentialDialog.copied === "at" ? "已复制" : "复制 AT"
                }}
              </button></span
            ><textarea
              :value="credentialDialog.accessToken || '未获取'"
              readonly
              rows="5"
              spellcheck="false"
              @focus="$event.target.select()"
            ></textarea>
          </label>
          <label class="credential-token-field"
            ><span
              ><strong>Refresh Token (RT)</strong
              ><button
                type="button"
                :disabled="!credentialDialog.refreshToken"
                @click="copyCredential('rt')"
              >
                <Check
                  v-if="credentialDialog.copied === 'rt'"
                  :size="14"
                /><Copy v-else :size="14" />{{
                  credentialDialog.copied === "rt" ? "已复制" : "复制 RT"
                }}
              </button></span
            ><textarea
              :value="credentialDialog.refreshToken || '未获取'"
              readonly
              rows="4"
              spellcheck="false"
              @focus="$event.target.select()"
            ></textarea>
          </label>
          <label class="credential-token-field"
            ><span
              ><strong>完整 ChatGPT Session</strong
              ><button
                type="button"
                :disabled="!credentialDialog.chatgptSession"
                @click="copyCredential('session')"
              >
                <Check
                  v-if="credentialDialog.copied === 'session'"
                  :size="14"
                /><Copy v-else :size="14" />{{
                  credentialDialog.copied === "session" ? "已复制" : "复制 Session"
                }}
              </button></span
            ><textarea
              :value="credentialDialog.chatgptSession || '未保存'"
              readonly
              rows="8"
              spellcheck="false"
              @focus="$event.target.select()"
            ></textarea>
          </label>
          <small
            v-if="credentialDialog.accountID"
            class="credential-account-id mono"
            >Account ID: {{ credentialDialog.accountID }}</small
          >
          <footer class="credential-export-actions">
            <button
              class="btn ghost"
              type="button"
              :disabled="
                !!credentialDialog.exporting || !credentialDialog.refreshToken
              "
              @click="exportCredentialFormat('cpa')"
            >
              <LoaderCircle
                v-if="credentialDialog.exporting === 'cpa'"
                class="spin"
                :size="15"
              /><Download v-else :size="15" />导出 CPA JSON</button
            ><button
              class="btn primary"
              type="button"
              :disabled="
                !!credentialDialog.exporting || !credentialDialog.refreshToken
              "
              @click="exportCredentialFormat('sub2')"
            >
              <LoaderCircle
                v-if="credentialDialog.exporting === 'sub2'"
                class="spin"
                :size="15"
              /><Download v-else :size="15" />导出 Sub2 JSON
            </button>
          </footer>
        </template>
      </section>
    </div>

    <div v-if="atCheckDialog.open" class="modal-backdrop login-dialog-backdrop">
      <section
        class="modal at-check-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="at-check-dialog-title"
      >
        <header class="login-dialog-header">
          <div class="login-dialog-mark" :class="atCheckTerminal ? (atCheckDialog.invalid || atCheckDialog.failed ? 'danger' : 'success') : 'running'">
            <CheckCircle2 v-if="atCheckTerminal && !atCheckDialog.invalid && !atCheckDialog.failed" :size="21" />
            <XCircle v-else-if="atCheckTerminal" :size="21" />
            <LoaderCircle v-else class="spin" :size="21" />
          </div>
          <div>
            <span class="overline">ACCESS TOKEN CHECK</span>
            <h2 id="at-check-dialog-title">检测 AT</h2>
            <p>{{ atCheckDialog.finished }} / {{ atCheckDialog.total }} 已完成</p>
          </div>
          <StatusPill :tone="atCheckTerminal ? (atCheckDialog.invalid || atCheckDialog.failed ? 'danger' : 'success') : 'running'">
            {{ atCheckTerminal ? '检测完成' : '并发检测中' }}
          </StatusPill>
          <IconButton
            v-if="atCheckTerminal"
            label="关闭 AT 检测进度"
            @click="atCheckDialog.open = false"
          ><X :size="16" /></IconButton>
        </header>

        <div class="batch-login-summary at-check-summary">
          <span>总数 <strong>{{ atCheckDialog.total }}</strong></span>
          <span>已完成 <strong>{{ atCheckDialog.finished }}</strong></span>
          <span class="success">有效 <strong>{{ atCheckDialog.valid }}</strong></span>
          <span :class="{ danger: atCheckDialog.invalid }">无效 <strong>{{ atCheckDialog.invalid }}</strong></span>
          <span :class="{ danger: atCheckDialog.failed }">失败 <strong>{{ atCheckDialog.failed }}</strong></span>
        </div>
        <div
          class="batch-login-progress"
          role="progressbar"
          :aria-valuenow="atCheckDialog.finished"
          aria-valuemin="0"
          :aria-valuemax="atCheckDialog.total"
        >
          <i :class="{ danger: atCheckDialog.invalid || atCheckDialog.failed }" :style="{ width: `${atCheckPercent}%` }"></i>
        </div>

        <div class="at-check-list">
          <div v-for="item in atCheckDialog.items" :key="item.email" class="at-check-item">
            <span class="batch-task-state" :class="atCheckTone(item)">
              <CheckCircle2 v-if="item.status === 'valid'" :size="15" />
              <XCircle v-else-if="item.status === 'invalid' || item.status === 'failed'" :size="15" />
              <LoaderCircle v-else-if="item.status === 'running'" class="spin" :size="15" />
              <Circle v-else :size="15" />
            </span>
            <span><strong>{{ item.email }}</strong><small>{{ item.message }}</small></span>
            <StatusPill :tone="atCheckTone(item)">{{ atCheckLabel(item) }}</StatusPill>
          </div>
        </div>

        <footer v-if="atCheckTerminal" class="panel-actions login-dialog-actions">
          <button class="btn primary" type="button" @click="atCheckDialog.open = false">关闭</button>
        </footer>
      </section>
    </div>

    <div v-if="batchLoginDialog.open" class="modal-backdrop login-dialog-backdrop">
      <section
        class="modal batch-login-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="batch-login-dialog-title"
      >
        <header class="login-dialog-header">
          <div class="login-dialog-mark" :class="batchLoginTerminal ? (batchLoginDialog.failed ? 'danger' : 'success') : 'running'">
            <CheckCircle2 v-if="batchLoginTerminal && !batchLoginDialog.failed" :size="21" />
            <XCircle v-else-if="batchLoginTerminal" :size="21" />
            <LoaderCircle v-else class="spin" :size="21" />
          </div>
          <div>
            <span class="overline">BATCH CHATGPT LOGIN</span>
            <h2 id="batch-login-dialog-title">{{ batchLoginTitle }}</h2>
            <p>{{ batchLoginDialog.finished }} / {{ batchLoginDialog.total }} 已完成</p>
          </div>
          <StatusPill :tone="batchLoginTerminal ? (batchLoginDialog.failed ? 'danger' : 'success') : 'running'">
            {{ batchLoginTerminal ? '执行完成' : '并发执行中' }}
          </StatusPill>
          <IconButton
            v-if="batchLoginTerminal"
            label="关闭批量任务详情"
            @click="batchLoginDialog.open = false"
          ><X :size="16" /></IconButton>
        </header>

        <div class="batch-login-summary">
          <span>总数 <strong>{{ batchLoginDialog.total }}</strong></span>
          <span>已完成 <strong>{{ batchLoginDialog.finished }}</strong></span>
          <span class="success">成功 <strong>{{ batchLoginDialog.succeeded }}</strong></span>
          <span :class="{ danger: batchLoginDialog.failed }">失败 <strong>{{ batchLoginDialog.failed }}</strong></span>
        </div>
        <div class="batch-login-progress" aria-hidden="true">
          <i :style="{ width: `${batchLoginDialog.total ? (batchLoginDialog.finished / batchLoginDialog.total) * 100 : 0}%` }"></i>
        </div>

        <div class="batch-login-list">
          <article v-for="task in batchLoginDialog.items" :key="task.email" class="batch-login-task">
            <button class="batch-task-summary" type="button" @click="task.expanded = !task.expanded">
              <span class="batch-task-state" :class="batchTaskTone(task)">
                <CheckCircle2 v-if="task.status === 'success'" :size="15" />
                <XCircle v-else-if="terminalTaskStatus(task)" :size="15" />
                <LoaderCircle v-else-if="batchTaskTone(task) === 'running'" class="spin" :size="15" />
                <Circle v-else :size="15" />
              </span>
              <span class="batch-task-copy">
                <strong>{{ task.email }}</strong>
                <small>{{ batchTaskLatest(task) }}</small>
              </span>
              <StatusPill :tone="batchTaskTone(task)">{{ batchTaskStatus(task) }}</StatusPill>
              <ChevronDown :class="{ expanded: task.expanded }" :size="16" />
            </button>
            <div v-if="task.expanded" class="batch-task-details">
              <ol class="login-timeline">
                <li
                  v-for="(item, index) in task.logs || []"
                  :key="`${task.email}-${item.time}-${index}`"
                  :class="item.level"
                >
                  <span class="timeline-icon">
                    <XCircle v-if="item.level === 'error'" :size="15" />
                    <AlertTriangle v-else-if="item.level === 'warning'" :size="15" />
                    <LoaderCircle
                      v-else-if="index === task.logs.length - 1 && !terminalTaskStatus(task)"
                      class="spin"
                      :size="15"
                    />
                    <CheckCircle2 v-else-if="item.level === 'success'" :size="15" />
                    <Circle v-else :size="15" />
                  </span>
                  <div>
                    <span><strong>{{ logStep(item.step) }}</strong><time>{{ logTime(item.time) }}</time></span>
                    <p>{{ item.message }}</p>
                  </div>
                </li>
                <li v-if="!task.logs?.length">
                  <span class="timeline-icon"><Circle :size="15" /></span>
                  <div><p>{{ task.error || '等待任务返回执行步骤' }}</p></div>
                </li>
              </ol>
              <p v-if="task.error" class="batch-task-error">{{ task.error }}</p>
            </div>
          </article>
        </div>

        <footer v-if="batchLoginTerminal" class="panel-actions login-dialog-actions">
          <button class="btn primary" type="button" @click="batchLoginDialog.open = false">关闭</button>
        </footer>
      </section>
    </div>

    <div v-if="loginDialog.open" class="modal-backdrop login-dialog-backdrop">
      <section
        class="modal login-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="login-dialog-title"
      >
        <header class="login-dialog-header">
          <div class="login-dialog-mark" :class="loginDialogTone">
            <CheckCircle2
              v-if="loginDialog.status === 'success'"
              :size="21"
            /><XCircle
              v-else-if="loginDialogTerminal"
              :size="21"
            /><LoaderCircle v-else class="spin" :size="21" />
          </div>
          <div>
            <span class="overline">CHATGPT LOGIN</span>
            <h2 id="login-dialog-title">{{ loginDialog.mode === 'oauth' ? '登录并获取 RT / AT' : '登录并获取临时 AT' }}</h2>
            <p>{{ loginDialog.email }}</p>
          </div>
          <StatusPill :tone="loginDialogTone">{{
            loginDialogStatus
          }}</StatusPill>
          <IconButton
            v-if="loginDialogTerminal"
            label="关闭任务详情"
            @click="closeLoginDialog"
            ><X :size="16"
          /></IconButton>
        </header>

        <div class="login-current" :class="loginDialogTone">
          <div>
            <LoaderCircle
              v-if="!loginDialogTerminal"
              class="spin"
              :size="15"
            /><CheckCircle2
              v-else-if="loginDialog.status === 'success'"
              :size="15"
            /><XCircle v-else :size="15" /><strong>{{
              loginDialogLatest
            }}</strong>
          </div>
          <small v-if="loginDialog.jobId" class="mono">{{
            loginDialog.jobId.slice(0, 12)
          }}</small>
        </div>
        <div v-if="!loginDialogTerminal" class="login-running-line">
          <i></i>
        </div>

        <ol ref="loginLogList" class="login-timeline">
          <li
            v-for="(item, index) in loginDialog.logs"
            :key="`${item.time}-${index}`"
            :class="item.level"
          >
            <span class="timeline-icon"
              ><XCircle v-if="item.level === 'error'" :size="15" /><AlertTriangle v-else-if="item.level === 'warning'" :size="15" /><LoaderCircle
                v-else-if="
                  index === loginDialog.logs.length - 1 && !loginDialogTerminal
                "
                class="spin"
                :size="15" /><CheckCircle2 v-else-if="item.level === 'success'" :size="15"
              /><Circle v-else :size="15"
            /></span>
            <div>
              <span
                ><strong>{{ logStep(item.step) }}</strong
                ><time>{{ logTime(item.time) }}</time></span
              >
              <p>{{ item.message }}</p>
            </div>
          </li>
          <li v-if="!loginDialog.logs.length">
            <span class="timeline-icon"><Circle :size="15" /></span>
            <div><p>等待任务返回执行步骤</p></div>
          </li>
        </ol>

        <div
          v-if="loginDialogTerminal"
          class="login-outcome"
          :class="loginDialogTone"
        >
          <CheckCircle2
            v-if="loginDialog.status === 'success'"
            :size="18"
          /><XCircle v-else :size="18" />
          <div>
            <strong>{{
              loginDialog.status === "success"
                ? loginDialog.mode === "oauth" ? "Codex RT / AT 已安全保存" : "ChatGPT 临时 AT 已安全保存"
                : loginDialog.error || "登录未完成"
            }}</strong
            ><small v-if="loginDialog.errorHint">{{
              loginDialog.errorHint
            }}</small>
          </div>
        </div>
        <footer
          v-if="loginDialogTerminal"
          class="panel-actions login-dialog-actions"
        >
          <button class="btn ghost" type="button" @click="closeLoginDialog">
            关闭
          </button>
          <button
            v-if="loginDialog.status !== 'success'"
            class="btn primary"
            type="button"
            :disabled="!!busy"
            @click="retryLogin"
          >
            <RefreshCw :size="15" />重新执行
          </button>
          <button
            v-else-if="loginDialog.mode !== 'oauth'"
            class="btn primary"
            type="button"
            :disabled="!!busy"
            @click="continueToTeam"
          >
            进入 Team 轮转<ArrowRight :size="15" />
          </button>
        </footer>
      </section>
    </div>
  </section>
</template>

<style scoped>
.mail-tabs {
  display: flex;
  gap: 2px;
  border-bottom: 1px solid var(--line);
}
.mail-tabs button {
  display: flex;
  min-height: 40px;
  align-items: center;
  gap: 7px;
  padding: 0 14px;
  border: 0;
  border-bottom: 2px solid transparent;
  background: none;
  color: var(--muted);
  font-size: 12px;
  font-weight: 650;
}
.mail-tabs button.active {
  border-bottom-color: var(--green);
  color: var(--text);
}
.mail-tabs span {
  min-width: 20px;
  padding: 2px 5px;
  border-radius: 10px;
  background: var(--surface-3);
  font-size: 9px;
  text-align: center;
}
.compact-search {
  display: flex;
  height: 34px;
  align-items: center;
  gap: 7px;
  padding: 0 9px;
  border: 1px solid var(--line);
  border-radius: 4px;
  background: var(--bg-elevated);
  color: var(--muted);
}
.compact-search input {
  min-width: 180px;
  border: 0;
  outline: 0;
  background: transparent;
  color: var(--text);
  font-size: 11px;
}
.mail-method-summary {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 7px;
  margin: -5px 0 14px;
}
.mail-accounts-panel > .panel-title > div:first-child {
  flex: 0 0 auto;
}
.space-filter-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin: 0 0 14px;
}
.space-filter-tabs button {
  display: inline-flex;
  min-height: 32px;
  align-items: center;
  gap: 7px;
  padding: 0 11px;
  border: 1px solid var(--line);
  border-radius: 5px;
  background: var(--surface-2);
  color: var(--muted);
  font-size: 10px;
  font-weight: 650;
}
.space-filter-tabs button:hover,
.space-filter-tabs button.active {
  border-color: rgba(37, 143, 97, .35);
  background: var(--green-bg);
  color: var(--green-strong);
}
.space-filter-tabs span {
  min-width: 18px;
  padding: 2px 5px;
  border-radius: 9px;
  background: var(--surface-3);
  font-size: 9px;
  text-align: center;
}
.mail-method-summary span {
  padding: 4px 8px;
  border: 1px solid var(--line-soft);
  border-radius: 4px;
  background: var(--surface-2);
  color: var(--muted);
  font-size: 9px;
}
.mail-account-table {
  width: 100%;
  min-width: 1360px !important;
  table-layout: fixed;
}
.mail-accounts-panel .table-shell {
  overflow-x: auto;
  overflow-y: visible;
}
.mail-account-table th:nth-child(1),
.mail-account-table td:nth-child(1) {
  width: 44px;
  text-align: center;
}
.mail-account-table th:nth-child(2),
.mail-account-table td:nth-child(2) {
  width: 220px;
}
.mail-account-table th:nth-child(3),
.mail-account-table td:nth-child(3) {
  width: 115px;
  min-width: 0;
}
.mail-account-table th:nth-child(4),
.mail-account-table td:nth-child(4) {
  width: 110px;
}
.mail-account-table th:nth-child(5),
.mail-account-table td:nth-child(5) {
  width: 180px;
}
.mail-account-table th:nth-child(6),
.mail-account-table td:nth-child(6) {
  width: 125px;
}
.mail-account-table th:nth-child(7),
.mail-account-table td:nth-child(7) {
  width: 280px;
}
.mail-account-table th:nth-child(8),
.mail-account-table td:nth-child(8) {
  width: 110px;
}
.mail-account-table th:nth-child(9),
.mail-account-table td:nth-child(9) {
  width: 280px;
}
.copy-account-trigger { cursor: pointer; }
.copy-account-trigger:hover { color: var(--blue); text-decoration: underline; }
.credentials-cell {
  min-width: 150px;
}
.credential-grid {
  display: grid;
  min-width: 0;
  gap: 4px;
}
.credential-grid small {
  overflow: hidden;
  color: var(--muted);
  font-size: 9px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.credential-grid small.ready {
  color: var(--green-strong);
}
.actions-cell {
  overflow: visible;
  width: 280px;
  min-width: 280px;
}
.actions-cell .row-actions {
  flex-wrap: wrap;
  min-width: 260px;
  gap: 5px;
}
.action-menu {
  position: relative;
  flex: 0 0 auto;
}
.action-menu-popover {
  position: fixed;
  z-index: 99999;
  top: 0;
  left: 0;
  right: auto;
  display: grid;
  width: 152px;
  padding: 5px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--surface);
  box-shadow: var(--shadow);
}
.action-menu { z-index: 30; }
.action-menu-item {
  display: flex;
  min-height: 32px;
  align-items: center;
  gap: 8px;
  padding: 0 9px;
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: var(--text-2);
  font-size: 10px;
  text-align: left;
  white-space: nowrap;
}
.action-menu-item:hover:not(:disabled) {
  background: var(--surface-2);
  color: var(--text);
}
.action-menu-item:disabled {
  cursor: not-allowed;
  opacity: 0.45;
}
.action-menu-item.danger {
  color: var(--red);
}
.login-progress {
  display: block;
  overflow: hidden;
  max-width: 320px;
  color: var(--muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.btn.compact {
  min-height: 31px;
  padding: 5px 9px;
  font-size: 10px;
}
.inbox-toolbar {
  padding-block: 14px;
}
.inbox-filters {
  display: flex;
  align-items: center;
  gap: 9px;
}
.inbox-filters select {
  height: 34px;
  padding: 0 28px 0 9px;
  border: 1px solid var(--line);
  border-radius: 4px;
  background: var(--bg-elevated);
  color: var(--text-2);
  font-size: 11px;
}
.mail-progress {
  display: grid;
  gap: 8px;
  margin-top: 12px;
}
.mail-progress > div:first-child {
  display: flex;
  justify-content: space-between;
  color: var(--muted);
  font-size: 10px;
}
.mail-progress strong {
  color: var(--text-2);
}
.inbox-grid {
  display: grid;
  grid-template-columns: minmax(360px, 0.8fr) minmax(480px, 1.2fr);
  gap: 16px;
}
.inbox-list,
.message-detail {
  min-height: 500px;
}
.message-list {
  max-height: 620px;
  overflow-y: auto;
  margin: -4px -8px -8px;
}
.message-list button {
  display: grid;
  width: 100%;
  gap: 5px;
  padding: 11px 10px;
  border: 0;
  border-bottom: 1px solid var(--line-soft);
  background: transparent;
  color: var(--text-2);
  text-align: left;
}
.message-list button:hover,
.message-list button.active {
  background: var(--surface-2);
}
.message-list button.active {
  box-shadow: inset 2px 0 var(--green);
}
.message-list button > span {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.message-list strong {
  overflow: hidden;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.message-list b {
  padding: 3px 6px;
  border-radius: 4px;
  background: var(--green-bg);
  color: var(--green-strong);
  font:
    700 11px "SFMono-Regular",
    Consolas,
    monospace;
}
.message-list small {
  overflow: hidden;
  color: var(--muted);
  font-size: 9px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.message-detail dl {
  display: grid;
  gap: 8px;
  margin-bottom: 16px;
}
.message-detail dl div {
  display: grid;
  grid-template-columns: 72px minmax(0, 1fr);
  gap: 10px;
  font-size: 10px;
}
.message-detail dt {
  color: var(--muted);
}
.message-detail dd {
  overflow: hidden;
  color: var(--text-2);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.message-detail pre {
  overflow: auto;
  max-height: 500px;
  padding: 14px;
  border: 1px solid var(--line-soft);
  border-radius: 5px;
  background: var(--surface-2);
  color: var(--text-2);
  font:
    11px/1.65 system-ui,
    sans-serif;
  white-space: pre-wrap;
  word-break: break-word;
}
.message-placeholder {
  display: grid;
  min-height: 420px;
  place-content: center;
  justify-items: center;
  gap: 10px;
  color: var(--muted);
  font-size: 11px;
}
.mail-import-modal {
  width: min(620px, 100%);
}
.mail-import-modal .field:first-of-type {
  margin-top: 18px;
}
.mail-import-progress {
  display: grid;
  gap: 9px;
  margin-top: 14px;
}
.mail-import-progress-heading,
.mail-import-progress-stats {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  color: var(--muted);
  font-size: 10px;
}
.mail-import-progress-heading strong,
.mail-import-progress-stats strong {
  color: var(--text-2);
}
.mail-import-progress-track {
  height: 5px;
  overflow: hidden;
  border-radius: 3px;
  background: var(--surface-3);
}
.mail-import-progress-track i {
  display: block;
  height: 100%;
  background: var(--green);
  transition: width 0.2s ease;
}
.mail-import-progress-track i.danger {
  background: var(--red);
}
.login-confirm-dialog {
  width: min(500px, 100%);
}
.login-confirm-dialog p {
  margin-top: 12px;
  color: var(--muted);
  font-size: 11px;
  line-height: 1.65;
}
.login-confirm-dialog p strong {
  color: var(--text-2);
  word-break: break-all;
}
.login-dialog-backdrop {
  z-index: 120;
}
.login-dialog {
  display: flex;
  width: min(760px, 100%);
  max-height: min(760px, calc(100vh - 40px));
  flex-direction: column;
  padding: 0;
  overflow: hidden;
}
.batch-login-dialog {
  display: flex;
  width: min(920px, 100%);
  max-height: min(820px, calc(100vh - 40px));
  flex-direction: column;
  padding: 0;
  overflow: hidden;
}
.at-check-dialog {
  display: flex;
  width: min(780px, 100%);
  max-height: min(760px, calc(100vh - 40px));
  flex-direction: column;
  padding: 0;
  overflow: hidden;
}
.batch-login-summary {
  display: grid;
  grid-template-columns: repeat(4, minmax(90px, 1fr));
  gap: 1px;
  border-bottom: 1px solid var(--line);
  background: var(--line);
}
.at-check-summary {
  grid-template-columns: repeat(5, minmax(80px, 1fr));
}
.batch-login-summary span {
  display: flex;
  min-height: 46px;
  align-items: center;
  justify-content: center;
  gap: 7px;
  background: var(--surface-2);
  color: var(--muted);
  font-size: 10px;
}
.batch-login-summary strong {
  color: var(--text-2);
  font-size: 13px;
}
.batch-login-summary .success strong {
  color: var(--green-strong);
}
.batch-login-summary .danger strong {
  color: var(--red);
}
.batch-login-progress {
  flex: 0 0 3px;
  overflow: hidden;
  background: var(--surface-3);
}
.batch-login-progress i {
  display: block;
  height: 100%;
  background: var(--green);
  transition: width 0.25s ease;
}
.batch-login-progress i.danger {
  background: var(--red);
}
.at-check-list {
  min-height: 240px;
  overflow-y: auto;
}
.at-check-item {
  display: grid;
  min-height: 58px;
  grid-template-columns: 22px minmax(0, 1fr) auto;
  align-items: center;
  gap: 10px;
  padding: 9px 20px;
  border-bottom: 1px solid var(--line-soft);
}
.at-check-item > span:nth-child(2) {
  display: grid;
  min-width: 0;
  gap: 4px;
}
.at-check-item strong,
.at-check-item small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.at-check-item strong {
  color: var(--text-2);
  font-size: 11px;
}
.at-check-item small {
  color: var(--muted);
  font-size: 10px;
}
.batch-login-list {
  min-height: 280px;
  overflow-y: auto;
}
.batch-login-task {
  border-bottom: 1px solid var(--line-soft);
}
.batch-task-summary {
  display: grid;
  width: 100%;
  min-height: 62px;
  grid-template-columns: 22px minmax(0, 1fr) auto 20px;
  align-items: center;
  gap: 10px;
  padding: 9px 20px;
  border: 0;
  background: var(--surface);
  color: var(--text-2);
  text-align: left;
}
.batch-task-summary:hover {
  background: var(--surface-2);
}
.batch-task-summary > svg {
  color: var(--muted);
  transition: transform 0.18s ease;
}
.batch-task-summary > svg.expanded {
  transform: rotate(180deg);
}
.batch-task-state {
  display: grid;
  place-items: center;
  color: var(--muted);
}
.batch-task-state.running,
.batch-task-state.success {
  color: var(--green-strong);
}
.batch-task-state.danger {
  color: var(--red);
}
.batch-task-copy {
  display: grid;
  min-width: 0;
  gap: 4px;
}
.batch-task-copy strong,
.batch-task-copy small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.batch-task-copy strong {
  font-size: 11px;
}
.batch-task-copy small {
  color: var(--muted);
  font-size: 10px;
}
.batch-task-details {
  border-top: 1px solid var(--line-soft);
  background: var(--surface-2);
}
.batch-task-details .login-timeline {
  min-height: 0;
  max-height: 300px;
  padding: 8px 54px 12px;
}
.batch-task-details .timeline-icon {
  background: var(--surface-2);
}
.batch-task-error {
  margin: 0 54px 14px;
  color: var(--red);
  font-size: 10px;
  line-height: 1.55;
  word-break: break-word;
}
.login-dialog-header {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto auto;
  align-items: center;
  gap: 12px;
  padding: 20px 22px 16px;
  border-bottom: 1px solid var(--line);
}
.login-dialog-header h2 {
  margin-top: 3px;
  font-size: 17px;
}
.login-dialog-header p {
  overflow: hidden;
  margin-top: 3px;
  color: var(--muted);
  font-size: 10px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.login-dialog-mark {
  display: grid;
  width: 40px;
  height: 40px;
  border: 1px solid var(--line);
  border-radius: 6px;
  place-items: center;
  color: var(--muted);
  background: var(--surface-2);
}
.login-dialog-mark.success {
  border-color: rgba(30, 150, 105, 0.28);
  color: var(--green-strong);
  background: var(--green-bg);
}
.login-dialog-mark.danger {
  border-color: rgba(219, 112, 112, 0.3);
  color: var(--red);
  background: var(--red-bg);
}
.login-current {
  display: flex;
  min-height: 48px;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 10px 22px;
  background: var(--surface-2);
}
.login-current > div {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 9px;
}
.login-current strong {
  overflow: hidden;
  color: var(--text-2);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.login-current > small {
  flex: none;
  color: var(--muted);
  font-size: 9px;
}
.login-current.success {
  color: var(--green-strong);
}
.login-current.danger {
  color: var(--red);
}
.login-running-line {
  position: relative;
  height: 2px;
  overflow: hidden;
  background: var(--surface-3);
}
.login-running-line i {
  position: absolute;
  width: 38%;
  height: 100%;
  background: var(--green);
  animation: login-scan 1.5s ease-in-out infinite;
}
.login-timeline {
  min-height: 260px;
  margin: 0;
  padding: 12px 22px 18px;
  overflow-y: auto;
  list-style: none;
}
.login-timeline li {
  position: relative;
  display: grid;
  grid-template-columns: 24px minmax(0, 1fr);
  gap: 9px;
  padding: 8px 0;
}
.login-timeline li:not(:last-child)::after {
  position: absolute;
  top: 28px;
  bottom: -5px;
  left: 7px;
  width: 1px;
  background: var(--line);
  content: "";
}
.timeline-icon {
  position: relative;
  z-index: 1;
  display: grid;
  width: 15px;
  height: 15px;
  margin-top: 1px;
  place-items: center;
  color: var(--muted);
  background: var(--surface);
}
.login-timeline li.success .timeline-icon {
  color: var(--green-strong);
}
.login-timeline li.warning .timeline-icon {
  color: var(--amber, #b87918);
}
.login-timeline li.error .timeline-icon {
  color: var(--red);
}
.login-timeline li > div > span {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
}
.login-timeline strong {
  color: var(--text-2);
  font-size: 10px;
}
.login-timeline time {
  color: var(--muted);
  font:
    9px "SFMono-Regular",
    Consolas,
    monospace;
}
.login-timeline p {
  margin-top: 3px;
  color: var(--muted);
  font-size: 10px;
  line-height: 1.55;
  word-break: break-word;
}
.login-timeline li.error p {
  color: var(--red);
}
.login-outcome {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin: 0 22px;
  padding: 11px 12px;
  border: 1px solid var(--line);
  border-radius: 5px;
  background: var(--surface-2);
  color: var(--text-2);
}
.login-outcome.success {
  border-color: rgba(30, 150, 105, 0.28);
  color: var(--green-strong);
  background: var(--green-bg);
}
.login-outcome.danger {
  border-color: rgba(219, 112, 112, 0.3);
  color: var(--red);
  background: var(--red-bg);
}
.login-outcome svg {
  flex: none;
}
.login-outcome div {
  display: grid;
  gap: 4px;
}
.login-outcome strong {
  font-size: 11px;
}
.login-outcome small {
  color: var(--muted);
  font-size: 10px;
  line-height: 1.5;
}
.login-dialog-actions {
  margin-top: 0;
  padding: 16px 22px 20px;
}
.credential-dialog-backdrop {
  z-index: 130;
}
.credential-dialog {
  display: grid;
  width: min(720px, 100%);
  max-height: calc(100vh - 40px);
  gap: 16px;
  overflow: auto;
}
.credential-dialog-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.credential-dialog-header h2 {
  margin-top: 4px;
}
.credential-dialog-header p {
  overflow: hidden;
  margin-top: 5px;
  color: var(--muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.credential-loading {
  display: flex;
  min-height: 190px;
  align-items: center;
  justify-content: center;
  gap: 9px;
  color: var(--muted);
  font-size: 11px;
}
.credential-error {
  padding: 9px 11px;
  border: 1px solid rgba(219, 112, 112, 0.3);
  border-radius: 4px;
  background: var(--red-bg);
  color: var(--red);
  font-size: 11px;
}
.credential-token-field {
  display: grid;
  gap: 7px;
}
.credential-token-field > span {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.credential-token-field strong {
  color: var(--text-2);
  font-size: 11px;
}
.credential-token-field button {
  display: inline-flex;
  min-height: 28px;
  align-items: center;
  gap: 5px;
  padding: 4px 8px;
  border: 1px solid var(--line);
  border-radius: 4px;
  background: var(--surface-2);
  color: var(--text-2);
  font-size: 10px;
}
.credential-token-field button:hover:not(:disabled) {
  border-color: var(--green);
  color: var(--green-strong);
}
.credential-token-field textarea {
  width: 100%;
  min-height: 88px;
  resize: none;
  border: 1px solid var(--line);
  border-radius: 4px;
  outline: 0;
  background: var(--bg-elevated);
  color: var(--text-2);
  padding: 10px;
  font-family: "SFMono-Regular", Consolas, monospace;
  font-size: 10px;
  line-height: 1.55;
  overflow-wrap: anywhere;
}
.credential-token-field textarea:focus {
  border-color: var(--green);
  box-shadow: 0 0 0 2px var(--green-bg);
}
.credential-account-id {
  overflow: hidden;
  color: var(--muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.credential-export-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding-top: 2px;
}
.spin {
  animation: spin 0.9s linear infinite;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
@keyframes login-scan {
  from {
    transform: translateX(-110%);
  }
  to {
    transform: translateX(290%);
  }
}
@media (max-width: 980px) {
  .inbox-grid {
    grid-template-columns: 1fr;
  }
}
@media (max-width: 620px) {
  .inbox-filters {
    align-items: stretch;
    flex-direction: column;
  }
  .inbox-filters > * {
    width: 100%;
  }
  .compact-search input {
    min-width: 0;
    width: 100%;
  }
  .login-dialog-header {
    position: relative;
    grid-template-columns: auto minmax(0, 1fr);
    padding-right: 52px;
  }
  .login-dialog-header > .status-pill {
    grid-column: 2;
    justify-self: start;
  }
  .login-dialog-header > .icon-button {
    position: absolute;
    top: 18px;
    right: 14px;
  }
  .batch-login-summary {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .batch-task-summary {
    grid-template-columns: 20px minmax(0, 1fr) 18px;
    padding: 9px 12px;
  }
  .batch-task-summary > .status-pill {
    grid-column: 2;
    justify-self: start;
  }
  .batch-task-summary > svg {
    grid-column: 3;
    grid-row: 1;
  }
  .batch-task-details .login-timeline {
    padding-right: 18px;
    padding-left: 18px;
  }
  .batch-task-error {
    margin-right: 18px;
    margin-left: 18px;
  }
}
</style>
