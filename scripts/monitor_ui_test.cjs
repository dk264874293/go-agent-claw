'use strict';

// 离线 DOM 回归；不启动服务、不调用模型。开发环境可用 MONITOR_UI_JSDOM 指定已有 jsdom。
// 运行：MONITOR_UI_JSDOM=/本机/jsdom目录 node --test scripts/monitor_ui_test.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
let JSDOM;
try { ({ JSDOM } = require(process.env.MONITOR_UI_JSDOM || 'jsdom')); }
catch { throw new Error('离线界面测试需要 jsdom；通过 MONITOR_UI_JSDOM 指向开发环境已有的 jsdom 目录。'); }
const root = path.resolve(__dirname, '..');
const html = fs.readFileSync(path.join(root, 'internal/monitor/web/index.html'), 'utf8');
const source = fs.readFileSync(path.join(root, 'internal/monitor/web/app.js'), 'utf8');
const stamp = '2026-11-06T08:00:00Z';
const workspaces = [{ id: 'w1', path: '/测试/工作区一', health: 'ok' }, { id: 'w2', path: '/测试/工作区二', health: 'ok' }];
const clone = (value) => JSON.parse(JSON.stringify(value));
const deferred = () => { let resolve; const promise = new Promise((done) => { resolve = done; }); return { promise, resolve }; };

test('实际请求参数区分未发送、空值、零值与未知，诊断身份不创建正文入口', async (t) => {
  const metadata = makeEvent(1, 'request_metadata', { payload: { request: {
    provider: 'deepseek', protocol: 'chat_completions', model: 'fixture-model', http_status: 400,
    parameters: { temperature: { state: 'value', value: 0 }, stream: { state: 'value', value: false }, top_p: { state: 'absent' }, max_tokens: { state: 'null' }, reasoning_effort: { state: 'unknown' } },
    input_count: 0, previous_model_call_id: 'previous-call', added_inputs: 0, removed_inputs: 0, changed_inputs: 1, diff_complete: false, changed_parameters: ['temperature'], schema_changed: false,
    manifest_id: 'manifest-fixture', diagnostic_id: 'diagnostic-fixture', capture_mode: 'on_error', capture_status: 'saved', capture_complete: false, capture_redacted: true,
    error: { summary: '<script>请求错误保持文本</script>', code: { state: 'absent' }, type: { state: 'value', value: 'invalid_request_error' }, param: { state: 'null' }, request_id: { state: 'unknown' } },
  } } });
  const h = await harness(t, { route: '?view=detail&task=a&tab=timeline', events: { 'attempt-a': [metadata] } });
  const block = h.el('event-1').querySelector('.request-summary'); const text = block.textContent;
  assert.match(text, /temperature0/); assert.match(text, /streamfalse/); assert.match(text, /top_p未发送/);
  assert.match(text, /max_tokensnull（已发送）/); assert.match(text, /reasoning_effort未知/); assert.match(text, /输入项 0/);
  assert.match(text, /新增 0、移除 0、修改 1/); assert.match(text, /完整性 不完整/); assert.match(text, /已脱敏/);
  assert.match(text, /已记录输入项差异/); assert.match(text, /差异范围不完整/); assert.match(text, /变化参数 temperature/); assert.match(text, /Schema 未变化/);
  assert.match(text, /manifest-fixture/); assert.match(text, /diagnostic-fixture/); assert.equal(block.querySelectorAll('a,button,script').length, 0);
  assert.match(text, /<script>请求错误保持文本<\/script>/);
  assert.equal(h.requests.some((request) => /diagnostics|manifest-fixture|diagnostic-fixture/.test(request.url.pathname)), false);
});

