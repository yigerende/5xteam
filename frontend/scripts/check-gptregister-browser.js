// Evaluate only on gptregister-preview.html. Run twice with a reload between.
(async () => {
  const f = window.gptRegisterFixture;
  if (!f) throw new Error('Requires isolated GPT Register fixture');
  const pause = () => new Promise(resolve => setTimeout(resolve, 60));
  const wait = async fn => { for (let i = 0; i < 100; i++) { if (fn()) return; await pause(); } throw new Error('UI assertion timed out'); };
  const assert = (ok, text) => { if (!ok) throw new Error(text); };
  const button = text => [...document.querySelectorAll('button')].find(e => e.getClientRects().length && (e.textContent.trim() === text || (text === '邮件账号' && e.textContent.trim().startsWith(text))));
  const click = async text => { const e = button(text); assert(e && !e.disabled, `Missing enabled button: ${text}`); e.click(); await pause(); };
  const key = 'space.gptregister.pending-intent.v1';
  if (!localStorage.getItem(key)) {
    await click('GPT Register');
    await click('测试连接 / 刷新余额');
    await wait(() => document.body.innerText.includes('1000'));
    assert(document.body.innerText.includes('冻结积分') && document.body.innerText.includes('200'), 'Balance/frozen missing');
    assert(!f.requests.some(r => r.path.endsWith('/jobs') && r.method === 'POST'), 'Test must not submit order');
    document.querySelector('.register-config input[type=checkbox]').click();
    await click('保存配置');
    await click('邮件账号');
    await click('注册');
    await wait(() => !!document.querySelector('[role=dialog]'));
    const quantity = document.querySelector('[role=dialog] input[type=number]');
    quantity.value = '2'; quantity.dispatchEvent(new Event('input', { bubbles: true }));
    document.querySelector('[role=dialog] input[type=checkbox]').click();
    await pause();
    assert(document.querySelector('[role=dialog]').innerText.includes('30 积分'), 'OAuth price is not full unit price');
    f.loseResponse = true;
    await click('提交注册');
    await wait(() => !!button('确认原任务') && !button('确认原任务').disabled);
    assert(f.jobs.length === 1 && f.jobs[0].quantity === 2 && f.jobs[0].need_oauth && f.jobs[0].enter_pro, 'Submission mode/quantity/destination mismatch');
    assert(localStorage.getItem(key) && quantity.disabled, 'Lost response did not preserve intent');
    return 'Phase 1 passed: read-only test, balance/prices, Pro destination, OAuth/quantity, durable lost-response intent. Reload and rerun.';
  }
  await click('注册');
  await wait(() => !!button('确认原任务') && !button('确认原任务').disabled);
  assert(document.querySelector('[role=dialog] input[type=number]').value === '2', 'Reload lost exact quantity');
  await click('确认原任务');
  await wait(() => !document.querySelector('[role=dialog]'));
  assert(f.jobs.length === 1 && !localStorage.getItem(key), 'Replay created duplicate or failed acknowledgement');
  f.jobs[0].delivered = 1; f.jobs[0].imported = 1;
  await click('刷新记录');
  await wait(() => document.body.innerText.includes('导入 1'));
  await click('导入明细');
  await wait(() => document.querySelector('[role=dialog]')?.innerText.includes('registered@example.com'));
  assert(!document.body.innerText.includes('undefined'), 'Undefined user-facing fields');
  return 'Phase 2 passed: reload and replay uses one original order; incremental progress and import receipts render.';
})()
