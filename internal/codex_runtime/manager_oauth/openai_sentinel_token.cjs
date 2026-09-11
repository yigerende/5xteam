/**
 * OpenAI Sentinel token helper.
 *
 * The current auth.openai.com account flow expects an openai-sentinel-token
 * header before it reliably sends OTP mail. This module runs the public
 * Sentinel SDK in a minimal browser-like VM and exchanges the generated proof
 * with sentinel.openai.com, matching the flow used by the referenced project.
 */

const vm = require('vm');
const fs = require('fs');
const path = require('path');
const os = require('os');

try {
  const proxyUrl = process.env.HTTPS_PROXY || process.env.HTTP_PROXY || process.env.ALL_PROXY || '';
  if (proxyUrl && !/^(none|direct)$/i.test(proxyUrl)) {
    const { ProxyAgent, setGlobalDispatcher } = require('undici');
    setGlobalDispatcher(new ProxyAgent(proxyUrl));
  }
} catch (err) {
  // A configured OAuth proxy must never silently fall back to direct traffic.
  throw new Error('OAuth proxy initialization failed: ' + err.message);
}

const SENTINEL_VERSION = '20260219f9f6';
const SENTINEL_SDK_URL = `https://sentinel.openai.com/sentinel/${SENTINEL_VERSION}/sdk.js`;
const SENTINEL_REQ_URL = 'https://sentinel.openai.com/backend-api/sentinel/req';
const SENTINEL_REFERER = `https://sentinel.openai.com/backend-api/sentinel/frame.html?sv=${SENTINEL_VERSION}`;
const SDK_CACHE_FILE = path.join(process.env.SENTINEL_CACHE_DIR || path.join(os.tmpdir(), 'space-merge-sentinel'), `sentinel-${SENTINEL_VERSION}.js`);

const USER_AGENT = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36';

let sdkSourceCache = '';

const SDK_GLOBAL_PATCH = 'var SentinelSDK=';
const SDK_GLOBAL_REPLACEMENT = 'globalThis.SentinelSDK=';
const INSTANCE_PATCH = 'var P=new _;';
const INSTANCE_REPLACEMENT = 'var P=new _;globalThis.__debugP=P;';
const EXPOSE_PATCH = 'return o?r?.[n(63)]?ce({so:o,c:r[n(63)]},t):o:null},t.token=ye,t}({});';
const EXPOSE_REPLACEMENT = 'return o?r?.[n(63)]?ce({so:o,c:r[n(63)]},t):o:null},t.token=ye,t.__debug_n=_n,t.__debug_bindProof=D,t}({});';

async function getSentinelToken({ deviceId, flow = 'authorize_continue' }) {
  try {
    const did = deviceId || crypto.randomUUID();
    const sdkSource = await getSdkSource();

    const requirements = await runSdkAction(sdkSource, {
      action: 'requirements',
      device_id: did,
      user_agent: USER_AGENT,
    });
    const requestP = String(requirements.request_p || '').trim();
    if (!requestP) return '';

    const challenge = await fetchChallenge({
      deviceId: did,
      flow,
      requestP,
    });
    const cValue = String(challenge.token || '').trim();
    if (!cValue) return '';

    const solved = await runSdkAction(sdkSource, {
      action: 'solve',
      device_id: did,
      user_agent: USER_AGENT,
      request_p: requestP,
      challenge,
    });
    const finalP = String(solved.final_p || solved.p || '').trim();
    const tValue = solved.t == null ? '' : String(solved.t).trim();
    if (!finalP || !tValue) return '';

    return JSON.stringify({
      p: finalP,
      t: tValue,
      c: cValue,
      id: did,
      flow,
    });
  } catch (err) {
    console.warn('[Sentinel] token 生成失败:', err.message);
    return '';
  }
}

async function getSdkSource() {
  if (sdkSourceCache) return sdkSourceCache;
  try {
    if (fs.existsSync(SDK_CACHE_FILE)) {
      sdkSourceCache = fs.readFileSync(SDK_CACHE_FILE, 'utf8');
      if (sdkSourceCache) return sdkSourceCache;
    }
  } catch {
    // cache is best effort
  }
  const resp = await fetch(SENTINEL_SDK_URL, {
    headers: {
      'Accept': '*/*',
      'Referer': 'https://auth.openai.com/',
      'User-Agent': USER_AGENT,
    },
  });
  if (!resp.ok) throw new Error(`下载 Sentinel SDK 失败: HTTP ${resp.status}`);
  sdkSourceCache = await resp.text();
  try {
    fs.mkdirSync(path.dirname(SDK_CACHE_FILE), { recursive: true });
    fs.writeFileSync(SDK_CACHE_FILE, sdkSourceCache);
  } catch {
    // cache is best effort
  }
  return sdkSourceCache;
}