test('仅元数据和成功请求未落盘正文分别显示，保留部分保存及未知语义', async (t) => {
  const reasons = ['metadata_only', 'success_body_not_saved', 'request_size_limit'];
  const events = reasons.map((capture_reason, i) => makeEvent(i + 1, 'request_metadata', { payload: { request: { capture_complete: false, capture_reason } } }));
  events.push(makeEvent(4, 'request_metadata', { payload: { request: { capture_redacted: false, capture_reason: 'metadata_only' } } }));
  const h = await harness(t, { route: '?view=detail&task=a&tab=timeline', events: { 'attempt-a': events } });
  assert.match(h.el('event-1').querySelector('.request-summary').textContent, /正文副本完整性 未保存（仅元数据）/);
  assert.doesNotMatch(h.el('event-1').querySelector('.request-summary').textContent, /正文副本完整性 不完整/);
  assert.match(h.el('event-2').querySelector('.request-summary').textContent, /正文副本完整性 成功请求正文未落盘/);
  assert.match(h.el('event-3').querySelector('.request-summary').textContent, /正文副本完整性 不完整/);
  assert.match(h.el('event-4').querySelector('.request-summary').textContent, /正文副本完整性 未知/);
});
function makeTask(id, options = {}) {
  const activity = options.activity || 'returned';
  return { id, workspace_id: 'w1', task_id: '任务-' + id, goal: '测试任务 ' + id, mode: 'readonly', kind: 'answer', activity,
    activity_groups: [activity === 'active' ? 'running' : activity], engine_status: activity === 'returned' ? 'ANSWERED' : '', record_quality: 'normal',
    last_observed_at: stamp, history_complete: true, budget: { max_model_calls: 8, max_tool_calls: 20 }, usage: { model_calls: 2, tool_calls: 3, elapsed_ms: 3500 },
    attempts: [{ id: 'attempt-' + id, activity, legacy: false, started_at: stamp, record_quality: 'normal', activities: [], model_usage: { input_tokens: 120, output_tokens: 30, cost: null } }],
    alerts: [], alert_count: 0, effects: [], plan: [], evidence: [{ id: 'evidence-' + id, label: '证据 ' + id }], traces: [], evaluations: [],
    final_text: '任务已答复；请分别核对验收。', ...options };
}
function makeEvent(sequence, event_type = 'model_finished', extra = {}) {
  return { sequence, event_type, attempt_id: 'attempt-a', recorded_at: stamp, agent_id: 'agent-a', payload: {}, ...extra };
}
async function until(check, message = '等待界面状态超时') {
  const start = Date.now();
  while (!check()) { if (Date.now() - start > 2500) throw new Error(message); await new Promise((done) => setTimeout(done, 5)); }
}
async function harness(t, options = {}) {
  const dom = new JSDOM(html, { url: 'http://localhost/' + (options.route || ''), runScripts: 'outside-only', pretendToBeVisual: true });
  t.after(() => dom.window.close());
  const window = dom.window; const requests = []; const tasks = new Map((options.tasks || [makeTask('a')]).map((task) => [task.id, task]));
  const events = new Map(Object.entries(options.events || {})); const evaluations = options.evaluations || [];
  let hook = options.hook;
  class EventSource {
    constructor() { this.listeners = {}; queueMicrotask(() => this.onopen?.()); }
    addEventListener(type, callback) { this.listeners[type] = callback; }
    emit(type) { this.listeners[type]?.({}); }
    close() { this.closed = true; }
  }
  window.EventSource = EventSource;
  window.HTMLElement.prototype.scrollIntoView = function () { this.dataset.scrolled = 'true'; };
  window.fetch = async (input, init = {}) => {
    const url = new URL(input, window.location.href); requests.push({ url, init });
    let data = hook ? await hook(url, init) : undefined;
    if (data === undefined) {
      const values = [...tasks.values()]; const selected = values.filter((task) => !url.searchParams.get('workspace') || task.workspace_id === url.searchParams.get('workspace'));
      if (url.pathname === '/api/v1/session') data = { ok: true };
      else if (url.pathname === '/api/v1/workspaces') data = { items: workspaces };
      else if (url.pathname === '/api/v1/overview') data = { total: selected.length, counts: { running: 1, approval: 0, alerts: 0, unknown: 0 }, recent: selected.slice(0, 6), attention: [], workspaces };
      else if (url.pathname === '/api/v1/tasks') data = { items: selected, total: selected.length };
      else if (url.pathname.startsWith('/api/v1/tasks/')) data = tasks.get(decodeURIComponent(url.pathname.split('/').at(-1)));
      else if (url.pathname.endsWith('/events')) {
        const id = decodeURIComponent(url.pathname.split('/').at(-2)); const after = Number(url.searchParams.get('after')) || 0;
        const all = (events.get(id) || []).filter((event) => event.sequence > after); const items = all.slice(0, 200);
        data = { items, next_after: items.at(-1)?.sequence || after, has_more: all.length > items.length };
      } else if (url.pathname.startsWith('/api/v1/evidence/')) data = { text: '证据原文 <script>保持为文本</script>', next_offset: 48, has_more: false };
      else if (url.pathname === '/api/v1/alerts') data = { items: [], total: 0, alert_total: 0 };
      else if (url.pathname === '/api/v1/evaluations') data = { items: evaluations };
      else throw new Error('未预期请求：' + url);
    }
    return { ok: data !== undefined, status: data === undefined ? 404 : 200, json: async () => clone(data || { error: '任务不存在' }) };
  };
  // 只在测试进程暴露闭包入口，生产脚本保持原样。
  window.eval(source + '\nwindow.__monitorTest = {state, refreshView, openTask, selectTab, loadEvidence};');
  const api = window.__monitorTest;
  await until(() => api.state.stream && !api.state.refreshPromise, '初始界面未完成加载：' + window.document.getElementById('notice').textContent);
  return { window, document: window.document, api, requests, tasks, events, setHook: (next) => { hook = next; },
    el: (id) => window.document.getElementById(id),
    change: (id, value) => { const el = window.document.getElementById(id); el.value = value; el.dispatchEvent(new window.Event('change', { bubbles: true })); },
    settle: () => until(() => !api.state.refreshPromise) };
}

