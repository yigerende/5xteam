(async () => {
  const fixture = window.gptPayFixture
  if (!fixture) throw new Error('Requires isolated GPTPay preview')
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const assert = (ok, text) => { if (!ok) throw new Error(text) }
  const wait = async fn => { for (let i = 0; i < 400; i++) { if (fn()) return; await pause(25) } throw new Error('Catalog UI wait timed out') }
  const button = text => [...document.querySelectorAll('button')].find(b => b.textContent.trim() === text)
  const click = async text => { await wait(() => button(text) && !button(text).disabled); button(text).click(); await pause(80) }
  const field = text => text === '支付国家' ? document.getElementById('pro-payment-country') : [...document.querySelectorAll('label.field')].find(l => l.querySelector('span')?.textContent.trim() === text)?.querySelector('input,select,textarea')
  const fill = async (text, value) => { const el = field(text); assert(el, 'Missing field: ' + text); el.value = value; el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true })); await pause(80) }
  const reads = () => fixture.requests.filter(r => r.path === '/api/gptpay/catalog')
  await click('Pro 全自动配置'); await wait(() => field('开通供应商') && !field('开通供应商').disabled)
  assert(reads().length === 0, 'Tokenseek unnecessarily queried CMSNav')
  await fill('开通供应商', 'cmsnav'); await wait(() => field('支付国家') && !field('支付国家').disabled)
  assert(field('支付国家').tagName === 'SELECT' && field('支付国家').options.length === 3, 'Country catalog not populated')
  assert(field('支付国家').textContent.includes('菲律宾 · PH · PHP'), 'Country/currency labels missing')
  assert(field('开通套餐').textContent.includes('Pro 50x · 120 Credits'), 'Catalog price missing')
  assert(reads().length === 1 && !reads()[0].query.includes('api_key'), 'Unexpected initial catalog requests')
  await fill('支付国家', 'PH'); await fill('开通套餐', 'pro50'); await click('保存 Pro 配置')
  await wait(() => fixture.config.country === 'PH' && fixture.config.plan_code === 'pro50')
  assert(document.body.innerText.includes('结算币种：PHP'), 'Selected currency not updated')
  fixture.catalog.productAvailability.pro50x = false
  await click('刷新国家与套餐'); await wait(() => !button('刷新国家与套餐').disabled)
  assert(field('开通套餐').querySelector('[value="pro50"]').disabled && button('保存 Pro 配置').disabled, 'Unavailable plan can be saved')
  assert(field('开通套餐').value === 'pro50' && field('支付国家').value === 'PH', 'Refreshing changed saved selections')
  fixture.catalogFailure = true
  await click('刷新国家与套餐'); await wait(() => document.body.innerText.includes('目录读取失败'))
  assert(button('保存 Pro 配置').disabled && field('支付国家').disabled, 'Failed catalog did not show blocked state')
  fixture.catalogFailure = false; fixture.catalog.productAvailability.pro50x = true
  fixture.catalog.creditPrices.pro50x = 130
  await click('刷新国家与套餐'); await wait(() => !field('支付国家').disabled)
  assert(field('开通套餐').textContent.includes('130 Credits') && !button('保存 Pro 配置').disabled, 'Catalog retry failed')
  fixture.catalog.countries = fixture.catalog.countries.filter(c => c.code !== 'PH')
  await click('刷新国家与套餐'); await wait(() => !button('刷新国家与套餐').disabled)
  assert(field('支付国家').value === 'PH' && button('保存 Pro 配置').disabled, 'Missing country silently changed')
  await fill('支付国家', 'US'); await click('保存 Pro 配置')
  fixture.catalogDelay = 650
  await click('刷新国家与套餐')
  await fill('开通供应商', 'tokenseek')
  await pause(750)
  assert(!field('支付国家') && !button('保存 Pro 配置').disabled, 'Late catalog response affected old supplier')
  fixture.catalogDelay = 0
  await fill('开通供应商', 'cmsnav'); await wait(() => field('支付国家') && !field('支付国家').disabled)
  await fill('GPTPay API 地址', 'https://catalog-draft.example/api/v1')
  await wait(() => reads().some(r => new URLSearchParams(r.query).get('url') === 'https://catalog-draft.example/api/v1'))
  await wait(() => !field('支付国家').disabled)
  assert(fixture.config.url !== 'https://catalog-draft.example/api/v1', 'Catalog read saved settings implicitly')
  const count = reads().length; await pause(800)
  assert(reads().length === count, 'Unexpected catalog background polling')
  return 'Passed: countries/currencies/prices, automatic public catalog load, manual refresh, unavailable plans, save persistence, failure/retry, missing country preservation, stale response isolation, draft URL, no polling'
})()
