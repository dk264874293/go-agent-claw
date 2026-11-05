'use strict';

// 页面只读取服务端投影，所有任务内容均以文本节点显示。
const $ = (id) => document.getElementById(id);
const state = {
  view: 'tasks', page: 1, pageSize: 50, total: 0, tasks: [], workspaces: [],
  task: null, taskId: '', attemptId: '', tab: 'timeline', events: [], nextAfter: 0,
  hasMore: false, evaluations: [], alertTasks: [], evidence: new Map(),
  expandedEvents: new Set(), stream: null, authenticated: false, loading: 0,
  expandedDetails: new Set(), evaluationEvidence: new Map(),
  eventRequest: 0, taskRequest: 0, listRequest: 0, targetSequence: 0,
};

const statusLabels = {
  ANSWERED: '已答复', VERIFIED: '验收通过', NEEDS_VERIFICATION: '待验收',
  BUDGET_EXHAUSTED: '预算耗尽', BLOCKED: '已阻塞', CANCELLED: '已取消', FAILED: '失败',
  SUCCEEDED: '成功', REJECTED: '未获准执行', CONFLICT: '版本冲突',
  CONFIRMED: '效果已确认', UNKNOWN: '效果未知', NONE: '确认无效果',
  pass: '通过', fail: '未通过', error: '错误', not_run: '未运行',
};
const activityLabels = {
  model: '请求模型中', model_running: '请求模型中', modeling: '请求模型中',
  tool: '执行工具中', tool_running: '执行工具中', approval: '等待审批', approval_waiting: '等待审批',
  queued: '排队中', agent: '子任务执行中', running: '执行中', finalizing: '收尾中',
  finalized: '已收尾，返回待确认', returned: '本次调用已返回', unknown: '活动未知',
  active: '有活动记录', legacy: '历史记录', recorded: '结果已记录', result_recorded: '引擎结果已记录',
};
const qualityLabels = {
  normal: '记录正常', complete: '记录完整', healthy: '记录正常', ok: '记录正常',
  incomplete: '历史不完整', partial: '历史不完整', gap: '观测缺口', corrupt: '记录损坏',
  invalid: '记录异常', error: '记录异常', unknown: '可靠性未知', legacy: '历史不完整',
  unconfirmed: '结束未确认', missing_end: '结束未确认', ended_unknown: '结束未确认',
  unconfirmed_end: '结束未确认', historical_incomplete: '历史不完整',
};
const eventLabels = {
  run_attempt_started: '开始运行尝试', run_finalized: '引擎收尾完成', run_returned: '调用已返回',
  run_configured: '运行参数已记录', artifact_saved: '工具证据已保存', recorder_closed: '观测记录已关闭',
  task_event_committed: '任务事件已提交', model_started: '模型请求开始', model_finished: '模型请求结束',
  model_usage: '模型用量回执', tool_attempted: '工具调用尝试', tool_attempt: '工具调用尝试',
  tool_admission: '工具准入结果', tool_handler_started: '工具开始执行', tool_started: '工具开始执行',
  tool_finished: '工具调用结束', receipt_reused: '复用历史回执', tool_receipt_reused: '复用历史回执',
  approval_waiting: '等待审批', approval_resolved: '审批已结束',
  agent_queued: '子任务排队', agent_started: '子任务开始', agent_finished: '子任务结束',
  subtask_started: '子任务开始', subtask_finished: '子任务结束', delivery_attempted: '终报发送尝试',
  heartbeat: '运行活动信号', gap: '观测记录缺口', observation_gap: '观测记录缺口',
  started: '任务执行开始', finished: '引擎结果已记录', action_started: '动作已登记',
  action_result: '动作结果', reconciled: '恢复核实', model: '模型预算准入', tool: '工具预算准入',
  turn: '开始一轮执行', elapsed: '累计计时', plan: '建立计划', plan_patch: '更新计划', step_done: '步骤完成',
};