test('启动链接移除凭据，并恢复服务端任务筛选与分页', async (t) => {
  const h = await harness(t, { route: '?view=tasks&workspace=w1&q=目标&status=FAILED&activity_group=unknown&has_alerts=true&sort=alerts&page=2#token=一次性凭据',
    hook: (url) => url.pathname === '/api/v1/tasks' ? { items: [makeTask('a')], total: 101 } : undefined });
  assert.equal(h.window.location.hash, '');
  assert.equal(h.el('workspace-filter').value, 'w1'); assert.equal(h.el('search').value, '目标');
  assert.equal(h.el('status-filter').value, 'FAILED'); assert.equal(h.el('activity-filter').value, 'unknown'); assert.equal(h.el('alerts-only').checked, true);
  const query = h.requests.find((r) => r.url.pathname === '/api/v1/tasks').url.searchParams;
  assert.equal(query.get('view'), 'summary'); assert.equal(query.get('sort'), 'alerts'); assert.equal(query.get('page'), '2');
  assert.equal(query.get('has_alerts'), 'true'); assert.match(h.el('page-summary').textContent, /2 \/ 3/);
  assert.equal(h.requests.filter((r) => r.url.pathname === '/api/v1/session').length, 1);
});

test('浏览器前进后退恢复任务详情和之前的列表筛选', async (t) => {
  const h = await harness(t, { route: '?view=tasks&q=已有筛选' });
  await h.api.openTask('a'); assert.equal(h.api.state.tab, 'delivery');
  await h.api.selectTab('evidence');
  assert.equal(new URL(h.window.location.href).searchParams.get('tab'), 'evidence');
  h.window.history.back(); await until(() => h.api.state.tab === 'delivery'); await h.settle();
  h.window.history.back(); await until(() => h.api.state.view === 'tasks'); await h.settle();
  assert.equal(h.el('search').value, '已有筛选'); assert.equal(h.el('view-tasks').hidden, false);
  h.window.history.forward(); await until(() => h.api.state.view === 'detail'); await h.settle();
  assert.equal(h.api.state.taskId, 'a'); assert.equal(h.api.state.tab, 'delivery');
});