async function fetchChallenge({ deviceId, flow, requestP }) {
  const body = JSON.stringify({
    p: requestP,
    id: deviceId,
    flow,
  });

  const resp = await fetch(SENTINEL_REQ_URL, {
    method: 'POST',
    headers: {
      'Accept': '*/*',
      'Accept-Language': 'en-US,en;q=0.9',
      'Content-Type': 'text/plain;charset=UTF-8',
      'Origin': 'https://sentinel.openai.com',
      'Referer': SENTINEL_REFERER,
      'Sec-Fetch-Dest': 'empty',
      'Sec-Fetch-Mode': 'cors',
      'Sec-Fetch-Site': 'same-origin',
      'User-Agent': USER_AGENT,
    },
    body,
  });

  if (!resp.ok) throw new Error(`/sentinel/req HTTP ${resp.status}`);
  return resp.json();
}

async function runSdkAction(sdkSource, payload) {
  const patchedSdk = patchSdk(sdkSource);
  const sandbox = buildSandbox(payload, patchedSdk);
  const context = vm.createContext(sandbox);
  const script = new vm.Script(`${runtimeSource()}\nrunSentinelAction();`);
  const result = await script.runInContext(context, { timeout: 30000 });
  if (!result || typeof result !== 'object') {
    throw new Error('Sentinel SDK 返回空结果');
  }
  return result;
}

function patchSdk(source) {
  let sdk = String(source || '');
  sdk = sdk.replace(SDK_GLOBAL_PATCH, SDK_GLOBAL_REPLACEMENT);
  sdk = sdk.replace(INSTANCE_PATCH, INSTANCE_REPLACEMENT);
  sdk = sdk.replace(EXPOSE_PATCH, EXPOSE_REPLACEMENT);
  if (!sdk.includes('globalThis.__debugP')) {
    throw new Error('Sentinel SDK patch 失败: __debugP 未注入');
  }
  if (!sdk.includes('__debug_bindProof')) {
    throw new Error('Sentinel SDK patch 失败: bindProof 未注入');
  }
  return sdk;
}

function buildSandbox(payload, sdkSource) {
  return {
    __payload: payload,
    __sdkSource: sdkSource,
    console,
    Math,
    Date,
    JSON,
    Promise,
    Map,
    WeakMap,
    Set,
    Array,
    Object,
    String,
    Number,
    Boolean,
    RegExp,
    Error,
    TypeError,
    Uint8Array,
    ArrayBuffer,
    TextEncoder,
    TextDecoder,
    URL,
    URLSearchParams,
    Buffer,
    setTimeout: (cb) => {
      if (typeof cb === 'function') cb();
      return 1;
    },
    clearTimeout: () => {},
    setInterval: () => 1,
    clearInterval: () => {},
  };
}