function node(tag, className, text) {
  const item = document.createElement(tag);
  if (className) item.className = className;
  if (text !== undefined && text !== null) item.textContent = String(text);
  return item;
}
function append(parent, ...items) { items.flat().filter(Boolean).forEach((item) => parent.append(item)); return parent; }
function button(text, action, className = 'button secondary') {
  const item = node('button', className, text); item.type = 'button';
  item.addEventListener('click', (event) => { try { Promise.resolve(action(event)).catch((error) => notice(error.message)); } catch (error) { notice(error.message); } });
  return item;
}
function known(value) { return value !== null && value !== undefined && value !== ''; }
function value(value, fallback = '未知') { return known(value) ? String(value) : fallback; }
function array(value) { return Array.isArray(value) ? value : []; }
function short(id) { return id ? String(id).slice(0, 10) : '未知'; }
function number(v) { return known(v) && Number.isFinite(Number(v)) ? Number(v).toLocaleString('zh-CN') : '未知'; }
function time(v, compact = false) {
  if (!v) return '时间未知';
  const d = new Date(v); if (!Number.isFinite(d.getTime())) return '时间未知';
  return d.toLocaleString('zh-CN', compact ? { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false } : { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
}
function duration(ms) {
  if (!known(ms)) return '未知';
  const seconds = Number(ms) / 1000;
  return seconds < 60 ? seconds.toFixed(seconds < 10 ? 1 : 0) + ' 秒' : Math.floor(seconds / 60) + ' 分 ' + Math.round(seconds % 60) + ' 秒';
}
function tone(code) {
  if (['VERIFIED', 'ANSWERED', 'SUCCEEDED', 'CONFIRMED', 'pass', 'normal', 'complete', 'healthy', 'ok', 'returned'].includes(code)) return 'good';
  if (['FAILED', 'fail', 'error', 'corrupt', 'invalid'].includes(code)) return 'bad';
  if (['NEEDS_VERIFICATION', 'BUDGET_EXHAUSTED', 'BLOCKED', 'UNKNOWN', 'incomplete', 'partial', 'gap', 'unconfirmed', 'missing_end', 'legacy', 'unknown', 'unconfirmed_end', 'historical_incomplete'].includes(code)) return 'warn';
  return '';
}
function badge(code, labels = statusLabels, fallback = '未知') { return node('span', 'badge ' + tone(code), labels[code] || value(code, fallback)); }
function empty(title, description) { return append(node('div', 'empty'), node('strong', '', title), node('div', '', description)); }
function notice(message) { $('notice').textContent = message || ''; $('notice').hidden = !message; }
function connection(text, kind) { $('connection-text').textContent = text; $('connection-dot').className = 'connection-dot ' + (kind || ''); }
function workspaceName(id) { const w = state.workspaces.find((w) => w.id === id); return w ? w.path : value(id, '工作区未知'); }
function summary(text, limit = 110) { const clean = String(text || '').replace(/\s+/g, ' ').trim(); return clean.length > limit ? clean.slice(0, limit) + '…' : clean; }
function workspaceLabel(id) { const path = workspaceName(id); return path.length > 65 ? '…/' + path.split('/').filter(Boolean).slice(-3).join('/') : path; }
function restoreFocus(id) { if (id) $(id)?.focus({ preventScroll: true }); }
function disclosure(key) {
  const details = node('details'); details.open = state.expandedDetails.has(key);
  details.addEventListener('toggle', () => {
    if (!details.isConnected) return;
    if (details.open) state.expandedDetails.add(key); else state.expandedDetails.delete(key);
  });
  return details;
}

async function api(path, options = {}) {
  let response;
  try { response = await fetch(path, { credentials: 'same-origin', ...options, headers: { Accept: 'application/json', ...(options.headers || {}) } }); }
  catch { throw new Error('无法连接本地观测服务，请检查服务是否仍在运行。'); }
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    if (response.status === 401) showLogin();
    throw new Error(data.error || '读取失败（HTTP ' + response.status + '）');
  }
  return data;
}
function showLogin() {
  state.authenticated = false;
  if (state.stream) { state.stream.close(); state.stream = null; }
  $('login').hidden = false;
  document.querySelectorAll('.view').forEach((view) => { view.hidden = true; });
  $('refresh').disabled = true;
  connection('需要访问凭据', 'retry');
}
async function authenticate(token) {
  await api('/api/v1/session', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }) });
  $('access-token').value = '';
  await initialize();
}
async function initialize() {
  const data = await api('/api/v1/workspaces');
  state.workspaces = array(data.items);
  state.authenticated = true; $('login').hidden = true; $('refresh').disabled = false;
  const select = $('workspace-filter'); const selected = select.value;
  select.replaceChildren(node('option', '', '全部工作区')); select.firstChild.value = '';
  state.workspaces.forEach((w) => { const option = node('option', '', w.path); option.value = w.id; select.append(option); });
  select.value = selected;
  renderHealth();
  await changeView(state.view);
  connectStream(); notice('');
}
function renderHealth() {
  const root = $('workspace-health'); root.replaceChildren();
  for (const w of state.workspaces) {
    if (w.error || ['corrupt', 'error', 'missing', 'unavailable'].includes(w.health)) root.append(node('div', 'health-warning', w.path + '：' + (w.error || w.health)));
  }
}
function connectStream() {
  if (state.stream) state.stream.close();
  const stream = new EventSource('/api/v1/stream'); state.stream = stream;
  stream.onopen = () => connection('实时连接正常', 'live');
  stream.onerror = () => connection('连接中断，正在重连', 'retry');
  let timer;
  const update = () => { clearTimeout(timer); timer = setTimeout(() => refreshView().catch((err) => notice(err.message)), 350); };
  stream.addEventListener('change', update);
  stream.addEventListener('reset', update);
}
async function changeView(view) {
  if (view === 'detail' && !state.taskId) return;
  state.view = view;
  document.querySelectorAll('.view').forEach((el) => { el.hidden = el.id !== 'view-' + view; });
  document.querySelectorAll('[data-view]').forEach((el) => { el.classList.toggle('active', el.dataset.view === view); if (el.dataset.view === view) el.setAttribute('aria-current', 'page'); else el.removeAttribute('aria-current'); });
  const headings = { tasks: ['任务', '查看执行进度、交付结果与记录状态。'], detail: ['任务详情', '沿着运行尝试和动作回执核对执行过程。'], alerts: ['异常', '告警只提示需要核实的事实，不自动调整或重试任务。'], evaluations: ['评测', '分别查看产物、范围、引擎、环境和评分器结果。'] };
  $('page-title').textContent = headings[view][0]; $('page-subtitle').textContent = headings[view][1];
  await refreshView();
}
async function refreshView() {
  if (!state.authenticated) return;
  if (state.view === 'tasks') await loadTasks();
  if (state.view === 'detail') await loadTask(state.taskId);
  if (state.view === 'alerts') await loadAlerts();
  if (state.view === 'evaluations') await loadEvaluations();
  $('updated-at').textContent = '视图更新于 ' + time(new Date().toISOString(), true);
}
async function loadTasks() {
  const request = ++state.listRequest;
  const params = new URLSearchParams({ page: state.page, page_size: state.pageSize, workspace: $('workspace-filter').value, status: $('status-filter').value, q: $('search').value.trim() });
  const data = await api('/api/v1/tasks?' + params);
  if (request !== state.listRequest) return;
  state.tasks = array(data.items); state.total = Number(data.total) || 0;
  $('task-count').textContent = number(state.total) + ' 个任务';
  $('page-summary').textContent = '第 ' + state.page + ' / ' + Math.max(1, Math.ceil(state.total / state.pageSize)) + ' 页';
  $('previous-page').disabled = state.page <= 1; $('next-page').disabled = state.page * state.pageSize >= state.total;
  const focused = document.activeElement?.id;
  const root = $('task-list'); root.replaceChildren();
  if (!state.tasks.length) { root.append(empty('没有匹配的任务', state.workspaces.length ? '调整筛选条件，或从 CLI、飞书、Bench 发起任务。' : '启动服务时使用 -workspace 登记需要查看的工作区。')); return; }
  for (const task of state.tasks) {
    const title = button(summary(task.goal) || '未记录任务目标', () => openTask(task.id), 'task-name');
    title.id = 'task-link-' + task.id;
    const workspace = node('span', '', workspaceLabel(task.workspace_id)); workspace.title = workspaceName(task.workspace_id);
    const meta = append(node('div', 'task-meta'), workspace, node('span', '', short(task.task_id)), node('span', '', time(task.last_observed_at)));
    const tags = append(node('div', 'task-status'), badge(task.activity, activityLabels), badge(task.engine_status, statusLabels, '未见终态'));
    if (task.record_quality && !['normal', 'complete', 'healthy', 'ok'].includes(task.record_quality)) tags.append(badge(task.record_quality, qualityLabels));
    if (array(task.alerts).length) tags.append(node('span', 'badge warn', task.alerts.length + ' 条提示'));
    const budget = node('div', 'task-budget');
    append(budget, append(node('div'), node('span', '', '模型 '), node('strong', '', number(task.usage?.model_calls) + ' / ' + number(task.budget?.max_model_calls))), append(node('div'), node('span', '', '工具 '), node('strong', '', number(task.usage?.tool_calls) + ' / ' + number(task.budget?.max_tool_calls))));
    root.append(append(node('article', 'task-row'), append(node('div'), title, meta), tags, budget));
  }
  restoreFocus(focused);
}
async function openTask(id, attemptId = '', sequence = 0) {
  state.taskId = id; state.task = null; state.attemptId = attemptId; state.events = []; state.targetSequence = sequence;
  state.tab = 'timeline'; state.evidence.clear(); state.expandedEvents.clear();
  document.querySelector('[data-view="detail"]').disabled = false;
  $('task-detail').replaceChildren(node('p', 'loading', '正在读取任务…'));
  await changeView('detail');
}
async function loadTask(id) {
  const request = ++state.taskRequest;
  const task = await api('/api/v1/tasks/' + encodeURIComponent(id));
  if (request !== state.taskRequest || id !== state.taskId) return;
  state.task = task;
  if (!array(task.attempts).some((a) => a.id === state.attemptId)) { state.attemptId = array(task.attempts)[0]?.id || ''; state.events = []; }
  renderTask();
  if (state.tab === 'timeline' && state.attemptId) await loadEvents(false, true);
}
function section(title, description) {
  const s = node('section', 'panel-section'); s.append(node('h3', '', title)); if (description) s.append(node('p', 'inline-note', description)); return s;
}
function renderTask() {
  const task = state.task; if (!task) return;
  const focused = document.activeElement?.id;
  const root = $('task-detail'); root.replaceChildren();
  append(root, node('h2', 'detail-goal', summary(task.goal, 120) || '未记录任务目标'), append(node('div', 'detail-meta'), node('span', '', workspaceName(task.workspace_id)), node('span', '', '任务 ' + value(task.task_id)), node('span', '', '权限 ' + value(task.mode))));
  if (task.goal?.length > 120) { const goal = disclosure('goal-' + task.id); goal.append(node('summary', 'inline-note', '展开完整任务目标'), node('div', 'prose', task.goal)); root.append(goal); }
  const facets = node('div', 'status-grid');
  const facet = (label, content, hint) => append(node('div', 'status-facet'), node('span', '', label), content, hint ? node('p', '', hint) : null);
  const latest = array(task.attempts)[0];
  append(facets,
    facet('运行活动', badge(task.activity, activityLabels), latest?.status && latest.status !== task.engine_status ? '本次返回观察：' + (statusLabels[latest.status] || latest.status) : '最近观察：' + time(task.last_observed_at)),
    facet('引擎持久结果', badge(task.engine_status, statusLabels, '未见可靠终态'), task.stop_code ? '停止原因：' + task.stop_code : '持久状态与入口观察分别核对'),
    facet('交付与验收', task.kind === 'answer' && !task.verification ? node('span', 'badge', '问答任务，无修改验收') : badge(task.verification?.passed ? 'VERIFIED' : 'NEEDS_VERIFICATION', { VERIFIED: '历史验收通过', NEEDS_VERIFICATION: '尚无通过的引擎验收' }), array(task.effects).filter((e) => e.file).length + ' 条文件回执 · 外部评分见交付页'),
    facet('记录可靠性', badge(task.record_quality, qualityLabels), task.history_complete ? '结构化效果历史完整' : '效果历史有缺口，不代表没有修改'));
  root.append(facets);
  if (array(task.alerts).length) {
    const summary = append(node('div', 'health-warning'), node('span', '', task.alerts.length + ' 条需要核实的提示 · '), button('查看异常证据', () => { state.tab = 'alerts'; renderTask(); }, 'button link-button'));
    root.append(summary);
  }
  const tabs = node('div', 'tabs'); tabs.setAttribute('role', 'tablist'); tabs.setAttribute('aria-label', '任务详情内容');
  for (const [id, title] of [['timeline', '执行时间线'], ['delivery', '交付与验收'], ['traces', '历史耗时'], ['evidence', '证据'], ['alerts', '异常提示']]) {
    const tab = button(title, () => { state.tab = id; renderTask(); if (id === 'timeline') loadEvents(false, true).catch((e) => notice(e.message)); }, 'tab' + (state.tab === id ? ' active' : ''));
    tab.id = 'tab-' + id; tab.setAttribute('role', 'tab'); tab.setAttribute('aria-selected', String(state.tab === id)); tab.setAttribute('aria-controls', 'detail-panel'); tabs.append(tab);
  }
  tabs.addEventListener('keydown', (event) => {
    const controls = [...tabs.querySelectorAll('button')]; const current = controls.indexOf(document.activeElement);
    if (current < 0) return;
    let next;
    if (event.key === 'ArrowRight') next = (current + 1) % controls.length;
    if (event.key === 'ArrowLeft') next = (current + controls.length - 1) % controls.length;
    if (event.key === 'Home') next = 0;
    if (event.key === 'End') next = controls.length - 1;
    if (next !== undefined) { event.preventDefault(); const id = controls[next].id; controls[next].click(); $(id)?.focus(); }
  });
  root.append(tabs);
  const panel = node('div'); panel.id = 'detail-panel'; panel.setAttribute('role', 'tabpanel'); panel.setAttribute('aria-labelledby', 'tab-' + state.tab); root.append(panel);
  if (state.tab === 'timeline') renderTimeline(panel);
  if (state.tab === 'delivery') renderDelivery(panel, task);
  if (state.tab === 'traces') renderTraces(panel, task);
  if (state.tab === 'evidence') renderEvidence(panel, task);
  if (state.tab === 'alerts') renderAlerts(panel, task.alerts, task);
  restoreFocus(focused);
}
function renderTimeline(root) {
  const task = state.task;
  const picker = node('label', 'attempt-picker'); picker.append(node('span', '', '运行尝试'));
  const select = node('select'); select.id = 'attempt-select'; select.setAttribute('aria-label', '选择运行尝试');
  for (const attempt of array(task.attempts)) { const opt = node('option', '', (attempt.legacy ? '历史片段' : time(attempt.started_at)) + ' · ' + short(attempt.id) + ' · ' + (activityLabels[attempt.activity] || value(attempt.activity))); opt.value = attempt.id; select.append(opt); }
  select.value = state.attemptId; select.disabled = !task.attempts?.length;
  select.addEventListener('change', () => { state.attemptId = select.value; state.events = []; state.targetSequence = 0; renderTask(); loadEvents().catch((e) => notice(e.message)); }); picker.append(select); root.append(picker);
  const attempt = array(task.attempts).find((a) => a.id === state.attemptId);
  const budget = node('div', 'budget-summary');
  for (const [label, val] of [['累计模型', number(task.usage?.model_calls) + ' / ' + number(task.budget?.max_model_calls)], ['累计工具', number(task.usage?.tool_calls) + ' / ' + number(task.budget?.max_tool_calls)], ['累计运行', duration(task.usage?.elapsed_ms)]]) budget.append(append(node('span'), node('span', '', label), node('strong', '', val)));
  root.append(budget);
  if (attempt) {
    const use = attempt.model_usage || {};
    const usage = node('p', 'inline-note');
    const tokenText = known(use.input_tokens) && known(use.output_tokens) ? 'Token 输入 ' + number(use.input_tokens) + ' / 输出 ' + number(use.output_tokens) : 'Token 总量未知（已知部分：输入 ' + number(use.known_input_tokens) + ' / 输出 ' + number(use.known_output_tokens) + '；' + number(use.unknown_token_calls) + ' 次调用未知）';
    const costText = known(use.cost) ? (use.currency || '') + ' ' + Number(use.cost).toFixed(6) : '总费用未知（已知部分 ' + (use.currency || '') + ' ' + number(use.known_cost) + '；' + number(use.unknown_cost_calls) + ' 次调用未知）';
    usage.textContent = attempt.legacy ? '此历史片段缺少完整实时阶段和用量字段；缺失信息显示未知。' : '本次尝试：' + tokenText + ' · ' + costText;
    append(root, usage, append(node('div', 'activity-list'), badge(attempt.record_quality, qualityLabels), attempt.status ? node('span', 'badge', '返回观察：' + (statusLabels[attempt.status] || attempt.status)) : null));
    if (attempt.result_run_id) root.append(node('p', 'inline-note', '返回结果 Run ID：' + attempt.result_run_id));
    for (const activity of array(attempt.activities)) root.append(node('div', 'health-warning', (activityLabels[activity.kind] || activity.kind) + ' · Agent ' + short(activity.agent_id) + ' · ' + short(activity.id) + ' · 开始于 ' + time(activity.started_at)));
  }
  if (array(task.plan).length) {
    const plan = node('details'); plan.append(node('summary', '', '任务计划 · ' + task.plan.length + ' 步'));
    const list = node('ol', 'plain-list'); for (const step of task.plan) list.append(node('li', '', (step.done ? '已完成 · ' : '未完成 · ') + value(step.goal || step.title) + (array(step.depends_on).length ? '（依赖：' + step.depends_on.join('、') + '）' : ''))); plan.append(list); root.append(plan);
  }
  const events = node('div', 'event-list'); events.id = 'events'; root.append(events); renderEvents();
  if (!state.attemptId) events.append(empty('没有可用的运行片段', '该记录尚未提供可关联的执行事件。'));
}
async function loadEvents(more = false, refresh = false) {
  if (!state.attemptId || state.tab !== 'timeline') return;
  const attempt = state.attemptId; const request = ++state.eventRequest;
  let after = more ? state.nextAfter : 0;
  const wanted = refresh ? Math.max(state.events.length, 200) : 200;
  let items = more ? [...state.events] : []; let data;
  do {
    data = await api('/api/v1/attempts/' + encodeURIComponent(attempt) + '/events?' + new URLSearchParams({ after, limit: 200 }));
    if (request !== state.eventRequest || attempt !== state.attemptId) return;
    const page = array(data.items); items.push(...page);
    const next = Number(data.next_after) || (page.at(-1)?.sequence || after);
    if (next <= after) break;
    after = next;
  } while (!more && data.has_more && (items.length < wanted || (state.targetSequence && after < state.targetSequence)));
  state.events = [...new Map(items.map((event) => [event.sequence, event])).values()].sort((a, b) => a.sequence - b.sequence);
  state.nextAfter = after; state.hasMore = !!data.has_more; renderEvents();
  if (state.targetSequence) { const target = $('event-' + state.targetSequence); if (target) { target.classList.add('targeted'); target.scrollIntoView({ block: 'center' }); } state.targetSequence = 0; }
}
function renderEvents() {
  const root = $('events'); if (!root) return; const focused = document.activeElement?.id; root.replaceChildren();
  if (!state.events.length) { root.append(empty('尚无可显示的事件', '新的观测记录会自动刷新；缺失记录不会推断补齐。')); return; }
  for (const event of state.events) {
    const p = event.payload || {};
    const row = node('article', 'event-row'); row.id = 'event-' + event.sequence;
    const stamp = append(node('div', 'event-time'), node('div', '', time(event.recorded_at, true)), node('div', '', '#' + event.sequence));
    const track = append(node('div', 'event-track'), node('span', 'event-dot ' + tone(p.status || (event.event_type.includes('gap') ? 'gap' : ''))));
    const content = node('div', 'event-content');
    const head = append(node('div', 'event-header'), node('strong', '', eventLabels[event.event_type] || event.event_type));
    if (p.tool) head.append(node('span', 'badge', p.tool)); if (p.status) head.append(badge(p.status)); if (p.effect) head.append(badge(p.effect));
    content.append(head);
    const identity = [event.agent_id ? 'Agent ' + short(event.agent_id) : null, event.parent_agent_id ? '父 Agent ' + short(event.parent_agent_id) : null, event.action_id ? '动作 ' + short(event.action_id) : null, event.model_call_id ? '模型调用 ' + short(event.model_call_id) : null, known(p.duration_ms) ? duration(p.duration_ms) : null].filter(Boolean);
    if (identity.length) content.append(node('p', 'event-caption', identity.join(' · ')));
    if (p.summary || p.path || p.error_class || p.stop_code) content.append(node('p', 'event-caption', [p.summary, p.path, p.error_class, p.stop_code].filter(Boolean).join(' · ')));
    const detail = node('details'); const key = state.attemptId + ':' + event.sequence;
    const summary = node('summary', '', '查看结构化记录'); summary.id = 'event-detail-' + event.sequence;
    detail.open = state.expandedEvents.has(key); detail.append(summary, node('pre', 'code-block', JSON.stringify(event, null, 2)));
    detail.addEventListener('toggle', () => { if (detail.open) state.expandedEvents.add(key); else state.expandedEvents.delete(key); }); content.append(detail);
    if (p.evidence_id) content.append(button('查看关联证据', () => openEvidence(p.evidence_id), 'button link-button'));
    append(row, stamp, track, content); root.append(row);
  }
  if (state.hasMore) { const more = button('加载后续事件', async () => { more.disabled = true; try { await loadEvents(true); } catch (e) { notice(e.message); more.disabled = false; } }); root.append(more); }
  restoreFocus(focused);
}
function renderDelivery(root, task) {
  const final = section('最终答复'); final.append(task.final_text ? node('div', 'prose', task.final_text) : node('p', 'inline-note', '没有可展示的最终答复。工具附带文字不作为终答。')); root.append(final);
  const effects = section('动作效果回执', '回执记录动作当时或恢复核实时的效果，不代表文件当前仍存在或任务已验收。');
  if (!task.history_complete) effects.append(node('div', 'health-warning', '结构化效果历史不完整，不能据此认定此前没有修改。'));
  if (!array(task.effects).length) effects.append(node('p', 'inline-note', '没有可展示的效果回执。'));
  for (const effect of array(task.effects)) {
    const row = node('div', 'effect-row');
    const noChange = effect.file && effect.file.before_version === effect.file.after_version;
    append(row, append(node('div', 'row-title'), node('strong', '', effect.file?.path || value(effect.tool)), noChange ? node('span', 'badge', '确认无变化') : badge(effect.effect), effect.status ? badge(effect.status) : null), node('p', 'row-caption', '动作 ' + value(effect.action_id) + ' · ' + value(effect.tool)));
    if (effect.effect === 'UNKNOWN') row.append(node('p', 'row-caption', effect.status === 'SUCCEEDED' ? '调用已成功结束，文件效果仍未核实。' : '效果未知，需要先核实，不能盲目重放。'));
    if (effect.file) row.append(append(node('div', 'versions'), node('div', '', '之前'), node('span', '', value(effect.file.before_version)), node('div', '', '之后'), node('span', '', value(effect.file.after_version))));
    effects.append(row);
  }
  root.append(effects);
  const verification = section('引擎验收', '历史验收绑定当时的工作区版本；此页面不重新执行验收。');
  if (task.verification) {
    verification.append(badge(task.verification.passed ? 'pass' : 'fail', { pass: '当时验收通过', fail: '未通过或已失效' }));
    verification.append(node('p', 'row-caption', '绑定版本：' + value(task.verification.version)));
    if (task.verification.output) verification.append(node('pre', 'code-block', task.verification.output));
  } else verification.append(node('p', 'inline-note', '尚无引擎验收记录。'));
  root.append(verification);
  const external = section('独立产物验收', '外部 oracle 的结果不会修改引擎终态。');
  if (array(task.evaluations).length) external.append(evaluationTable(task.evaluations)); else external.append(node('p', 'inline-note', '没有关联的外部评测记录。')); root.append(external);
}
function renderTraces(root, task) {
  root.append(node('p', 'inline-note', 'Trace 在运行收尾时导出。此处展示历史操作耗时；缺少 Trace 不表示任务失败。'));
  const traces = array(task.traces);
  if (!traces.length) { root.append(empty('没有可用的历史 Trace', '实时阶段请查看执行时间线；历史耗时不会从事件间隔猜测。')); return; }
  const groups = new Map(); for (const trace of traces) { const id = trace.run_id || '未知'; if (!groups.has(id)) groups.set(id, []); groups.get(id).push(trace); }
  for (const [id, rows] of groups) {
    const part = section('运行 ' + short(id));
    const dates = rows.map((row) => [Date.parse(row.start_time), Date.parse(row.end_time)]).filter(([start, end]) => Number.isFinite(start) && Number.isFinite(end) && end >= start);
    const start = dates.length ? Math.min(...dates.map((d) => d[0])) : 0;
    const end = dates.length ? Math.max(...dates.map((d) => d[1])) : 0;
    part.append(node('p', 'inline-note', dates.length ? '起点 ' + time(new Date(start).toISOString()) + ' · 观察跨度 ' + duration(end - start) : '未提供有效开始与结束时间'));
    for (const trace of rows) {
      const row = node('div', 'trace-row'); const name = node('span', 'trace-name', (trace.depth > 0 ? '↳ '.repeat(Math.min(trace.depth, 3)) : '') + trace.name);
      name.title = trace.name;
      const chart = document.createElementNS('http://www.w3.org/2000/svg', 'svg'); chart.setAttribute('viewBox', '0 0 1000 18'); chart.setAttribute('preserveAspectRatio', 'none'); chart.setAttribute('role', 'img'); chart.setAttribute('aria-label', trace.name + '，耗时 ' + duration(trace.duration_ms));
      const from = Date.parse(trace.start_time); const to = Date.parse(trace.end_time);
      if (end > start && Number.isFinite(from) && Number.isFinite(to) && to >= from) {
        const bar = document.createElementNS('http://www.w3.org/2000/svg', 'rect');
        bar.setAttribute('x', String(Math.max(0, Math.min(999, (from - start) / (end - start) * 1000)))); bar.setAttribute('y', '3');
        bar.setAttribute('width', String(Math.max(2, Math.min(1000, (to - from) / (end - start) * 1000)))); bar.setAttribute('height', '12'); bar.setAttribute('rx', '2'); chart.append(bar);
      }
      append(row, name, chart, node('span', 'trace-duration', duration(trace.duration_ms))); part.append(row);
    }
    root.append(part);
  }
}
function renderEvidence(root, task) {
  root.append(node('p', 'inline-note', '按需读取服务端允许的证据片段，单次最多 16 KiB。'));
  if (!array(task.evidence).length) { root.append(empty('没有可读取的证据引用', '历史日志中的自由文本引用不会被猜测为文件访问路径。')); return; }
  for (const evidence of task.evidence) {
    const row = node('div', 'evidence-row'); row.id = 'evidence-' + evidence.id;
    append(row, append(node('div', 'row-title'), node('strong', '', evidence.label || evidence.id), button(state.evidence.has(evidence.id) ? '重新读取' : '读取片段', () => loadEvidence(evidence.id, false), 'button small secondary')));
    const body = node('div', 'evidence-output'); const saved = state.evidence.get(evidence.id);
    if (saved) {
      body.append(node('pre', 'code-block', saved.text || '（空内容）'));
      if (saved.truncated) body.append(node('p', 'inline-note', '内容已按读取限额截断。'));
      if (saved.has_more) body.append(button('读取下一段', () => loadEvidence(evidence.id, true), 'button small secondary'));
      else body.append(node('p', 'inline-note', '已读至当前证据末尾。'));
    }
    row.append(body); root.append(row);
  }
}
async function openEvidence(id) {
  if (!state.task) return;
  if (!array(state.task.evidence).some((e) => e.id === id)) { notice('该证据未包含在当前任务允许读取的引用中。'); return; }
  state.tab = 'evidence'; renderTask(); await loadEvidence(id, false);
  $('evidence-' + id)?.scrollIntoView({ block: 'center' });
}
async function loadEvidence(id, more) {
  try {
    const old = state.evidence.get(id); const taskID = state.taskId;
    const data = await api('/api/v1/evidence/' + encodeURIComponent(id) + '?' + new URLSearchParams({ offset: more ? old?.next_offset || 0 : 0, limit: 16384 }));
    if (taskID !== state.taskId) return;
    state.evidence.set(id, { ...data, text: (more ? old?.text || '' : '') + (data.text || '') });
    if (state.tab === 'evidence') renderTask();
  } catch (e) { notice(e.message); }
}
async function loadAlerts() {
  const tasks = []; let page = 1; let total = 0;
  do { const data = await api('/api/v1/tasks?' + new URLSearchParams({ page, page_size: 200, workspace: $('workspace-filter').value })); tasks.push(...array(data.items)); total = Number(data.total) || 0; if (!array(data.items).length) break; page++; } while (tasks.length < total);
  state.alertTasks = tasks; renderGlobalAlerts();
}
function alertCategory(code) {
  if (/budget|limit/.test(code)) return 'budget'; if (/effect|pending/.test(code)) return 'unknown';
  if (/repeat/.test(code)) return 'repeat'; if (/slow|long/.test(code)) return 'slow';
  if (/activity|stale|heartbeat/.test(code)) return 'activity'; return 'record';
}
function renderGlobalAlerts() {
  const root = $('alert-list'); root.replaceChildren(); const selected = $('alert-filter').value;
  let count = 0;
  for (const task of state.alertTasks) {
    const alerts = array(task.alerts).filter((a) => !selected || alertCategory(a.code) === selected);
    if (alerts.length) { const s = section(summary(task.goal) || short(task.task_id), workspaceLabel(task.workspace_id)); renderAlerts(s, alerts, task); root.append(s); count += alerts.length; }
  }
  if (!count) root.append(empty('当前没有匹配的异常提示', '没有提示不等于已验收，仍需结合任务终态和交付证据判断。'));
}
function renderAlerts(root, alerts, task) {
  if (!array(alerts).length) { root.append(empty('没有异常提示', '任务交付与验收状态仍需分别核对。')); return; }
  for (const alert of array(alerts)) {
    const row = node('article', 'alert-row');
    append(row, node('span', 'badge warn', value(alert.code, '提示')), node('p', 'alert-description', alert.message || '请核对相关记录。'));
    const links = node('div', 'row-title');
    links.append(button(alert.sequence ? '定位事件 #' + alert.sequence : '查看任务', async () => { await openTask(task.id, alert.attempt_id || '', alert.sequence || 0); }, 'button link-button'));
    if (alert.evidence_id) links.append(button('查看证据', async () => { if (state.taskId !== task.id || !state.task) await openTask(task.id); await openEvidence(alert.evidence_id); }, 'button link-button'));
    row.append(links); root.append(row);
  }
}
async function loadEvaluations() { const data = await api('/api/v1/evaluations'); state.evaluations = array(data.items); renderEvaluations(); }
function renderEvaluations() {
  const root = $('evaluation-list'); root.replaceChildren();
  if (!state.evaluations.length) { root.append(empty('没有登记的评测结果', '启动时使用 -eval-root 指定实验目录；此页面不会运行模型或评分器。')); return; }
  const groups = new Map(); for (const item of state.evaluations) { const name = item.experiment_id || '无法读取的实验'; if (!groups.has(name)) groups.set(name, []); groups.get(name).push(item); }
  for (const [name, items] of groups) {
    const part = node('section', 'evaluation'); const header = append(node('div', 'evaluation-header'), node('h2', '', name), node('span', 'muted', items.length + ' 个样本')); part.append(header);
    const arms = [...new Set(items.map((e) => e.arm).filter(Boolean))];
    if (arms.length) {
      const counts = node('div', 'budget-summary');
      for (const arm of arms) { const rows = items.filter((e) => e.arm === arm); counts.append(append(node('span'), node('span', '', arm === 'baseline' ? '旧版综合通过' : arm === 'candidate' ? '新版综合通过' : arm + ' 综合通过'), node('strong', '', rows.filter((e) => e.combined_pass && !e.read_error).length + ' / ' + rows.length))); }
      part.append(counts);
    }
    part.append(evaluationTable(items)); root.append(part);
  }
}
function evaluationTable(items) {
  const table = node('table', 'data-table');
  const head = node('tr'); for (const label of ['样本', '综合', '产物 / 范围', '引擎 / 环境 / 评分器', '调用与费用']) head.append(node('th', '', label));
  const thead = node('thead'); thead.append(head); table.append(thead); const tbody = node('tbody');
  for (const item of items) {
    const row = node('tr'); const identity = node('td');
    append(identity, node('div', '', value(item.case_id) + ' · ' + value(item.repeat)), node('div', 'muted', item.arm === 'baseline' ? '旧版' : item.arm === 'candidate' ? '新版' : value(item.arm)));
    if (item.task_id) identity.append(button('查看任务', async () => {
      const data = await api('/api/v1/tasks?' + new URLSearchParams({ q: item.task_id, workspace: item.workspace_id || '', page_size: 200 }));
      const candidates = array(data.items).filter((t) => t.task_id === item.task_id);
      if (candidates.length === 1) await openTask(candidates[0].id);
      else notice(candidates.length ? '存在多个相同任务 ID，请在任务列表按工作区定位。' : '该评测任务尚未包含在已登记的任务工作区中。');
    }, 'button link-button'));
    const combined = node('td'); combined.append(badge(item.read_error ? 'error' : item.combined_pass ? 'pass' : 'fail'));
    if (item.read_error) combined.append(node('p', 'row-caption', item.read_error));
    const artifact = append(node('td'), append(node('div'), node('span', '', '产物 '), badge(item.artifact_result)), append(node('div'), node('span', '', '范围 '), badge(item.scope_result)));
    const engine = node('td');
    for (const [title, result] of [['引擎', item.engine_result], ['环境', item.environment_result], ['评分器', item.checker_result]]) engine.append(append(node('div'), node('span', '', title + ' '), badge(result)));
    engine.append(node('p', 'row-caption', value(item.engine_status, '终态未知') + (item.stop_code ? ' · ' + item.stop_code : '')));
    const costs = append(node('td'), node('div', '', '模型 ' + number(item.model_calls) + ' · 工具 ' + number(item.tool_calls)), node('div', 'muted', known(item.cost) ? '费用 ' + number(item.cost) : '费用未知'));
    append(row, identity, combined, artifact, engine, costs); tbody.append(row);
    if (array(item.labels).length) { const labelsRow = node('tr'); const cell = node('td'); cell.colSpan = 5; const labels = node('div', 'failure-labels'); item.labels.forEach((label) => labels.append(node('span', 'badge warn', label))); cell.append(labels); labelsRow.append(cell); tbody.append(labelsRow); }
    if (array(item.evidence).length) {
      const evidenceRow = node('tr'); const cell = node('td'); cell.colSpan = 5;
      const details = disclosure('evaluation-' + item.id); details.append(node('summary', '', '查看样本证据 · ' + item.evidence.length + ' 项'));
      for (const evidence of item.evidence) {
        const part = node('div', 'evidence-row'); const output = node('div');
        const render = () => {
          const saved = state.evaluationEvidence.get(evidence.id); if (!saved) return;
          output.replaceChildren(node('pre', 'code-block', saved.text || '（空内容）'));
          if (saved.truncated) output.append(node('p', 'inline-note', '证据已按读取限额截断。'));
          if (saved.has_more) output.append(button('读取下一段', () => load(true), 'button small secondary'));
          else output.append(node('p', 'inline-note', '已读至当前证据末尾。'));
        };
        const load = async (more) => {
          const saved = state.evaluationEvidence.get(evidence.id);
          const data = await api('/api/v1/evidence/' + encodeURIComponent(evidence.id) + '?' + new URLSearchParams({ offset: more ? saved?.next_offset || 0 : 0, limit: 16384 }));
          state.evaluationEvidence.set(evidence.id, { ...data, text: (more ? saved?.text || '' : '') + (data.text || '') });
          if (output.isConnected) render();
          else if (state.view === 'evaluations') renderEvaluations();
          else if (state.view === 'detail' && state.tab === 'delivery') renderTask();
        };
        render();
        append(part, append(node('div', 'row-title'), node('span', '', evidence.label || evidence.id), button('读取证据', () => load(false), 'button small secondary')), output); details.append(part);
      }
      cell.append(details); evidenceRow.append(cell); tbody.append(evidenceRow);
    }
  }
  table.append(tbody); return append(node('div', 'table-wrap'), table);
}

