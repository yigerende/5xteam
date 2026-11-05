import { createApp } from 'vue';
import MailManagementView from '../src/components/MailManagementView.vue';
import '../src/styles.css';

// All fetches are handled in memory. This fixture never reaches a real API.
const storage = 'gptregister.ui.fixture';
const fixture = window.gptRegisterFixture = {
  requests: [], loseResponse: false,
  settings: { url: 'https://fixture.invalid', key_present: true, enter_pro: false },
  jobs: JSON.parse(localStorage.getItem(storage) || '[]'),
};
const json = data => new Response(JSON.stringify({ ok: true, data }), { headers: { 'Content-Type': 'application/json' } });
window.fetch = async (path, options = {}) => {
  const url = new URL(path, location.origin);
  const body = options.body ? JSON.parse(options.body) : {};
  fixture.requests.push({ path: url.pathname, method: options.method || 'GET', body });
  const root = '/api/mail/gptregister';
  if (url.pathname === `${root}/settings`) {
    if (options.method === 'PUT') Object.assign(fixture.settings, { url: body.url, key_present: true, enter_pro: body.enter_pro });
    return json(fixture.settings);
  }
  if (url.pathname === `${root}/test` || url.pathname === `${root}/catalog`) return json({ settings: fixture.settings, user: { email: 'demo@example.com', points: 1000, frozen: 200 }, catalog: { orders_open: true, max_quantity: 50, products: [{ need_oauth: false, unit_price: 7 }, { need_oauth: true, unit_price: 15 }] } });
  if (url.pathname === `${root}/jobs`) {
    if (options.method === 'POST') {
      let job = fixture.jobs.find(j => j.intent === body.request_key);
      if (!job) {
        job = { id: `fixture-${fixture.jobs.length + 1}`, intent: body.request_key, quantity: body.quantity, need_oauth: body.need_oauth, enter_pro: fixture.settings.enter_pro, platform_url: fixture.settings.url, state: 'syncing', remote_status: 'running', delivered: 0, imported: 0, skipped: 0, unit_price: body.need_oauth ? 15 : 7, charged: 0, released: 0, created_at: new Date().toISOString(), message: '等待交付' };
        fixture.jobs.push(job);
        localStorage.setItem(storage, JSON.stringify(fixture.jobs));
      }
      if (fixture.loseResponse) throw new TypeError('Simulated lost response');
      return json(job);
    }
    return json(fixture.jobs);
  }
  if (url.pathname.endsWith('/receipts')) return json([{ account_id: 'fixture-delivery', email: 'registered@example.com', result: 'imported', reason: '已直接进入 Pro 管理' }]);
  if (url.pathname === '/api/mail/status') return json({ available: true });
  if (url.pathname === '/api/mail/accounts') return json({ items: [], total: 0, pipelines: [], counts: { all: 0 }, space_counts: {} });
  if (url.pathname === '/api/mail/messages') return json({ items: [], total: 0 });
  return json({});
};
createApp(MailManagementView).mount('#app');