test('快速搜索时较晚返回的旧请求不能覆盖新列表', async (t) => {
  const slow = deferred(); const h = await harness(t, { route: '?view=tasks' });
  h.setHook((url) => url.pathname === '/api/v1/tasks' && url.searchParams.get('q') === '旧条件' ? slow.promise : url.pathname === '/api/v1/tasks' ? { items: [makeTask('new', { goal: '新条件结果' })], total: 1 } : undefined);
  const submit = (q) => { h.el('search').value = q; h.el('filters').dispatchEvent(new h.window.Event('submit', { bubbles: true, cancelable: true })); };
  submit('旧条件'); await until(() => h.requests.some((r) => r.url.searchParams.get('q') === '旧条件'));
  submit('新条件'); await until(() => h.el('task-list').textContent.includes('新条件结果'));
  slow.resolve({ items: [makeTask('old', { goal: '不该出现的旧结果' })], total: 1 }); await new Promise((done) => setTimeout(done, 20));
  assert.match(h.el('task-list').textContent, /新条件结果/); assert.doesNotMatch(h.el('task-list').textContent, /不该出现/);
  assert.equal(new URL(h.window.location.href).searchParams.get('q'), '新条件');
});

test('切换任务后丢弃迟到的任务详情响应', async (t) => {
  const slow = deferred(); const h = await harness(t, { tasks: [makeTask('a'), makeTask('b', { goal: '当前任务乙' })] });
  h.setHook((url) => url.pathname === '/api/v1/tasks/a' ? slow.promise : undefined);
  const first = h.api.openTask('a'); await until(() => h.requests.some((r) => r.url.pathname === '/api/v1/tasks/a'));
  await h.api.openTask('b'); slow.resolve(makeTask('a', { goal: '迟到任务甲' })); await first;
  assert.match(h.el('task-detail').textContent, /当前任务乙/); assert.doesNotMatch(h.el('task-detail').textContent, /迟到任务甲/);
  assert.equal(new URL(h.window.location.href).searchParams.get('task'), 'b');
});

test('实时刷新合并并发读取，保留展开事件、焦点及已有节点', async (t) => {
  const h = await harness(t, { route: '?view=detail&task=a&tab=timeline', tasks: [makeTask('a', { activity: 'active' })], events: { 'attempt-a': [makeEvent(1), makeEvent(2)] } });
  const row = h.el('event-1'); const details = row.querySelector('details'); details.open = true;
  h.el('event-detail-1').focus(); await new Promise((done) => setTimeout(done, 5));
  const taskDelay = deferred(); const before = h.requests.filter((r) => r.url.pathname === '/api/v1/tasks/a').length;
  let delayed = false;
  h.setHook((url) => { if (url.pathname === '/api/v1/tasks/a' && !delayed) { delayed = true; return taskDelay.promise; } });
  const one = h.api.refreshView(); const two = h.api.refreshView(); const three = h.api.refreshView();
  assert.equal(h.requests.filter((r) => r.url.pathname === '/api/v1/tasks/a').length, before + 1);
  h.events.get('attempt-a').push(makeEvent(3)); taskDelay.resolve(makeTask('a', { activity: 'active' }));
  await Promise.all([one, two, three]); await h.settle();
  assert.equal(h.requests.filter((r) => r.url.pathname === '/api/v1/tasks/a').length, before + 2);
  assert.equal(h.el('event-1'), row); assert.equal(row.querySelector('details'), details); assert.equal(details.open, true);
  assert.equal(h.document.activeElement, h.el('event-detail-1')); assert.equal(h.el('event-3').dataset.scrolled, undefined);
  assert.equal(h.el('show-new-events').hidden, false);
});