function runtimeSource() {
  return String.raw`
function createStorage() {
  const map = new Map();
  return {
    get length() { return map.size; },
    clear() { map.clear(); },
    getItem(key) { return map.has(String(key)) ? map.get(String(key)) : null; },
    setItem(key, value) { map.set(String(key), String(value)); },
    removeItem(key) { map.delete(String(key)); },
  };
}

function createElement(tagName) {
  const tag = String(tagName || 'div').toLowerCase();
  return {
    nodeType: 1,
    tagName: tag.toUpperCase(),
    nodeName: tag.toUpperCase(),
    style: {},
    children: [],
    src: '',
    contentWindow: { postMessage() {} },
    appendChild(child) { this.children.push(child); return child; },
    removeChild(child) { this.children = this.children.filter((x) => x !== child); return child; },
    setAttribute() {},
    getAttribute() { return null; },
    addEventListener(event, cb) { if (event === 'load' && typeof cb === 'function') cb(); },
    removeEventListener() {},
    getBoundingClientRect() {
      return { x: 0, y: 0, width: 0, height: 0, top: 0, left: 0, right: 0, bottom: 0 };
    },
  };
}

function installRuntime(payload) {
  const screen = {
    width: Number(payload.screen_width || 1366),
    height: Number(payload.screen_height || 768),
    availWidth: Number(payload.screen_width || 1366),
    availHeight: Number(payload.screen_height || 768),
    colorDepth: 24,
    pixelDepth: 24,
  };
  const scripts = [];
  const documentElement = createElement('html');
  documentElement.clientWidth = screen.width;
  documentElement.clientHeight = screen.height;

  const document = {
    readyState: 'complete',
    hidden: false,
    visibilityState: 'visible',
    referrer: 'https://auth.openai.com/',
    URL: 'https://auth.openai.com/',
    cookie: 'oai-did=' + encodeURIComponent(payload.device_id || ''),
    scripts,
    currentScript: { src: 'https://sentinel.openai.com/sentinel/sdk.js', getAttribute() { return null; } },
    documentElement,
    body: createElement('body'),
    head: createElement('head'),
    createElement(tag) {
      const el = createElement(tag);
      if (String(tag).toLowerCase() === 'script') scripts.push(el);
      return el;
    },
    createElementNS(_ns, tag) { return this.createElement(tag); },
    querySelector() { return null; },
    querySelectorAll() { return []; },
    getElementById() { return null; },
    getElementsByTagName() { return []; },
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() { return true; },
  };

  const performance = {
    now: () => Number(payload.performance_now || 12345.67),
    timeOrigin: Number(payload.time_origin || Date.now() - 12345),
    memory: { jsHeapSizeLimit: Number(payload.js_heap_size_limit || 4294967296) },
  };

  globalThis.window = globalThis;
  globalThis.self = globalThis;
  globalThis.top = globalThis;
  globalThis.parent = globalThis;
  globalThis.document = document;
  globalThis.navigator = {
    userAgent: String(payload.user_agent || 'Mozilla/5.0'),
    language: 'en-US',
    languages: ['en-US', 'en'],
    hardwareConcurrency: 12,
    platform: 'Win32',
    vendor: 'Google Inc.',
    webdriver: false,
  };
  globalThis.location = {
    href: 'https://auth.openai.com/',
    origin: 'https://auth.openai.com',
    pathname: '/',
    search: '',
  };
  globalThis.screen = screen;
  globalThis.performance = performance;
  globalThis.localStorage = createStorage();
  globalThis.sessionStorage = createStorage();
  globalThis.__sentinel_init_pending = [];
  globalThis.__sentinel_token_pending = [];
  globalThis.requestIdleCallback = (cb) => {
    if (typeof cb === 'function') cb({ didTimeout: false, timeRemaining: () => 50 });
    return 1;
  };
  globalThis.cancelIdleCallback = () => {};
  globalThis.addEventListener = () => {};
  globalThis.removeEventListener = () => {};
  globalThis.dispatchEvent = () => true;
  globalThis.postMessage = () => {};
  globalThis.atob = (input) => Buffer.from(String(input || ''), 'base64').toString('binary');
  globalThis.btoa = (input) => Buffer.from(String(input || ''), 'binary').toString('base64');
  globalThis.URL = URL;
  globalThis.URLSearchParams = URLSearchParams;
  globalThis.Event = class Event { constructor(type) { this.type = type; } };
  globalThis.CustomEvent = class CustomEvent extends globalThis.Event {
    constructor(type, init) { super(type); this.detail = init && Object.prototype.hasOwnProperty.call(init, 'detail') ? init.detail : null; }
  };
  globalThis.MessageChannel = class MessageChannel {
    constructor() {
      this.port1 = { postMessage() {}, addEventListener() {}, removeEventListener() {}, start() {}, close() {} };
      this.port2 = { postMessage() {}, addEventListener() {}, removeEventListener() {}, start() {}, close() {} };
    }
  };
  globalThis.matchMedia = (query) => ({
    media: String(query || ''),
    matches: false,
    onchange: null,
    addListener() {},
    removeListener() {},
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() { return false; },
  });
  globalThis.getComputedStyle = () => ({ getPropertyValue() { return ''; } });
  globalThis.history = { length: 1, state: null, back() {}, forward() {}, go() {}, pushState() {}, replaceState() {} };
  globalThis.chrome = { runtime: {}, app: {} };
  globalThis.CSS = { supports() { return true; } };
  globalThis.indexedDB = {
    open() { return { onerror: null, onsuccess: null, onupgradeneeded: null, result: {}, error: null }; },
    deleteDatabase() { return {}; },
  };
  globalThis.fetch = async () => { throw new Error('fetch should not be called inside Sentinel VM'); };
  globalThis.crypto = {
    getRandomValues(arr) {
      for (let i = 0; i < arr.length; i += 1) arr[i] = Math.floor(Math.random() * 256);
      return arr;
    },
  };
}

async function runSentinelAction() {
  const payload = globalThis.__payload || {};
  installRuntime(payload);
  eval(String(globalThis.__sdkSource || ''));

  if (payload.action === 'requirements') {
    const requestP = await globalThis.__debugP.getRequirementsToken();
    return { request_p: requestP };
  }

  if (payload.action === 'solve') {
    const challenge = payload.challenge || {};
    const requestP = String(payload.request_p || '').trim();
    const finalP = await globalThis.__debugP.getEnforcementToken(challenge);
    globalThis.SentinelSDK.__debug_bindProof(challenge, requestP);
    const dx = challenge && challenge.turnstile ? challenge.turnstile.dx : null;
    const tValue = dx ? await globalThis.SentinelSDK.__debug_n(challenge, dx) : null;
    return { final_p: finalP, t: tValue };
  }

  throw new Error('unsupported Sentinel action: ' + payload.action);
}
`;
}

async function readStdin() {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  return Buffer.concat(chunks).toString('utf8');
}

async function main() {
  const raw = await readStdin();
  const payload = raw.trim() ? JSON.parse(raw) : {};
  const token = await getSentinelToken({
    deviceId: payload.deviceId || payload.device_id || '',
    flow: payload.flow || 'authorize_continue',
  });
  process.stdout.write(JSON.stringify({ token }));
}

if (require.main === module) {
  main().catch((err) => {
    process.stderr.write(String(err && err.stack ? err.stack : err) + '\n');
    process.exit(1);
  });
}

module.exports = { getSentinelToken };
