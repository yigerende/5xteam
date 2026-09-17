<script setup>
import { computed, onBeforeUnmount, reactive } from "vue";
import { Check, Copy, Download, KeyRound, LoaderCircle, RefreshCw, Save, X } from "lucide-vue-next";
import { api } from "../api";
import { copyText } from "../clipboard";
import IconButton from "./IconButton.vue";

const emit = defineEmits(["exported", "saved"]);
const editableFields = { gptPassword: 'gpt_password', totpSecret: 'totp_secret', accessToken: 'access_token', refreshToken: 'refresh_token', chatgptSession: 'chatgpt_session', accountID: 'chatgpt_account_id' };

const credentialDialog = reactive({
  open: false,
  email: "",
  gptPassword: "",
  totpSecret: "",
  accessToken: "",
  refreshToken: "",
  chatgptSession: "",
  accountID: "",
  loading: false,
  exporting: "",
  copied: "",
  error: "",
  revision: "",
  original: {},
  saving: false,
  saved: false,
  loaded: false,
});
const hasChanges = computed(() => credentialDialog.loaded && Object.keys(editableFields).some(key => credentialDialog[key] !== credentialDialog.original[key]));
const totpCode = reactive({ value: "", loading: false, error: "", remaining: 0 });
let credentialRequestID = 0;
let totpTimer;
let totpDeadline = 0;
async function openCredentialDialog(account) {
  const requestID = ++credentialRequestID;
  resetTotpCode();
  Object.assign(credentialDialog, {
    open: true,
    email: account.email,
    gptPassword: "",
    totpSecret: "",
    accessToken: "",
    refreshToken: "",
    chatgptSession: "",
    accountID: "",
    loading: true,
    exporting: "",
    copied: "",
    error: "",
    revision: "",
    original: {},
    saving: false,
    saved: false,
    loaded: false,
  });
  try {
    const credentials = await api(
      `/api/mail/accounts/${encodeURIComponent(account.email)}/credentials`,
    );
    if (requestID !== credentialRequestID) return;
    Object.assign(credentialDialog, {
      gptPassword: credentials.gpt_password || "",
      totpSecret: credentials.totp_secret || "",
      accessToken: credentials.access_token || "",
      refreshToken: credentials.refresh_token || "",
      chatgptSession: formatChatGPTSession(credentials.chatgpt_session),
      accountID: credentials.chatgpt_account_id || "",
      revision: credentials.revision || "",
    });
    credentialDialog.original = Object.fromEntries(Object.keys(editableFields).map(key => [key, credentialDialog[key]]));
    credentialDialog.loaded = true;
  } catch (error) {
    if (requestID === credentialRequestID) credentialDialog.error = error.message;
  } finally {
    if (requestID === credentialRequestID) credentialDialog.loading = false;
  }
}
function closeCredentialDialog(force = false) {
  if (force !== true && credentialDialog.saving) return;
  if (force !== true && hasChanges.value && !window.confirm('有未保存的凭证修改，确认放弃？')) return;
  credentialRequestID++;
  resetTotpCode();
  Object.assign(credentialDialog, {
    open: false,
    email: "",
    gptPassword: "",
    totpSecret: "",
    accessToken: "",
    refreshToken: "",
    chatgptSession: "",
    accountID: "",
    loading: false,
    exporting: "",
    copied: "",
    error: "",
    revision: "",
    original: {},
    saving: false,
    saved: false,
    loaded: false,
  });
}
async function saveCredentials() {
  if (!hasChanges.value || credentialDialog.saving || credentialDialog.exporting) return;
  const requestID = credentialRequestID;
  const email = credentialDialog.email;
  const body = { revision: credentialDialog.revision };
  for (const [key, field] of Object.entries(editableFields)) {
    if (credentialDialog[key] !== credentialDialog.original[key]) body[field] = credentialDialog[key];
  }
  credentialDialog.saving = true;
  credentialDialog.error = '';
  credentialDialog.saved = false;
  resetTotpCode();
  try {
    await api(`/api/mail/accounts/${encodeURIComponent(email)}/credentials`, { method: 'PATCH', body });
    emit('saved', email);
    if (requestID !== credentialRequestID) return;
    await openCredentialDialog({ email });
    if (credentialDialog.email === email && credentialDialog.loaded) credentialDialog.saved = true;
  } catch (error) {
    if (requestID === credentialRequestID) credentialDialog.error = error.message;
  } finally {
    if (requestID === credentialRequestID) credentialDialog.saving = false;
  }
}
function resetTotpCode() {
  window.clearInterval(totpTimer);
  if (credentialDialog.copied === "totp-code") credentialDialog.copied = "";
  totpDeadline = 0;
  Object.assign(totpCode, { value: "", loading: false, error: "", remaining: 0 });
}
function updateTotpRemaining() {
  totpCode.remaining = Math.max(0, Math.ceil((totpDeadline - performance.now()) / 1000));
  if (!totpCode.remaining) window.clearInterval(totpTimer);
}
async function showTotpCode() {
  if (!credentialDialog.totpSecret || totpCode.loading || credentialDialog.saving || credentialDialog.totpSecret !== credentialDialog.original.totpSecret) return;
  resetTotpCode();
  totpCode.loading = true;
  const requestID = credentialRequestID;
  const secret = credentialDialog.totpSecret;
  const started = performance.now();
  try {
    const result = await api(`/api/mail/accounts/${encodeURIComponent(credentialDialog.email)}/totp`, { cache: "no-store" });
    if (requestID !== credentialRequestID || secret !== credentialDialog.totpSecret) return;
    if (!/^\d{6}$/.test(result.code) || !Number.isFinite(result.valid_for_ms)) throw new Error("验证码响应异常，请重试");
    totpCode.value = result.code;
    // Use server validity and monotonic elapsed time, not the PC's wall clock.
    totpDeadline = started + result.valid_for_ms;
    updateTotpRemaining();
    if (totpCode.remaining) totpTimer = window.setInterval(updateTotpRemaining, 250);
  } catch (error) {
    if (requestID === credentialRequestID && secret === credentialDialog.totpSecret) totpCode.error = error.message;
  } finally {
    if (requestID === credentialRequestID && secret === credentialDialog.totpSecret) totpCode.loading = false;
  }
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
function parseSessionAccessToken() {
  credentialDialog.saved = false;
  credentialDialog.error = '';
  const raw = credentialDialog.chatgptSession.trim();
  if (!raw) return;
  try {
    const session = JSON.parse(raw);
    if (!session || typeof session !== 'object' || Array.isArray(session)) throw new Error();
    const token = session.accessToken || session.access_token;
    if (typeof token === 'string' && token.trim()) credentialDialog.accessToken = token.trim();
  } catch {
    credentialDialog.error = 'Session JSON 格式不完整，请检查后保存；原 AT 已保留';
  }
}
async function copyCredential(kind) {
  if (kind === "totp-code") {
    updateTotpRemaining();
    if (!totpCode.remaining) return;
  }
  const value = {
    at: credentialDialog.accessToken,
    rt: credentialDialog.refreshToken,
    password: credentialDialog.gptPassword,
    totp: credentialDialog.totpSecret,
    "totp-code": totpCode.value,
    session: credentialDialog.chatgptSession,
  }[kind];
  if (!value) return;
  const requestID = credentialRequestID;
  try {
    await copyText(value);
    if (requestID !== credentialRequestID) return;
    if (credentialDialog.error === "复制失败，请选中凭证后手动复制") credentialDialog.error = "";
    credentialDialog.copied = kind;
    window.setTimeout(() => {
      if (requestID === credentialRequestID && credentialDialog.copied === kind) credentialDialog.copied = "";
    }, 1600);
  } catch {
    if (requestID === credentialRequestID) credentialDialog.error = "复制失败，请选中凭证后手动复制";
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
  if (hasChanges.value || credentialDialog.saving || credentialDialog.exporting || !credentialDialog.accessToken || !credentialDialog.refreshToken) return;
  const requestID = credentialRequestID;
  const email = credentialDialog.email;
  credentialDialog.exporting = format;
  credentialDialog.error = "";
  try {
    const credentials = await api(
      `/api/mail/accounts/${encodeURIComponent(email)}/credentials?format=${format}`,
    );
    if (requestID !== credentialRequestID) return;
    const safeEmail = email.replace(/[^a-z0-9@._-]+/gi, "_");
    const suffix = format === "cpa" ? "cpa-auth" : "sub2api-account";
    downloadJSON(`${safeEmail}-${suffix}.json`, credentials);
    emit("exported",
      `${email}：${format === "cpa" ? "CPA" : "Sub2"} JSON 已导出`,
    );
  } catch (error) {
    if (requestID === credentialRequestID) credentialDialog.error = error.message;
  } finally {
    if (requestID === credentialRequestID) credentialDialog.exporting = "";
  }
}

onBeforeUnmount(() => closeCredentialDialog(true));
defineExpose({ open: openCredentialDialog });
</script>

<template>
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
            <h2 id="credential-dialog-title">账号凭证</h2>
            <p>{{ credentialDialog.email }}</p>
          </div>
          <IconButton label="关闭凭证窗口" :disabled="credentialDialog.saving" @click="closeCredentialDialog"
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
          <p v-if="credentialDialog.saved && !hasChanges" class="credential-success" role="status">凭证已保存</p>
          <fieldset class="credential-fields" :disabled="credentialDialog.saving || !credentialDialog.loaded">
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
            ><input v-model="credentialDialog.gptPassword" aria-label="ChatGPT 密码" placeholder="未设置" autocomplete="off" spellcheck="false"
          /></label>
          <div class="credential-token-field">
            <span><strong>OpenAI 2FA 密钥</strong><button type="button" :disabled="!credentialDialog.totpSecret" @click="copyCredential('totp')"><Check v-if="credentialDialog.copied === 'totp'" :size="14" /><Copy v-else :size="14" />{{ credentialDialog.copied === 'totp' ? '已复制' : '复制 2FA' }}</button></span>
            <input v-model="credentialDialog.totpSecret" aria-label="OpenAI 2FA 密钥" placeholder="未配置" autocomplete="off" spellcheck="false" @input="resetTotpCode" @focus="$event.target.select()" />
            <div v-if="credentialDialog.totpSecret" class="credential-totp-code">
              <code v-if="totpCode.value">{{ totpCode.remaining ? totpCode.value : '------' }}</code>
              <small v-if="totpCode.value">{{ totpCode.remaining ? `${totpCode.remaining} 秒后过期` : '已过期' }}</small>
              <button type="button" :disabled="totpCode.loading || credentialDialog.totpSecret !== credentialDialog.original.totpSecret" :title="credentialDialog.totpSecret !== credentialDialog.original.totpSecret ? '请先保存 2FA 密钥' : ''" @click="showTotpCode"><LoaderCircle v-if="totpCode.loading" class="spin" :size="14" /><RefreshCw v-else-if="totpCode.value" :size="14" /><KeyRound v-else :size="14" />{{ totpCode.value ? '刷新' : '查看验证码' }}</button>
              <button v-if="totpCode.value" type="button" :disabled="!totpCode.remaining || totpCode.loading" @click="copyCredential('totp-code')"><Check v-if="credentialDialog.copied === 'totp-code'" :size="14" /><Copy v-else :size="14" />{{ credentialDialog.copied === 'totp-code' ? '已复制' : '复制验证码' }}</button>
            </div>
            <p v-if="totpCode.error" class="danger-text" role="alert">{{ totpCode.error }}</p>
          </div>
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
              v-model="credentialDialog.accessToken"
              aria-label="Access Token (AT)"
              placeholder="未获取"
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
              v-model="credentialDialog.refreshToken"
              aria-label="Refresh Token (RT)"
              placeholder="未获取"
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
              v-model="credentialDialog.chatgptSession"
              @input="parseSessionAccessToken"
              aria-label="完整 ChatGPT Session"
              placeholder="未保存"
              rows="8"
              spellcheck="false"
              @focus="$event.target.select()"
            ></textarea>
          </label>
          <label class="credential-token-field"><span><strong>Account ID</strong></span><input v-model="credentialDialog.accountID" aria-label="Account ID" placeholder="未获取" spellcheck="false" /></label>
          </fieldset>
          <footer class="credential-export-actions">
            <button
              class="btn ghost"
              type="button"
              :disabled="
                hasChanges || credentialDialog.saving || !!credentialDialog.exporting || !credentialDialog.accessToken || !credentialDialog.refreshToken
              "
              :title="hasChanges ? '请先保存修改' : ''"
              @click="exportCredentialFormat('cpa')"
            >
              <LoaderCircle
                v-if="credentialDialog.exporting === 'cpa'"
                class="spin"
                :size="15"
              /><Download v-else :size="15" />导出 CPA JSON</button
            ><button
              class="btn ghost"
              type="button"
              :disabled="
                hasChanges || credentialDialog.saving || !!credentialDialog.exporting || !credentialDialog.accessToken || !credentialDialog.refreshToken
              "
              :title="hasChanges ? '请先保存修改' : ''"
              @click="exportCredentialFormat('sub2')"
            >
              <LoaderCircle
                v-if="credentialDialog.exporting === 'sub2'"
                class="spin"
                :size="15"
              /><Download v-else :size="15" />导出 Sub2 JSON
            </button>
            <button class="btn primary" type="button" :disabled="!hasChanges || credentialDialog.saving || !!credentialDialog.exporting" @click="saveCredentials"><LoaderCircle v-if="credentialDialog.saving" class="spin" :size="15" /><Save v-else :size="15" />{{ credentialDialog.saving ? '保存中' : '保存修改' }}</button>
          </footer>
        </template>
      </section>
    </div>
</template>

<style scoped>
.credential-dialog-backdrop {
  z-index: 130;
}
.credential-dialog {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
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
.credential-dialog-header > div {
  min-width: 0;
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
  min-width: 0;
  gap: 7px;
}
.credential-fields {
  display: grid;
  gap: 16px;
  min-width: 0;
  margin: 0;
  padding: 0;
  border: 0;
}
.credential-success { color: var(--green-strong); font-size: 12px; }
.credential-token-field input {
  width: 100%;
  min-width: 0;
}
.credential-totp-code {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.credential-totp-code code {
  min-width: 72px;
  color: var(--blue);
  font-size: 18px;
  font-variant-numeric: tabular-nums;
}
.credential-totp-code small {
  min-width: 70px;
  color: var(--muted);
  font-size: 11px;
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
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
  padding-top: 2px;
}
.spin { animation: spin 0.9s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