test('证据并发读取去重，刷新保留文本节点与阅读选区', async (t) => {
  const h = await harness(t, { route: '?view=detail&task=a&tab=evidence' }); const data = deferred();
  h.setHook((url) => url.pathname === '/api/v1/evidence/evidence-a' ? data.promise : undefined);
  const one = h.api.loadEvidence('evidence-a', false); const two = h.api.loadEvidence('evidence-a', false);
  assert.equal(h.requests.filter((r) => r.url.pathname === '/api/v1/evidence/evidence-a').length, 1);
  data.resolve({ text: '一段需要持续阅读的证据', next_offset: 100, has_more: false }); await Promise.all([one, two]);
  const pre = h.el('evidence-evidence-a').querySelector('pre'); const text = pre.firstChild;
  const selection = h.window.getSelection(); const range = h.document.createRange(); range.setStart(text, 2); range.setEnd(text, 6); selection.removeAllRanges(); selection.addRange(range);
  await h.api.refreshView();
  assert.equal(h.el('evidence-evidence-a').querySelector('pre'), pre); assert.equal(pre.firstChild, text); assert.equal(selection.toString(), '需要持续');
});

test('离开后重开同一任务时，不接收上次访问的迟到证据', async (t) => {
  const h = await harness(t, { route: '?view=detail&task=a&tab=evidence' }); const data = deferred();
  h.setHook((url) => url.pathname === '/api/v1/evidence/evidence-a' ? data.promise : undefined);
  const read = h.api.loadEvidence('evidence-a', false); await h.api.openTask('a'); await h.api.selectTab('evidence');
  data.resolve({ text: '上次访问的迟到原文', next_offset: 100, has_more: false }); await read;
  assert.doesNotMatch(h.el('task-detail').textContent, /上次访问的迟到原文/); assert.equal(h.api.state.evidence.size, 0);
});

test('时间线保留分页位置，关键事件绕过筛选且原始记录仍可读', async (t) => {
  const stream = Array.from({ length: 220 }, (_, i) => makeEvent(i + 1, i % 2 ? 'model_finished' : 'heartbeat'));
  stream[200] = makeEvent(201, 'heartbeat', { agent_id: '其他Agent', payload: { stop_code: 'model_limit', error_class: 'budget' } });
  const h = await harness(t, { route: '?view=detail&task=a&tab=timeline', events: { 'attempt-a': stream } });
  assert.match(h.el('event-count').textContent, /已加载 200 条/); assert.match(h.el('event-count').textContent, /仍有后续/);
  assert.equal(h.el('event-1'), null); assert.ok(h.el('event-2'));
  const before = h.requests.filter((r) => r.url.pathname.endsWith('/events')).length; await h.api.refreshView();
  assert.equal(h.requests.filter((r) => r.url.pathname.endsWith('/events')).length, before);
  h.el('load-more-events').click(); await until(() => h.api.state.events.length === 220);
  h.change('event-category', 'tool'); assert.ok(h.el('event-201')); assert.equal(h.el('event-2'), null);
  assert.match(h.el('event-201').querySelector('pre').textContent, /model_limit/);
  assert.match(h.el('event-count').textContent, /已加载 220 条 · 当前显示 1 条/);
  const query = h.requests.filter((r) => r.url.pathname.endsWith('/events')).at(-1).url.searchParams;
  assert.equal(query.get('after'), '200'); assert.equal(query.get('limit'), '200');
});

test('异常深链接加载目标所在分页，临时呈现被过滤的目标事件', async (t) => {
  const stream = Array.from({ length: 210 }, (_, i) => makeEvent(i + 1, 'heartbeat'));
  const h = await harness(t, { route: '?view=detail&task=a&attempt=attempt-a&tab=timeline&event=205&event_category=tool', events: { 'attempt-a': stream } });
  assert.ok(h.el('event-205')); assert.equal(h.el('event-205').classList.contains('targeted'), true); assert.equal(h.el('event-205').dataset.scrolled, 'true');
  assert.equal(h.el('event-category').value, 'tool'); assert.equal(h.requests.filter((r) => r.url.pathname.endsWith('/events')).length, 2);
});

