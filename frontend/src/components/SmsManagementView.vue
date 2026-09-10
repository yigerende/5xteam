<script setup>
import { computed, onMounted, reactive, ref } from "vue";
import {
  Copy,
  History,
  MessageSquareText,
  Plus,
  RefreshCw,
  Save,
  Smartphone,
  Trash2,
  X,
} from "lucide-vue-next";
import { api } from "../api";
import MessageBar from "./MessageBar.vue";
import StatusPill from "./StatusPill.vue";
import Pagination from "./Pagination.vue";

const providers = ref([]);
const phones = ref([]);
const stats = ref({});
const loading = ref(false);
const busy = ref("");
const message = reactive({ text: "", type: "" });
const importOpen = ref(false);
const importForm = reactive({
  provider: "chongpt",
  text: "",
  max_bindings: 3,
  note: "",
});
const codeDialog = ref(null);
const codeResult = ref(null);
const bindDialog = ref(null);
const bindEmail = ref("");
const configProvider = ref("hero_sms");
const config = reactive({
  provider: "",
  enabled: false,
  api_key: "",
  api_key_set: false,
  base_url: "",
  service: "",
  country: "",
  max_price: "",
  cdks: "",
  cdks_count: 0,
});
const platformBalance = ref("");
const activationHistory = ref([]);
const historyLoading = ref(false);
const smsMenu = ref("pool");
const phonePage = ref(1);
const phonePageSize = ref(10);
const phoneTotal = ref(0);
const activationPage = ref(1);
const activationPageSize = ref(10);
const activationTotal = ref(0);

const providerLabel = (key) =>
  ({
    chongpt: "chongpt",
    chong10666: "10666 接码",
    generic: "通用接码",
    hero_sms: "hero-sms",
    nextpro: "nextpro",
    congou: "congou",
    chatai: "chatai",
  })[key] || key;
const stateLabel = (value) =>
  ({
    available: "可用",
    cooldown: "冷却中",
    full: "已绑满",
    expired: "已过期",
    disabled: "停用",
  })[value] ||
  value ||
  "未知";
const activationStatus = (value) =>
  ({
    1: "等待短信",
    2: "已收到短信",
    3: "取消",
    4: "完成",
    5: "失败",
    6: "完成",
    8: "取消/超时",
  })[String(value)] ||
  value ||
  "未知";
const activationTone = (value) =>
  ["2", "4", "6"].includes(String(value))
    ? "success"
    : ["3", "5", "8"].includes(String(value))
      ? "danger"
      : "pending";
const isCardProvider = computed(
  () => !["generic"].includes(importForm.provider),
);
const configProviders = computed(() =>
  providers.value.filter((item) =>
    ["hero_sms", "nextpro", "congou", "chatai"].includes(item.key),
  ),
);