document.querySelectorAll('[data-view]').forEach((el) => el.addEventListener('click', () => changeView(el.dataset.view).catch((e) => notice(e.message))));
$('refresh').addEventListener('click', async () => { $('refresh').disabled = true; notice(''); try { await refreshView(); } catch (e) { notice(e.message); } finally { $('refresh').disabled = !state.authenticated; } });
$('login-form').addEventListener('submit', async (event) => { event.preventDefault(); const token = $('access-token').value.trim(); if (!token) return; const btn = event.currentTarget.querySelector('button'); btn.disabled = true; try { await authenticate(token); } catch (e) { notice(e.message); } finally { btn.disabled = false; } });
let searchTimer;
function applyFilter() { state.page = 1; loadTasks().catch((e) => notice(e.message)); }
$('filters').addEventListener('submit', (e) => { e.preventDefault(); clearTimeout(searchTimer); applyFilter(); });
$('search').addEventListener('input', () => { clearTimeout(searchTimer); searchTimer = setTimeout(applyFilter, 250); });
$('workspace-filter').addEventListener('change', applyFilter); $('status-filter').addEventListener('change', applyFilter);
$('previous-page').addEventListener('click', () => { state.page = Math.max(1, state.page - 1); loadTasks().catch((e) => notice(e.message)); });
$('next-page').addEventListener('click', () => { state.page++; loadTasks().catch((e) => notice(e.message)); });
$('alert-filter').addEventListener('change', renderGlobalAlerts);

(async () => {
  const params = new URLSearchParams(location.hash.slice(1)); const token = params.get('token');
  if (location.hash) history.replaceState(null, '', location.pathname + location.search);
  try { if (token) await authenticate(token); else await initialize(); } catch (e) { showLogin(); notice(e.message); }
})();