test('详情默认标签按当前尝试选择，刷新不会在任务返回时切页', async (t) => {
  const h = await harness(t, { tasks: [makeTask('a', { activity: 'active' }), makeTask('b')] });
  await h.api.openTask('a'); assert.equal(h.api.state.tab, 'timeline');
  h.tasks.set('a', makeTask('a')); await h.api.refreshView(); assert.equal(h.api.state.tab, 'timeline');
  await h.api.openTask('b'); assert.equal(h.api.state.tab, 'delivery');
  await h.api.selectTab('timeline'); await h.api.refreshView(); assert.equal(h.api.state.tab, 'timeline');
});

test('全站工作区筛选约束异常和评测，跨工作区详情返回总览', async (t) => {
  const h = await harness(t, { route: '?view=detail&task=a&tab=delivery' });
  h.change('workspace-filter', 'w2'); await until(() => h.api.state.view === 'overview'); await h.settle();
  assert.equal(h.api.state.taskId, ''); assert.equal(new URL(h.window.location.href).searchParams.get('workspace'), 'w2');
  h.document.querySelector('[data-view="alerts"]').click(); await until(() => h.api.state.view === 'alerts'); await h.settle();
  assert.equal(h.requests.findLast((r) => r.url.pathname === '/api/v1/alerts').url.searchParams.get('workspace'), 'w2');
  h.document.querySelector('[data-view="evaluations"]').click(); await until(() => h.api.state.view === 'evaluations'); await h.settle();
  assert.equal(h.requests.findLast((r) => r.url.pathname === '/api/v1/evaluations').url.searchParams.get('workspace'), 'w2');
  assert.equal(h.requests.filter((r) => r.url.pathname === '/api/v1/tasks').length, 0);
});

test('任务正文中的 HTML 作为文字展示，评测筛选保持五维结果', async (t) => {
  const evaluations = [{ id: 'e1', experiment_id: '实验甲', case_id: 'case-a', arm: 'candidate', combined_pass: true, artifact_result: 'pass', scope_result: 'pass', engine_result: 'pass', environment_result: 'pass', checker_result: 'pass', labels: [], evidence: [] },
    { id: 'e2', experiment_id: '实验乙', case_id: 'case-b', arm: 'baseline', combined_pass: false, artifact_result: 'pass', scope_result: 'fail', engine_result: 'fail', environment_result: 'pass', checker_result: 'pass', labels: ['范围缺口'], evidence: [] }];
  const h = await harness(t, { tasks: [makeTask('a', { final_text: '<img src=x onerror="window.evil=1">最终答复' })], evaluations });
  await h.api.openTask('a'); assert.match(h.el('task-detail').textContent, /<img src=x/); assert.equal(h.el('task-detail').querySelector('img'), null); assert.equal(h.window.evil, undefined);
  h.document.querySelector('[data-view="evaluations"]').click(); await until(() => h.api.state.view === 'evaluations'); await h.settle();
  h.change('evaluation-experiment', '实验乙'); h.change('evaluation-arm', 'baseline'); h.change('evaluation-result', 'fail');
  assert.equal(h.el('evaluation-count').textContent, '1 / 2 个样本'); assert.match(h.el('evaluation-list').textContent, /case-b/); assert.doesNotMatch(h.el('evaluation-list').textContent, /case-a/);
  for (const label of ['产物', '范围', '引擎', '环境', '评分器']) assert.ok(h.el('evaluation-list').textContent.includes(label));
});