function setMessage(text = "", type = "") {
  Object.assign(message, { text, type });
}
function activationTimestamp(item) {
  const value = item?.created_at ?? item?.createdAt ?? item?.timestamp ?? item?.time ?? item?.date;
  if (value == null || value === "") return 0;
  if (typeof value === "number" || /^\d+(\.\d+)?$/.test(String(value).trim())) {
    const numeric = Number(value);
    return numeric < 1e12 ? numeric * 1000 : numeric;
  }
  const parsed = Date.parse(String(value));
  return Number.isFinite(parsed) ? parsed : 0;
}
function formatActivationTime(item) {
  const timestamp = activationTimestamp(item);
  if (!timestamp) return "-";
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(new Date(timestamp));
}
async function load() {
  loading.value = true;
  try {
    const [providerData] = await Promise.all([
      api("/api/sms/providers"),
      loadPhones(),
    ]);
    providers.value = providerData.providers || [];
    if (
      !configProviders.value.some(
        (item) => item.key === configProvider.value,
      ) &&
      configProviders.value[0]
    )
      configProvider.value = configProviders.value[0].key;
    await loadConfig();
    await loadActivationHistory();
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    loading.value = false;
  }
}
async function loadPhones() {
  const result = await api(`/api/sms/phones?page=${phonePage.value}&page_size=${phonePageSize.value}`);
  phones.value = result.items || result.phones || [];
  stats.value = result.stats || {};
  phoneTotal.value = Number(result.total || 0);
  const lastPage = Math.max(1, Math.ceil(phoneTotal.value / phonePageSize.value));
  if (phonePage.value > lastPage) {
    phonePage.value = lastPage;
    return loadPhones();
  }
}
function setPhonePage(value) {
  phonePage.value = value;
  loadPhones().catch((error) => setMessage(error.message, "error"));
}
function setPhonePageSize(value) {
  phonePageSize.value = value;
  phonePage.value = 1;
  loadPhones().catch((error) => setMessage(error.message, "error"));
}
async function loadActivationHistory() {
  if (configProvider.value !== "hero_sms") {
    activationHistory.value = [];
    activationTotal.value = 0;
    return;
  }
  historyLoading.value = true;
  try {
    const result = await api(
      `/api/sms/platform/history?provider=hero_sms&page=${activationPage.value}&page_size=${activationPageSize.value}`,
    );
    const items = Array.isArray(result.items) ? result.items : [];
    activationHistory.value = [...items].sort((a, b) => activationTimestamp(b) - activationTimestamp(a));
    activationTotal.value = Number(result.total || 0);
    const lastPage = Math.max(1, Math.ceil(activationTotal.value / activationPageSize.value));
    if (activationPage.value > lastPage) {
      activationPage.value = lastPage;
      return loadActivationHistory();
    }
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    historyLoading.value = false;
  }
}
function setActivationPage(value) {
  activationPage.value = value;
  loadActivationHistory();
}
function setActivationPageSize(value) {
  activationPageSize.value = value;
  activationPage.value = 1;
  loadActivationHistory();
}
async function loadConfig() {
  if (!configProvider.value) return;
  try {
    const result = await api(
      `/api/sms/platform/config?provider=${encodeURIComponent(configProvider.value)}`,
    );
    Object.assign(config, result.config || result);
  } catch (error) {
    setMessage(error.message, "error");
  }
}
async function importPhones() {
  if (!importForm.text.trim())
    return setMessage(
      isCardProvider.value ? "请填写卡密" : "请填写手机号----接码 URL",
      "error",
    );
  busy.value = "import";
  try {
    const result = await api("/api/sms/phones/import", {
      method: "POST",
      body: {
        ...importForm,
        max_bindings: Number(importForm.max_bindings) || 3,
      },
    });
    importOpen.value = false;
    importForm.text = "";
    setMessage(
      `导入完成：新增 ${result.added || 0} 个，跳过 ${result.skipped || 0} 个`,
      result.errors?.length ? "error" : "success",
    );
    phonePage.value = 1;
    await loadPhones();
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}
async function deletePhone(phone) {
  if (
    !window.confirm(
      `确认删除 ${phone.phone_number || phone.card_code_masked || "这条号源"}？`,
    )
  )
    return;
  busy.value = phone.id;
  try {
    await api("/api/sms/phones/delete", {
      method: "POST",
      body: { ids: [phone.id] },
    });
    setMessage("号源已删除", "success");
    await loadPhones();
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}
async function updatePhone(phone, patch) {
  busy.value = phone.id;
  try {
    await api("/api/sms/phones/update", {
      method: "POST",
      body: { id: phone.id, ...patch },
    });
    setMessage("号源状态已更新", "success");
    await loadPhones();
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}
async function bindPhone() {
  if (!bindEmail.value.trim())
    return setMessage("请输入 GPT 账号邮箱", "error");
  busy.value = bindDialog.value.id;
  try {
    await api("/api/sms/phones/bind", {
      method: "POST",
      body: { id: bindDialog.value.id, gpt_email: bindEmail.value.trim() },
    });
    bindDialog.value = null;
    bindEmail.value = "";
    setMessage("绑定成功", "success");
    await loadPhones();
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}
async function fetchCode(phone) {
  codeDialog.value = phone;
  codeResult.value = null;
  busy.value = phone.id;
  try {
    codeResult.value = await api("/api/sms/phones/fetch-code", {
      method: "POST",
      body: { id: phone.id },
    });
    await loadPhones();
  } catch (error) {
    codeResult.value = { found: false, message: error.message };
  } finally {
    busy.value = "";
  }
}
async function saveConfig() {
  busy.value = "config";
  try {
    await api("/api/sms/platform/config", {
      method: "POST",
      body: {
        ...config,
        provider: configProvider.value,
        enabled: !!config.enabled,
      },
    });
    setMessage(`${providerLabel(configProvider.value)} 配置已保存`, "success");
    await loadConfig();
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}
async function checkPlatformBalance() {
  busy.value = "balance";
  try {
    const result = await api(
      `/api/sms/platform/balance?provider=${encodeURIComponent(configProvider.value)}`,
    );
    if (!result.success && result.ok === false)
      throw new Error(result.error || result.message || "余额查询失败");
    platformBalance.value = result.balance ?? result.data?.balance ?? "";
    setMessage(`余额：${platformBalance.value || "平台未返回余额"}`, "success");
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}
async function testPlatform() {
  busy.value = "test-platform";
  try {
    const result = await api("/api/sms/platform/test", {
      method: "POST",
      body: { provider: configProvider.value },
    });
    if (!result.success && result.ok === false)
      throw new Error(result.error || result.message || "测试申请失败");
    setMessage(
      result.message ||
        `测试申请成功${result.phone ? `：${result.phone}` : ""}`,
      "success",
    );
  } catch (error) {
    setMessage(error.message, "error");
  } finally {
    busy.value = "";
  }
}
async function copy(value) {
  if (!value) return;
  try {
    await navigator.clipboard.writeText(value);
    setMessage("已复制", "success");
  } catch {
    setMessage("复制失败", "error");
  }
}
onMounted(load);
</script>

<template>
  <section class="view-stack">
    <header class="page-heading">
      <div>
        <span class="overline">SMS POOL</span>
        <h1>接码管理</h1>
        <p>
          维护手机号码池和实时接码平台；Team OAuth 只有遇到 add_phone
          才会按需取号。
        </p>
      </div>
      <div class="heading-actions">
        <button
          class="btn ghost"
          type="button"
          :disabled="loading"
          @click="load"
        >
          <RefreshCw :size="15" />刷新</button
        ><button class="btn primary" type="button" @click="importOpen = true">
          <Plus :size="15" />导入号源
        </button>
      </div>
    </header>
    <MessageBar :message="message" />
    <nav class="sms-subnav" aria-label="接码管理菜单"><button type="button" :class="{ active: smsMenu === 'pool' }" @click="smsMenu = 'pool'"><Smartphone :size="14" />号码池</button><button type="button" :class="{ active: smsMenu === 'settings' }" @click="smsMenu = 'settings'"><Save :size="14" />实时接码平台设置</button><button type="button" :class="{ active: smsMenu === 'history' }" @click="smsMenu = 'history'; loadActivationHistory()"><History :size="14" />激活历史</button></nav>
    <div class="stat-grid compact top-cards">
      <div class="stat-card">
        <span>总号源</span><strong>{{ stats.total ?? phones.length }}</strong>
      </div>
      <div class="stat-card">
        <span>可用</span><strong>{{ stats.available || 0 }}</strong>
      </div>
      <div class="stat-card">
        <span>冷却中</span><strong>{{ stats.cooldown || 0 }}</strong>
      </div>
      <div class="stat-card">
        <span>已绑满</span><strong>{{ stats.full || 0 }}</strong>
      </div>
      <div class="stat-card">
        <span>停用</span><strong>{{ stats.disabled || 0 }}</strong>
      </div>
      <div class="stat-card history-card">
        <span>hero-sms 激活历史</span
        ><strong>{{ activationTotal }}</strong
        ><small>{{ historyLoading ? "读取中…" : "激活记录" }}</small>
      </div>
    </div>
    <section v-if="smsMenu === 'pool'" class="panel">
      <div class="panel-title responsive">
        <div>
          <span>PHONE POOL</span>
          <h2>号码池</h2>
        </div>
        <StatusPill tone="success"
          ><Smartphone :size="13" />仅在需要时接码</StatusPill
        >
      </div>
      <div class="table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>手机号</th>
              <th>平台</th>
              <th>卡密 / 接码地址</th>
              <th>绑定</th>
              <th>状态</th>
              <th>最近验证码</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="phone in phones" :key="phone.id">
              <td class="mono">{{ phone.phone_number || "实时申请" }}</td>
              <td>{{ providerLabel(phone.provider) }}</td>
              <td
                class="mono truncate-cell"
                :title="
                  phone.provider === 'generic'
                    ? phone.api_url
                    : phone.card_code_masked
                "
              >
                {{
                  phone.provider === "generic"
                    ? phone.api_url
                    : phone.card_code_masked || "-"
                }}
              </td>
              <td>
                {{ phone.bind_count || 0 }}/{{ phone.max_bindings || 0 }}
                <div
                  v-for="binding in phone.bindings || []"
                  :key="binding.gpt_email"
                  class="table-note"
                >
                  {{ binding.gpt_email }}
                </div>
              </td>
              <td>
                <StatusPill
                  :tone="
                    phone.state === 'available'
                      ? 'success'
                      : phone.state === 'disabled'
                        ? 'danger'
                        : 'pending'
                  "
                  >{{ stateLabel(phone.state) }}</StatusPill
                >
              </td>
              <td class="mono">{{ phone.last_code || "-" }}</td>
              <td>
                <div class="row-actions">
                  <button
                    class="btn ghost compact"
                    type="button"
                    :disabled="busy === phone.id"
                    @click="fetchCode(phone)"
                  >
                    <MessageSquareText :size="14" />接码</button
                  ><button
                    class="btn ghost compact"
                    type="button"
                    :disabled="busy === phone.id"
                    @click="bindDialog = phone"
                  >
                    绑定</button
                  ><button
                    class="btn ghost compact"
                    type="button"
                    :disabled="busy === phone.id"
                    @click="
                      updatePhone(phone, {
                        status:
                          phone.status === 'disabled' ? 'active' : 'disabled',
                      })
                    "
                  >
                    {{ phone.status === "disabled" ? "启用" : "停用" }}</button
                  ><button
                    class="icon-button"
                    type="button"
                    title="复制手机号"
                    @click="copy(phone.phone_number)"
                  >
                    <Copy :size="14" /></button
                  ><button
                    class="icon-button danger"
                    type="button"
                    title="删除"
                    :disabled="busy === phone.id"
                    @click="deletePhone(phone)"
                  >
                    <Trash2 :size="14" />
                  </button>
                </div>
              </td>
            </tr>
            <tr v-if="!phones.length">
              <td colspan="7" class="empty-cell">
                暂无号源，请先导入号码或在下方配置实时平台。
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <Pagination :page="phonePage" :page-size="phonePageSize" :total="phoneTotal" @update:page="setPhonePage" @update:page-size="setPhonePageSize" />
    </section>
    <section v-if="smsMenu === 'settings' && configProviders.length" class="panel">
      <div class="panel-title">
        <div>
          <span>REALTIME PROVIDER</span>
          <h2>实时接码平台</h2>
        </div>
        <select
          v-model="configProvider"
          @change="
            platformBalance = '';
            loadConfig();
          "
        >
          <option
            v-for="item in configProviders"
            :key="item.key"
            :value="item.key"
          >
            {{ providerLabel(item.key) }}
          </option>
        </select>
      </div>
      <div class="settings-fields">
        <label class="field"
          ><span
            >API Key
            <small v-if="config.api_key_set">已配置，留空保留</small></span
          ><input
            v-model="config.api_key"
            type="password"
            placeholder="平台 API Key" /></label
        ><label class="field"
          ><span>Base URL</span
          ><input v-model="config.base_url" placeholder="https://..." /></label
        ><label class="field"
          ><span>Service</span
          ><input v-model="config.service" placeholder="dr" /></label
        ><label class="field"
          ><span>Country</span
          ><input v-model="config.country" placeholder="33" /></label
        ><label class="field"
          ><span>Max Price</span
          ><input v-model="config.max_price" placeholder="1" /></label
        ><label class="field wide"
          ><span>CDK 卡密池（每行一个）</span
          ><textarea
            v-model="config.cdks"
            rows="3"
            placeholder="卡密制平台填写；hero-sms 留空"
          ></textarea></label
        ><label class="toggle-row wide"
          ><div>
            <strong>启用 {{ providerLabel(configProvider) }}</strong
            ><small>Team OAuth 遇到 add_phone 时按需申请</small>
          </div>
          <input v-model="config.enabled" type="checkbox" /><i></i
        ></label>
      </div>
      <div class="panel-actions">
        <button
          class="btn primary"
          type="button"
          :disabled="busy === 'config'"
          @click="saveConfig"
        >
          <Save :size="15" />保存平台配置</button
        ><button
          class="btn ghost"
          type="button"
          :disabled="!!busy"
          @click="checkPlatformBalance"
        >
          {{
            busy === "balance"
              ? "查询中…"
              : `查余额${platformBalance !== "" ? `：${platformBalance}` : ""}`
          }}</button
        ><button
          class="btn ghost"
          type="button"
          :disabled="!!busy"
          @click="testPlatform"
        >
          {{ busy === "test-platform" ? "测试申请中…" : "测试申请一个号" }}
        </button>
      </div>
    </section>
    <section v-if="smsMenu === 'history' && configProvider === 'hero_sms'" class="panel">
      <div class="panel-title">
        <div>
          <span>ACTIVATION HISTORY</span>
          <h2>激活历史</h2>
        </div>
        <button
          class="btn ghost compact"
          type="button"
          :disabled="historyLoading"
          @click="loadActivationHistory"
        >
          <RefreshCw :size="14" />刷新历史
        </button>
      </div>
      <div class="table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>手机号</th>
              <th>服务</th>
              <th>国家</th>
              <th>状态</th>
              <th>验证码</th>
              <th>价格</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="(item, index) in activationHistory"
              :key="item.id || item.activation_id || index"
            >
              <td>{{ formatActivationTime(item) }}</td>
              <td class="mono">
                {{ item.phone_number || item.phone || item.number || "-" }}
              </td>
              <td>
                {{
                  item.service || item.service_name || item.service_code || "-"
                }}
              </td>
              <td>{{ item.country || item.country_code || "-" }}</td>
              <td>
                <StatusPill :tone="activationTone(item.status ?? item.state)">{{
                  activationStatus(item.status ?? item.state)
                }}</StatusPill>
              </td>
              <td class="mono">
                {{ item.code || item.sms_code || item.sms || "-" }}
              </td>
              <td>
                {{ item.price ?? item.cost ?? "-"
                }}<small v-if="item.currency" class="table-note">
                  · {{ item.currency }}</small
                >
              </td>
            </tr>
            <tr v-if="!activationHistory.length">
              <td colspan="7" class="empty-cell">
                {{ historyLoading ? "正在读取激活历史…" : "暂无激活历史" }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <Pagination :page="activationPage" :page-size="activationPageSize" :total="activationTotal" @update:page="setActivationPage" @update:page-size="setActivationPageSize" />
    </section>
    <div
      v-if="importOpen"
      class="modal-backdrop"
      @click.self="importOpen = false"
    >
      <form class="modal mail-import-modal" @submit.prevent="importPhones">
        <span class="overline">IMPORT SMS SOURCE</span>
        <h2>导入接码号源</h2>
        <label class="field"
          ><span>平台</span
          ><select v-model="importForm.provider">
            <option value="chongpt">chongpt 卡密</option>
            <option value="chong10666">10666 卡密</option>
            <option value="generic">通用：手机号----接码 URL</option>
          </select></label
        ><label class="field"
          ><span>{{
            isCardProvider
              ? "卡密（每行一个）"
              : "手机号----接码 URL（每行一个）"
          }}</span
          ><textarea
            v-model="importForm.text"
            rows="7"
            spellcheck="false"
            :placeholder="
              isCardProvider
                ? '粘贴卡密，每行一个'
                : '+13655183077----https://example.com/sms?phone={phone}'
            "
            required
          ></textarea>
        </label>
        <div class="field-row">
          <label class="field"
            ><span>每号绑定上限</span
            ><input
              v-model.number="importForm.max_bindings"
              type="number"
              min="1"
              max="10" /></label
          ><label class="field"
            ><span>备注</span><input v-model="importForm.note"
          /></label>
        </div>
        <div class="panel-actions">
          <button class="btn ghost" type="button" @click="importOpen = false">
            取消</button
          ><button
            class="btn primary"
            type="submit"
            :disabled="busy === 'import'"
          >
            确认导入
          </button>
        </div>
      </form>
    </div>
    <div
      v-if="codeDialog"
      class="modal-backdrop"
      @click.self="codeDialog = null"
    >
      <section class="modal">
        <div class="panel-title">
          <div>
            <span>SMS CODE</span>
            <h2>接收验证码</h2>
          </div>
          <button class="icon-button" type="button" @click="codeDialog = null">
            <X :size="16" />
          </button>
        </div>
        <p class="mono">
          {{ codeDialog.phone_number || providerLabel(codeDialog.provider) }}
        </p>
        <div class="code-result">
          <strong v-if="codeResult?.found">{{ codeResult.code }}</strong
          ><span v-else>{{ codeResult?.message || "正在查询…" }}</span>
        </div>
        <button
          v-if="codeResult?.code"
          class="btn ghost"
          type="button"
          @click="copy(codeResult.code)"
        >
          <Copy :size="14" />复制验证码
        </button>
      </section>
    </div>
    <div
      v-if="bindDialog"
      class="modal-backdrop"
      @click.self="bindDialog = null"
    >
      <form class="modal" @submit.prevent="bindPhone">
        <div class="panel-title">
          <div>
            <span>BIND PHONE</span>
            <h2>绑定 GPT 账号</h2>
          </div>
          <button class="icon-button" type="button" @click="bindDialog = null">
            <X :size="16" />
          </button>
        </div>
        <p class="mono">{{ bindDialog.phone_number }}</p>
        <label class="field"
          ><span>GPT 账号邮箱</span
          ><input
            v-model="bindEmail"
            type="email"
            required
            placeholder="free@example.com"
        /></label>
        <div class="panel-actions">
          <button class="btn ghost" type="button" @click="bindDialog = null">
            取消</button
          ><button
            class="btn primary"
            type="submit"
            :disabled="busy === bindDialog.id"
          >
            确认绑定
          </button>
        </div>
      </form>
    </div>
  </section>
</template>