test('切换工作区清除旧评测筛选时，URL 与界面保持一致', async (t) => {
  const evaluations = [{ id: 'e1', experiment_id: '实验甲', arm: 'candidate', combined_pass: true, labels: [], evidence: [] }];
  const h = await harness(t, { route: '?view=evaluations&experiment=实验甲&arm=candidate', evaluations });
  assert.equal(h.el('evaluation-experiment').value, '实验甲');
  h.change('workspace-filter', 'w2'); await h.settle();
  assert.equal(h.el('evaluation-experiment').value, ''); assert.equal(h.el('evaluation-arm').value, '');
  const query = new URL(h.window.location.href).searchParams;
  assert.equal(query.has('experiment'), false); assert.equal(query.has('arm'), false);
});

test('SSE reset 后保留已加载第二页事件，重读不会退回第一页', async (t) => {
  const stream = Array.from({ length: 220 }, (_, i) => makeEvent(i + 1));
  const h = await harness(t, { route: '?view=detail&task=a&tab=timeline', events: { 'attempt-a': stream } });
  h.el('load-more-events').click(); await until(() => h.api.state.events.length === 220);
  const count = h.requests.filter((r) => r.url.pathname.endsWith('/events')).length;
  h.api.state.stream.emit('reset');
  await until(() => h.requests.filter((r) => r.url.pathname.endsWith('/events')).length > count); await h.settle();
  assert.equal(h.api.state.events.length, 220); assert.ok(h.el('event-220'));
});

test('产物列表重排后按钮仍读取当前显示产物的证据', async (t) => {
  const artifacts = [{ action_id: 'action-one', path: 'one.txt', version: 'v1', evidence_id: 'evidence-one' }, { action_id: 'action-two', path: 'two.txt', version: 'v2', evidence_id: 'evidence-two' }];
  const task = makeTask('a', { artifacts, evidence: [{ id: 'evidence-one', label: 'one.txt' }, { id: 'evidence-two', label: 'two.txt' }] });
  const h = await harness(t, { route: '?view=detail&task=a&tab=delivery', tasks: [task] });
  h.tasks.set('a', { ...task, artifacts: [...artifacts].reverse() }); await h.api.refreshView();
  const part = h.document.querySelector('section[data-key="可信产物清单"]');
  assert.match(part.querySelector('.evidence-row').textContent, /two.txt/);
  part.querySelector('.evidence-row button').click();
  await until(() => h.requests.some((r) => r.url.pathname.startsWith('/api/v1/evidence/')));
  assert.equal(h.requests.findLast((r) => r.url.pathname.startsWith('/api/v1/evidence/')).url.pathname, '/api/v1/evidence/evidence-two');
  await until(() => h.api.state.evidence.has('evidence-two') && !h.api.state.evidenceLoading.has('evidence-two'));
});

test('输入搜索后立即换页，防抖回调不会把用户带回旧页面', async (t) => {
  const h = await harness(t, { route: '?view=tasks' });
  for (const [view, input] of [['tasks', 'search'], ['alerts', 'alert-search']]) {
    h.document.querySelector('[data-view="' + view + '"]').click(); await until(() => h.api.state.view === view); await h.settle();
    h.el(input).value = '尚未提交的搜索'; h.el(input).dispatchEvent(new h.window.Event('input', { bubbles: true }));
    h.document.querySelector('[data-view="overview"]').click(); await until(() => h.api.state.view === 'overview'); await h.settle();
    await new Promise((done) => setTimeout(done, 300)); assert.equal(h.api.state.view, 'overview');
    assert.equal(new URL(h.window.location.href).searchParams.get('view'), 'overview');
  }
});

test('报告引用目标更新时，保留的同文案按钮读取新版引用', async (t) => {
  const report = { status: 'published', report_id: 'report-a', draft_artifact_id: 'artifact-old', issues: [] };
  const evidence = [{ id: 'evidence-old', label: '工具证据 artifact-old' }, { id: 'evidence-new', label: '工具证据 artifact-new' }];
  const task = makeTask('a', { report, evidence });
  const h = await harness(t, { route: '?view=detail&task=a&tab=delivery', tasks: [task] });
  const before = h.document.querySelector('section[data-key="证据报告"] button');
  h.tasks.set('a', { ...task, report: { ...report, draft_artifact_id: 'artifact-new' } }); await h.api.refreshView();
  const after = h.document.querySelector('section[data-key="证据报告"] button'); assert.equal(after, before);
  after.click(); await until(() => h.requests.some((r) => r.url.pathname.startsWith('/api/v1/evidence/')));
  assert.equal(h.requests.findLast((r) => r.url.pathname.startsWith('/api/v1/evidence/')).url.pathname, '/api/v1/evidence/evidence-new');
  await until(() => h.api.state.evidence.has('evidence-new') && !h.api.state.evidenceLoading.has('evidence-new'));
});

test('评测证据在刷新后继续按最新游标分页，引用替换后读取新证据', async (t) => {
  const evaluations = [{ id: 'e1', experiment_id: '实验甲', case_id: 'case-a', arm: 'candidate', combined_pass: true, labels: [], evidence: [{ id: 'sample-one', label: 'answer.txt' }] }];
  const h = await harness(t, { route: '?view=evaluations', evaluations });
  h.setHook((url) => url.pathname === '/api/v1/evidence/sample-one' ? (url.searchParams.get('offset') === '0' ? { text: '第一段', next_offset: 9, has_more: true } : { text: '第二段', next_offset: 18, has_more: false }) : undefined);
  const details = h.document.querySelector('details[data-key="evaluation-e1"]'); details.open = true;
  details.querySelector('button').click(); await until(() => details.textContent.includes('第一段'));
  await h.api.refreshView(); assert.equal(h.document.querySelector('details[data-key="evaluation-e1"]'), details); assert.equal(details.open, true);
  [...details.querySelectorAll('button')].find((b) => b.textContent === '读取下一段').click();
  await until(() => details.textContent.includes('第一段第二段'));
  assert.equal(h.requests.findLast((r) => r.url.pathname === '/api/v1/evidence/sample-one').url.searchParams.get('offset'), '9');
  evaluations[0].evidence = [{ id: 'sample-two', label: '新版 answer.txt' }]; await h.api.refreshView();
  assert.match(details.textContent, /新版 answer.txt/); assert.doesNotMatch(details.textContent, /第一段第二段/);
  details.querySelector('button').click(); await until(() => h.requests.some((r) => r.url.pathname === '/api/v1/evidence/sample-two'));
  assert.equal(h.requests.findLast((r) => r.url.pathname.startsWith('/api/v1/evidence/')).url.pathname, '/api/v1/evidence/sample-two');
  await until(() => h.api.state.evaluationEvidence.has('sample-two') && !h.api.state.evaluationLoading.has('sample-two'));
});

test('尝试列表头部新增运行时，聚焦的选择框仍选中正在阅读的旧尝试', async (t) => {
  const task = makeTask('a', { activity: 'active', attempts: [{ id: 'attempt-old', activity: 'active', legacy: false, started_at: stamp, record_quality: 'normal', activities: [], model_usage: {} }, { id: 'attempt-older', activity: 'returned', legacy: false, started_at: '2026-11-06T07:59:00Z', record_quality: 'normal', activities: [], model_usage: {} }] });
  const h = await harness(t, { route: '?view=detail&task=a&attempt=attempt-old&tab=timeline', tasks: [task] });
  const select = h.el('attempt-select'); select.focus(); assert.equal(select.value, 'attempt-old');
  const newer = { id: 'attempt-new', activity: 'active', legacy: false, started_at: '2026-11-06T08:01:00Z', record_quality: 'normal', activities: [], model_usage: {} };
  h.tasks.set('a', { ...task, attempts: [newer, ...task.attempts] }); await h.api.refreshView();
  assert.equal(h.el('attempt-select'), select); assert.equal(h.document.activeElement, select);
  assert.equal(h.api.state.attemptId, 'attempt-old'); assert.equal(select.value, 'attempt-old');
});
