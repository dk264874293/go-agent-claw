'use strict';

// 页面只读取服务端投影，所有任务内容均以文本节点显示。
const $ = (id) => document.getElementById(id);
const state = {
  view: 'overview', page: 1, pageSize: 50, total: 0, tasks: [], workspaces: [],
  taskGeneration: 0, task: null, taskId: '', attemptId: '', tab: '', events: [], nextAfter: 0,
  hasMore: false, eventsLoaded: false, evaluations: [], evidence: new Map(),
  expandedEvents: new Set(), expandedDetails: new Set(), evaluationEvidence: new Map(),
  stream: null, authenticated: false, navigation: 0, eventRequest: 0,
  targetSequence: 0, eventSequence: 0, eventAgent: '', eventCategory: '', allEvents: false,
  alertPage: 1, alertTotal: 0, evidenceLoading: new Set(), evaluationLoading: new Set(),
  refreshPromise: null, refreshEpoch: -1, refreshAgain: false, newSequence: 0,
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
  budget_closing_started: '预算收尾已准入', budget_closing_finished: '预算收尾结果已记录',
  readonly_failure: '只读探索失败已记录', readonly_failure_resolved: '只读探索失败边界已解除',
  readonly_closing_started: '只读探索收尾已准入', readonly_closing_finished: '只读探索收尾结果已记录',
  analysis_updated: '分析笔记已记录', analysis_update: '分析笔记已记录', analysis_evidence: '分析原文证据已记录',
  request_manifest: '实际请求清单', request_metadata: '实际请求元数据', request_diagnostic: '本地请求诊断',
  work_notes_processed: '读取笔记回执', work_notes_rejected: '读取笔记未采用',
  source_reviews_updated: '文档处置声明已记录', delivery_review_evaluated: '交付前处理已评估', delivery_review_turn: '处理轮次票据已消费', delivery_review_request: '处理请求票据已消费', delivery_review_response: '处理响应已记录', delivery_review_completed: '交付前处理响应已结束', delivery_review_updated: '交付待办摘要已更新', delivery_review_interrupted: '处理已中断且不重发', delivery_review_unconfirmed: '处理结果未确认',
  quality_draft: '质量草稿已登记', quality_checked: '本地质量检查已记录',
  source_view_created: '来源视图已读取', source_view_historical: '历史来源材料已查看',
  source_view_delivered: '来源视图已交付模型', source_view_processed: '来源视图已取得处理资格', source_view_folded: '来源视图已折叠', local_check_batch: '局部核对批次已提交',
  local_checks_prepared: '局部核对材料已登记', local_checks_completed: '局部核对结果已记录', local_check_completed: '局部核对已完成',
  quality_review_admitted: '风险复核已准入', quality_review_completed: '风险复核已记录', quality_review_finished: '风险复核已记录',
  quality_revision_admitted: '作者修订已准入', quality_revision_completed: '作者修订已记录', quality_revision_finished: '作者修订已记录',
  quality_frozen: '质量发布材料已冻结', quality_ready: '质量票据已就绪', quality_stage: '质量阶段观察', quality_rejected: '质量检查已阻断', quality_analysis_published: '质量分析已发布', quality_report_reprepared: '质量续交付已准备', quality_material_saved: '受限质量材料已保存',
};

function node(tag, className, text) {
  const item = document.createElement(tag);
  if (className) item.className = className;
  if (text !== undefined && text !== null) item.textContent = String(text);
  return item;
}
function append(parent, ...items) { items.flat().filter(Boolean).forEach((item) => parent.append(item)); return parent; }
function button(text, action, className = 'button secondary') {
  const item = node('button', className, text); item.type = 'button'; item.dataset.key = className + ':' + text;
  item._action = action;
  item.addEventListener('click', (event) => { try { Promise.resolve(item._action(event)).catch((error) => notice(error.message)); } catch (error) { notice(error.message); } });
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
  const details = node('details'); details.dataset.key = key; details.open = state.expandedDetails.has(key);
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
// 按稳定身份协调节点，实时刷新不替换正在阅读的整个详情树。
function domKey(el) { return el.nodeType === 1 ? (el.id || el.dataset.key || '') : ''; }
function syncNode(old, fresh) {
  if (old.nodeType === 3) { if (old.data !== fresh.data) old.data = fresh.data; return; }
  for (const attr of [...old.attributes]) if (attr.name !== 'open' && !fresh.hasAttribute(attr.name)) old.removeAttribute(attr.name);
  for (const attr of [...fresh.attributes]) if (attr.name !== 'open' && old.getAttribute(attr.name) !== attr.value) old.setAttribute(attr.name, attr.value);
  syncChildren(old, fresh);
  if (old instanceof HTMLSelectElement && document.activeElement !== old) old.value = fresh.value;
  if (old instanceof HTMLInputElement && document.activeElement !== old) { old.value = fresh.value; old.checked = fresh.checked; }
  if (old instanceof HTMLButtonElement) { old.disabled = fresh.disabled; if (fresh._action) old._action = fresh._action; }
}
function syncChildren(root, fresh) {
  const old = [...root.childNodes]; const used = new Set();
  const keyed = new Map(old.filter(domKey).map((el) => [domKey(el), el]));
  let cursor = root.firstChild;
  for (const candidate of [...fresh.childNodes]) {
    const key = domKey(candidate);
    let match = key ? keyed.get(key) : (cursor && !domKey(cursor) && !used.has(cursor) ? cursor : null);
    if (match && used.has(match)) match = null;
    if (match && (match.nodeType !== candidate.nodeType || match.nodeName !== candidate.nodeName)) match = null;
    if (match) { used.add(match); syncNode(match, candidate); }
    else match = candidate;
    if (match !== cursor) root.insertBefore(match, cursor);
    cursor = match.nextSibling;
  }
  for (const el of old) if (!used.has(el) && el.parentNode === root) el.remove();
}
function workspace() { return $('workspace-filter').value; }
function valid(epoch) { return epoch === state.navigation && state.authenticated; }
function resetTask() {
  state.taskGeneration++; state.evidenceLoading.clear();
  state.task = null; state.events = []; state.eventsLoaded = false; state.nextAfter = 0;
  state.hasMore = false; state.newSequence = 0; state.evidence.clear(); state.expandedEvents.clear();
  state.eventRequest++;
}
function routeURL() {
  const params = new URLSearchParams(); params.set('view', state.view);
  for (const [key, val] of Object.entries({ workspace: workspace(), q: $('search').value.trim(), status: $('status-filter').value,
    activity_group: $('activity-filter').value, has_alerts: $('alerts-only').checked ? 'true' : '', sort: $('sort-filter').value === 'alerts' ? 'alerts' : '',
    page: state.page > 1 ? state.page : '', alert_q: $('alert-search').value.trim(), category: $('alert-filter').value, alert_page: state.alertPage > 1 ? state.alertPage : '',
    experiment: $('evaluation-experiment').value, arm: $('evaluation-arm').value, result: $('evaluation-result').value })) if (val !== '') params.set(key, val);
  if (state.view === 'detail') {
    for (const [key, val] of Object.entries({ task: state.taskId, attempt: state.attemptId, tab: state.tab, event: state.eventSequence || '', event_agent: state.eventAgent, event_category: state.eventCategory, all_events: state.allEvents ? 'true' : '' })) if (val !== '') params.set(key, val);
  }
  return location.pathname + '?' + params;
}
function writeRoute(mode = 'push') {
  const url = routeURL();
  if (url !== location.pathname + location.search) history[mode === 'replace' ? 'replaceState' : 'pushState'](null, '', url);
}
function readRoute() {
  const params = new URLSearchParams(location.search); const oldTask = state.taskId; const oldAttempt = state.attemptId;
  state.view = ['overview', 'tasks', 'detail', 'alerts', 'evaluations'].includes(params.get('view')) ? params.get('view') : 'overview';
  state.taskId = params.get('task') || ''; state.attemptId = params.get('attempt') || '';
  state.tab = ['timeline', 'delivery', 'traces', 'evidence', 'alerts'].includes(params.get('tab')) ? params.get('tab') : '';
  state.page = Math.max(1, Math.min(1000000, Number(params.get('page')) || 1));
  state.alertPage = Math.max(1, Math.min(1000000, Number(params.get('alert_page')) || 1));
  state.eventSequence = Math.max(0, Number(params.get('event')) || 0); state.targetSequence = state.eventSequence;
  state.eventAgent = params.get('event_agent') || ''; state.eventCategory = params.get('event_category') || ''; state.allEvents = params.get('all_events') === 'true';
  for (const [id, key] of Object.entries({ 'workspace-filter': 'workspace', search: 'q', 'status-filter': 'status', 'activity-filter': 'activity_group', 'alert-search': 'alert_q', 'alert-filter': 'category', 'evaluation-result': 'result' })) $(id).value = params.get(key) || '';
  $('sort-filter').value = params.get('sort') === 'alerts' ? 'alerts' : 'recent'; $('alerts-only').checked = params.get('has_alerts') === 'true';
  // 实验和组别选项由当前工作区的评测结果填充。
  state.evaluationExperiment = params.get('experiment') || ''; state.evaluationArm = params.get('arm') || '';
  if (oldTask !== state.taskId || oldAttempt !== state.attemptId) resetTask();
  if (state.view === 'detail' && !state.taskId) state.view = 'overview';
}
async function initialize() {
  const data = await api('/api/v1/workspaces'); state.workspaces = array(data.items);
  state.authenticated = true; $('login').hidden = true; $('refresh').disabled = false;
  fillOptions($('workspace-filter'), state.workspaces.map((w) => [w.id, w.path]), '全部工作区');
  readRoute(); renderHealth(); await changeView(state.view, false); connectStream(); notice('');
}
function fillOptions(select, options, label, selected = select.value) {
  const fresh = node('select'); const first = node('option', '', label); first.value = ''; first.dataset.key = 'option:'; fresh.append(first);
  for (const [id, text] of options) { const option = node('option', '', text); option.value = id; option.dataset.key = 'option:' + id; fresh.append(option); }
  syncChildren(select, fresh); select.value = selected;
}
function renderHealth() {
  const fresh = node('div');
  for (const w of state.workspaces.filter((w) => !workspace() || w.id === workspace())) {
    if (w.error || ['corrupt', 'error', 'missing', 'unavailable'].includes(w.health)) { const warning = node('div', 'health-warning', w.path + '：' + (w.error || w.health)); warning.dataset.key = w.id; fresh.append(warning); }
  }
  syncChildren($('workspace-health'), fresh);
}
function connectStream() {
  if (state.stream) state.stream.close();
  const stream = new EventSource('/api/v1/stream'); state.stream = stream;
  stream.onopen = () => connection('实时连接正常', 'live');
  stream.onerror = () => connection('连接中断，正在重连', 'retry');
  let timer;
  const update = () => { clearTimeout(timer); timer = setTimeout(() => refreshView().catch((err) => notice(err.message)), 350); };
  stream.addEventListener('change', update);
  stream.addEventListener('reset', () => { state.eventsLoaded = false; update(); });
}
async function changeView(view, save = true) {
  clearTimeout(searchTimer); clearTimeout(alertTimer);
  if (view === 'detail' && !state.taskId) return;
  state.view = view; state.navigation++; state.eventRequest++;
  document.querySelectorAll('.view').forEach((el) => { el.hidden = el.id !== 'view-' + view; });
  document.querySelectorAll('[data-view]').forEach((el) => { el.classList.toggle('active', el.dataset.view === view); if (el.dataset.view === view) el.setAttribute('aria-current', 'page'); else el.removeAttribute('aria-current'); });
  const headings = { overview: ['总览', '掌握运行活动，发现需要核实的任务。'], tasks: ['任务', '查看执行进度、交付结果与记录状态。'], detail: ['任务详情', '核对当前活动、交付结论与执行证据。'], alerts: ['异常', '按任务核实提示，定位相关事件与证据。'], evaluations: ['评测', '分别核对产物、范围、引擎、环境与评分器结果。'] };
  $('page-title').textContent = headings[view][0]; $('page-subtitle').textContent = headings[view][1];
  if (save) writeRoute();
  if (view === 'detail' && !state.task) $('task-detail').replaceChildren(node('p', 'loading', '正在读取任务…'));
  renderHealth(); await refreshView();
}
async function refreshView() {
  if (!state.authenticated) return;
  const epoch = state.navigation;
  if (state.refreshPromise && state.refreshEpoch === epoch) { state.refreshAgain = true; return state.refreshPromise; }
  state.refreshEpoch = epoch; state.refreshAgain = false;
  const view = state.view; const root = $('view-' + view); root.setAttribute('aria-busy', 'true');
  const promise = (async () => {
    try {
      if (view === 'overview') await loadOverview();
      if (view === 'tasks') await loadTasks();
      if (view === 'detail') await loadTask(state.taskId);
      if (view === 'alerts') await loadAlerts();
      if (view === 'evaluations') await loadEvaluations();
      if (valid(epoch)) { $('updated-at').textContent = '更新于 ' + time(new Date().toISOString(), true); notice(''); }
    } catch (error) { if (valid(epoch)) throw error; }
    finally { if (valid(epoch)) root.removeAttribute('aria-busy'); }
  })();
  state.refreshPromise = promise;
  try { await promise; }
  finally {
    if (state.refreshPromise === promise) {
      state.refreshPromise = null;
      if (state.refreshAgain && valid(epoch)) { state.refreshAgain = false; refreshView().catch((error) => notice(error.message)); }
    }
  }
}
function taskRow(task, compact = false) {
  const row = node('article', compact ? 'summary-row' : 'task-row'); row.dataset.key = task.id;
  const title = button(summary(task.goal, compact ? 75 : 110) || '未记录任务目标', () => openTask(task.id), 'task-name'); if (!compact) title.id = 'task-link-' + task.id;
  const path = node('span', '', workspaceLabel(task.workspace_id)); path.title = workspaceName(task.workspace_id);
  const meta = append(node('div', 'task-meta'), path, node('span', '', short(task.task_id)), node('span', '', time(task.last_observed_at)));
  const tags = append(node('div', 'task-status'), badge(task.activity, activityLabels), badge(task.engine_status, statusLabels, '未见终态'));
  if (array(task.activity_groups).includes('approval')) tags.prepend(node('span', 'badge info', '等待审批'));
  const alerts = task.alert_count ?? array(task.alerts).length;
  if (alerts) tags.append(node('span', 'badge warn', alerts + ' 条提示'));
  if (task.record_quality && !['normal', 'complete', 'healthy', 'ok'].includes(task.record_quality)) tags.append(badge(task.record_quality, qualityLabels));
  append(row, append(node('div'), title, meta), tags);
  if (!compact) {
    const budget = node('div', 'task-budget');
    for (const [label, used, max] of [['模型', task.usage?.model_calls, task.budget?.max_model_calls], ['工具', task.usage?.tool_calls, task.budget?.max_tool_calls]]) budget.append(append(node('div'), node('span', '', label + ' '), node('strong', '', number(used) + ' / ' + (max > 0 ? number(max) : '未记录上限'))));
    row.append(budget);
  }
  return row;
}
function renderTaskRows(root, tasks, compact = false) {
  const fresh = node('div');
  for (const task of tasks) fresh.append(taskRow(task, compact));
  if (!tasks.length) fresh.append(empty('暂无匹配任务', '调整筛选，或从 CLI、飞书、Bench 发起任务。'));
  syncChildren(root, fresh);
}
async function loadOverview() {
  const epoch = state.navigation; const data = await api('/api/v1/overview?' + new URLSearchParams({ workspace: workspace() })); if (!valid(epoch)) return;
  $('overview-total').textContent = number(data.total) + ' 个任务 · 分类可能重叠';
  const fresh = node('div');
  for (const [key, label, hint] of [['running', '运行活动', '当前有活动或正在收尾'], ['approval', '等待审批', '运行活动中正在等待审批'], ['alerts', '有异常提示', '需要结合事件与证据核实'], ['unknown', '活动未知', '活动未知不代表任务失败']]) {
    const card = button('', () => { $('activity-filter').value = key === 'alerts' ? '' : key; $('alerts-only').checked = key === 'alerts'; $('search').value = ''; $('status-filter').value = ''; state.page = 1; return changeView('tasks'); }, 'metric-card ' + (key === 'running' ? 'info' : 'warn'));
    card.dataset.key = key; card.setAttribute('aria-label', label + '，' + number(data.counts?.[key]) + ' 个任务');
    append(card, node('span', 'metric-label', label), node('strong', 'metric-value', number(data.counts?.[key])), node('span', 'metric-hint', hint)); fresh.append(card);
  }
  syncChildren($('overview-summary'), fresh);
  renderTaskRows($('overview-recent'), array(data.recent), true); renderTaskRows($('overview-attention'), array(data.attention), true);
  if (Array.isArray(data.workspaces)) {
    const updates = new Map(data.workspaces.map((w) => [w.id, w])); state.workspaces = state.workspaces.map((w) => updates.get(w.id) || w); renderHealth();
  }
}
async function loadTasks() {
  const epoch = state.navigation;
  if (!$('task-list').hasChildNodes()) $('task-list').append(node('p', 'loading', '正在读取任务…'));
  const params = new URLSearchParams({ page: state.page, page_size: state.pageSize, workspace: workspace(), status: $('status-filter').value, q: $('search').value.trim(), activity_group: $('activity-filter').value, sort: $('sort-filter').value, view: 'summary' });
  if ($('alerts-only').checked) params.set('has_alerts', 'true');
  const data = await api('/api/v1/tasks?' + params); if (!valid(epoch)) return;
  state.tasks = array(data.items); state.total = Number(data.total) || 0;
  if (!state.tasks.length && state.total && state.page > Math.ceil(state.total / state.pageSize)) { state.page = Math.ceil(state.total / state.pageSize); writeRoute('replace'); return loadTasks(); }
  $('task-count').textContent = number(state.total) + ' 个任务';
  $('page-summary').textContent = '第 ' + state.page + ' / ' + Math.max(1, Math.ceil(state.total / state.pageSize)) + ' 页';
  $('previous-page').disabled = state.page <= 1; $('next-page').disabled = state.page * state.pageSize >= state.total;
  renderTaskRows($('task-list'), state.tasks);
}
async function openTask(id, attemptId = '', sequence = 0) {
  resetTask(); state.taskId = id; state.attemptId = attemptId; state.tab = sequence || attemptId ? 'timeline' : '';
  state.targetSequence = sequence; state.eventSequence = sequence; state.eventAgent = ''; state.eventCategory = ''; state.allEvents = false;
  $('task-detail').replaceChildren(node('p', 'loading', '正在读取任务…'));
  await changeView('detail');
}
async function loadTask(id) {
  const epoch = state.navigation; const task = await api('/api/v1/tasks/' + encodeURIComponent(id)); if (!valid(epoch) || id !== state.taskId) return;
  state.task = task;
  if (!array(task.attempts).some((a) => a.id === state.attemptId)) {
    state.attemptId = array(task.attempts)[0]?.id || ''; state.events = []; state.eventsLoaded = false; state.nextAfter = 0; state.hasMore = false;
  }
  if (!state.tab) { const latest = array(task.attempts).find((a) => !a.legacy); state.tab = latest && latest.activity !== 'returned' ? 'timeline' : 'delivery'; }
  writeRoute('replace'); renderTask();
  if (state.tab === 'timeline' && state.attemptId) {
    if (state.targetSequence) await loadEvents(state.eventsLoaded);
    else if (!state.eventsLoaded) await loadEvents(false);
    else if (!state.hasMore) await loadEvents(true, true);
    else { const pending = $('event-update'); if (pending) { pending.textContent = '执行记录有更新，继续加载后续事件以查看。'; pending.hidden = false; } }
  }
}
function section(title, description) {
  const s = node('section', 'panel-section'); s.dataset.key = title; s.append(node('h3', '', title)); if (description) s.append(node('p', 'inline-note', description)); return s;
}
function renderTask() {
  const task = state.task; if (!task) return;
  const focused = document.activeElement?.id;
  const root = node('div');
  append(root, node('h2', 'detail-goal', summary(task.goal, 120) || '未记录任务目标'), append(node('div', 'detail-meta'), node('span', '', workspaceName(task.workspace_id)), node('span', '', '任务 ' + value(task.task_id)), node('span', '', '权限 ' + value(task.mode))));
  if (task.goal?.length > 120) { const goal = disclosure('goal-' + task.id); goal.append(node('summary', 'inline-note', '展开完整任务目标'), node('div', 'prose', task.goal)); root.append(goal); }
  const facets = node('div', 'status-grid');
  const facet = (label, content, hint) => append(node('div', 'status-facet'), node('span', '', label), content, hint ? node('p', '', hint) : null);
  const latest = array(task.attempts)[0];
  const delegationLabels = { auto: '自动准入', off: '关闭', on: '显式开启' };
  append(facets,
    facet('运行活动', badge(task.activity, activityLabels), latest?.status && latest.status !== task.engine_status ? '本次返回观察：' + (statusLabels[latest.status] || latest.status) : '最近观察：' + time(task.last_observed_at)),
    facet('引擎持久结果', badge(task.engine_status, statusLabels, '未见可靠终态'), task.stop_code ? '停止原因：' + task.stop_code : '持久状态与入口观察分别核对'),
    facet('交付与验收', task.kind === 'answer' && !task.verification ? node('span', 'badge', '问答任务，无修改验收') : badge(task.verification?.passed ? 'VERIFIED' : 'NEEDS_VERIFICATION', { VERIFIED: '历史验收通过', NEEDS_VERIFICATION: '尚无通过的引擎验收' }), array(task.effects).filter((e) => e.file).length + ' 条文件回执 · 外部评分见交付页'),
    facet('记录可靠性', badge(task.record_quality, qualityLabels), task.history_complete ? '结构化效果历史完整' : '效果历史有缺口，不代表没有修改'));
  const styles = { concise: '精简', detailed: '详细' };
  const policy = facet('执行策略', node('span', 'badge', task.report ? '严格证据报告' : task.analysis_enabled ? '严格分析报告' : (task.kind === 'answer' ? '普通回答' : (task.kind === 'change' ? '修改任务' : '任务类型未知'))), '读取档位：' + number(task.read_page_bytes || 8000) + ' 字节 · 委派：' + (delegationLabels[task.delegation] || '历史未记录') + ' · 篇幅：' + (styles[task.output_style] || '历史未记录'));
  root.append(facets); const policyDetails = disclosure('policy-' + task.id); policyDetails.className = 'policy-details'; policyDetails.append(node('summary', '', '执行策略与配置'), policy); root.append(policyDetails);
  if (array(task.alerts).length) {
    const summary = append(node('div', 'health-warning'), node('span', '', task.alerts.length + ' 条需要核实的提示 · '), button('查看异常证据', () => { selectTab('alerts'); }, 'button link-button'));
    root.append(summary);
  }
  const tabs = node('div', 'tabs'); tabs.setAttribute('role', 'tablist'); tabs.setAttribute('aria-label', '任务详情内容');
  for (const [id, title] of [['timeline', '执行时间线'], ['delivery', '交付与验收'], ['traces', '历史耗时'], ['evidence', '证据'], ['alerts', '异常提示']]) {
    const tab = button(title, () => selectTab(id), 'tab' + (state.tab === id ? ' active' : ''));
    tab.id = 'tab-' + id; tab.tabIndex = state.tab === id ? 0 : -1; tab.setAttribute('role', 'tab'); tab.setAttribute('aria-selected', String(state.tab === id)); tab.setAttribute('aria-controls', 'detail-panel'); tabs.append(tab);
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
  syncChildren($('task-detail'), root);
  restoreFocus(focused);
}
async function selectTab(id) {
  state.tab = id; state.navigation++; state.eventRequest++; writeRoute(); renderTask();
  if (id === 'timeline') { if (!state.eventsLoaded) await loadEvents(); else if (!state.hasMore) await loadEvents(true, true); }
}
function renderTimeline(root) {
  const task = state.task;
  const picker = node('label', 'attempt-picker'); picker.append(node('span', '', '运行尝试'));
  const select = node('select'); select.id = 'attempt-select'; select.setAttribute('aria-label', '选择运行尝试');
  for (const attempt of array(task.attempts)) { const opt = node('option', '', (attempt.legacy ? '历史片段' : time(attempt.started_at)) + ' · ' + short(attempt.id) + ' · ' + (activityLabels[attempt.activity] || value(attempt.activity))); opt.value = attempt.id; opt.dataset.key = 'option:' + attempt.id; select.append(opt); }
  select.value = state.attemptId; select.disabled = !task.attempts?.length;
  select.addEventListener('change', () => { state.attemptId = select.value; state.navigation++; state.events = []; state.eventsLoaded = false; state.nextAfter = 0; state.hasMore = false; state.eventSequence = 0; state.targetSequence = 0; state.newSequence = 0; writeRoute(); renderTask(); loadEvents().catch((e) => notice(e.message)); }); picker.append(select); root.append(picker);
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
    const plan = disclosure('plan-' + task.id); plan.append(node('summary', '', '任务计划 · ' + task.plan.length + ' 步'));
    const list = node('ol', 'plain-list'); for (const step of task.plan) list.append(node('li', '', (step.done ? '已完成 · ' : '未完成 · ') + value(step.goal || step.title) + (array(step.depends_on).length ? '（依赖：' + step.depends_on.join('、') + '）' : ''))); plan.append(list); root.append(plan);
  }
  const filters = node('div', 'event-filters'); filters.dataset.key = 'event-filters';
  const agent = node('select'); agent.id = 'event-agent'; agent.setAttribute('aria-label', '筛选已加载事件的 Agent');
  fillOptions(agent, [...new Set(state.events.map((event) => event.agent_id).filter(Boolean))].sort().map((id) => [id, 'Agent ' + short(id)]), '全部 Agent', state.eventAgent);
  const category = node('select'); category.id = 'event-category'; category.setAttribute('aria-label', '筛选已加载事件类别');
  fillOptions(category, [['run', '运行与计划'], ['model', '模型'], ['tool', '工具'], ['agent', '子任务'], ['approval', '审批'], ['other', '其他记录']], '全部事件类别', state.eventCategory);
  const all = node('input'); all.type = 'checkbox'; all.id = 'all-events'; all.checked = state.allEvents;
  agent.addEventListener('change', () => { state.eventAgent = agent.value; writeRoute(); renderEvents(); });
  category.addEventListener('change', () => { state.eventCategory = category.value; writeRoute(); renderEvents(); });
  all.addEventListener('change', () => { state.allEvents = all.checked; writeRoute(); renderEvents(); });
  append(filters, append(node('label'), node('span', '', 'Agent'), agent), append(node('label'), node('span', '', '事件类别'), category), append(node('label', 'checkbox-field'), all, node('span', '', '显示全部事件'))); root.append(filters);
  const count = node('p', 'inline-note'); count.id = 'event-count'; root.append(count);
  const update = node('div', 'live-update-bar'); update.id = 'event-update'; update.hidden = !state.newSequence; update.textContent = state.newSequence ? '有新增事件，阅读位置已保留。' : ''; root.append(update);
  const latest = button('查看新增事件', () => { const target = $('event-' + state.newSequence); if (target) target.scrollIntoView({ block: 'center' }); state.newSequence = 0; renderTask(); }, 'button small secondary'); latest.id = 'show-new-events'; latest.hidden = !state.newSequence; root.append(latest);
  const events = node('div', 'event-list'); events.id = 'events'; root.append(events); renderEvents(events, count);
  if (!state.attemptId) events.append(empty('没有可用的运行片段', '该记录尚未提供可关联的执行事件。'));
}
async function loadEvents(more = false, refresh = false) {
  if (!state.attemptId || state.tab !== 'timeline') return;
  const attempt = state.attemptId; const epoch = state.navigation; const request = ++state.eventRequest;
  let after = more ? state.nextAfter : 0; let items = more ? [...state.events] : []; let data;
  const previous = state.nextAfter; const wanted = more ? 0 : Math.max(state.events.length, 200);
  do {
    data = await api('/api/v1/attempts/' + encodeURIComponent(attempt) + '/events?' + new URLSearchParams({ after, limit: 200 }));
    if (!valid(epoch) || request !== state.eventRequest || attempt !== state.attemptId || state.tab !== 'timeline') return;
    const page = array(data.items); items.push(...page);
    const next = Number(data.next_after) || (page.at(-1)?.sequence || after);
    if (next <= after) break;
    after = next;
  } while (data.has_more && (items.length < wanted || (state.targetSequence && after < state.targetSequence)));
  state.events = [...new Map(items.map((event) => [event.sequence, event])).values()].sort((a, b) => a.sequence - b.sequence);
  state.nextAfter = after; state.hasMore = !!data.has_more; state.eventsLoaded = true;
  if (refresh && after > previous && !state.newSequence) state.newSequence = state.events.find((event) => event.sequence > previous && eventVisible(event))?.sequence || 0;
  renderTask();
  if (state.targetSequence) {
    const target = $('event-' + state.targetSequence);
    if (target) target.scrollIntoView({ block: 'center' });
    else notice('未找到目标事件，记录可能存在缺口。');
    state.targetSequence = 0;
  }
}
function eventCategory(event) {
  const type = event.event_type;
  if (/^approval/.test(type)) return 'approval'; if (/^(agent|subtask)_/.test(type)) return 'agent';
  if (/^(model|request_)/.test(type)) return 'model'; if (/^(tool|action|receipt)/.test(type)) return 'tool';
  if (/^(run_|plan|step|started$|finished$|reconciled$)/.test(type)) return 'run'; return 'other';
}
function eventCritical(event) {
  const p = event.payload || {};
  return event.sequence === state.eventSequence || array(state.task?.alerts).some((a) => a.attempt_id === state.attemptId && a.sequence === event.sequence) ||
    /gap|corrupt/.test(event.event_type) || p.incomplete === true || Number(p.dropped) > 0 || !!(p.error || p.error_class || p.stop_code || p.evidence_id || p.artifact_id || p.request?.error) || ['dropped', 'error', 'incomplete', 'unavailable'].includes(p.request?.capture_status) || ['FAILED', 'REJECTED', 'CONFLICT', 'UNKNOWN', 'error', 'fail'].includes(p.status) || p.effect === 'UNKNOWN';
}
function requestParameterValue(parameter) {
  if (!parameter || parameter.state === 'unknown') return '未知';
  if (parameter.state === 'absent') return '未发送';
  if (parameter.state === 'null') return 'null（已发送）';
  if (parameter.state === 'value' && Object.prototype.hasOwnProperty.call(parameter, 'value')) return JSON.stringify(parameter.value);
  return '未知';
}
function requestBodyCompleteness(request) {
  if (request.capture_complete === true) return '完整';
  if (request.capture_complete === false) {
    if (request.capture_reason === 'metadata_only') return '未保存（仅元数据）';
    if (request.capture_reason === 'success_body_not_saved') return '成功请求正文未落盘';
    return '不完整';
  }
  return '未知';
}
function renderRequestSummary(target, request) {
  const group = node('div', 'content-group request-summary');
  group.append(node('p', 'event-caption', ['渠道 ' + value(request.provider), '协议 ' + value(request.protocol), '模型 ' + value(request.model), '端点身份 ' + value(request.endpoint_id)].join(' · ')));
  const parameters = Object.entries(request.parameters || {}).sort(([a], [b]) => a.localeCompare(b));
  if (parameters.length) {
    const list = node('dl', 'request-parameters');
    for (const [name, parameter] of parameters) append(list, node('dt', '', name), node('dd', '', requestParameterValue(parameter)));
    group.append(list);
  } else group.append(node('p', 'inline-note', '实际请求参数未知。'));
  const counts = Object.entries(request.role_counts || {}).map(([role, count]) => role + ' ' + number(count));
  group.append(node('p', 'event-caption', '输入项 ' + number(request.input_count) + (counts.length ? ' · ' + counts.join(' / ') : '') + ' · 工具 ' + (array(request.tool_names).length ? request.tool_names.join('、') : '未记录')));
  if (known(request.http_status) || request.service_request_id) group.append(node('p', 'event-caption', 'HTTP ' + value(request.http_status) + ' · 服务请求 ID ' + value(request.service_request_id)));
  if (request.error) {
    const error = request.error;
    group.append(node('p', 'health-warning', ['错误码 ' + requestParameterValue(error.code), '类型 ' + requestParameterValue(error.type), '参数 ' + requestParameterValue(error.param), '请求 ID ' + requestParameterValue(error.request_id), error.summary].filter(Boolean).join(' · ')));
  }
  if (request.previous_model_call_id) {
    group.append(node('p', 'event-caption', '前次成功调用 ' + request.previous_model_call_id + ' · 已记录输入项差异：新增 ' + number(request.added_inputs) + '、移除 ' + number(request.removed_inputs) + '、修改 ' + number(request.changed_inputs)));
    if (request.diff_complete !== true) group.append(node('p', 'inline-note', request.diff_complete === false ? '差异范围不完整；上述计数仅覆盖已记录输入项。' : '差异范围完整性未知。'));
    if (array(request.changed_parameters).length || known(request.schema_changed)) group.append(node('p', 'event-caption', '变化参数 ' + (array(request.changed_parameters).length ? request.changed_parameters.join('、') : '无') + ' · 工具 Schema ' + (known(request.schema_changed) ? (request.schema_changed ? '已变化' : '未变化') : '未知')));
  }
  if (request.manifest_id) group.append(node('p', 'event-caption', '独立清单 ID：' + request.manifest_id));
  if (request.diagnostic_id) group.append(node('p', 'event-caption', '本地诊断 ID：' + request.diagnostic_id));
  const captureModes = { off: '关闭', metadata: '仅元数据', on_error: '失败时记录' };
  const captureStatuses = { pending: '等待保存', saved: '已保存', complete: '副本完整', dropped: '采集丢失', unavailable: '不可用', incomplete: '不完整' };
  if (request.capture_mode || request.capture_status || request.capture_reason) group.append(node('p', 'event-caption', '诊断模式 ' + (captureModes[request.capture_mode] || value(request.capture_mode)) + ' · 保存状态 ' + (captureStatuses[request.capture_status] || value(request.capture_status)) + (request.capture_reason ? ' · 原因 ' + request.capture_reason : '')));
  if (known(request.capture_complete) || known(request.capture_redacted)) group.append(node('p', 'event-caption', '正文副本完整性 ' + requestBodyCompleteness(request) + ' · 脱敏状态 ' + (known(request.capture_redacted) ? (request.capture_redacted ? '已脱敏' : '未发生替换') : '未知')));
  if (known(request.original_bytes) || known(request.saved_bytes)) group.append(node('p', 'event-caption', '原请求 ' + number(request.original_bytes) + ' 字节 · 保存副本 ' + number(request.saved_bytes) + ' 字节'));
  if (request.previous_available === false) group.append(node('p', 'inline-note', '前次成功请求副本不可用；仍可核对已有的结构变化摘要。'));
  if (Number(request.omitted_items) > 0) group.append(node('p', 'inline-note', '有 ' + number(request.omitted_items) + ' 个清单项未在观测元数据中展示。'));
  if (request.diagnostic_id || request.manifest_id) group.append(node('p', 'inline-note', '清单与诊断身份独立于工具证据；诊断正文如有保存，仅在本地受限目录保留，页面不提供读取或下载。'));
  target.append(group);
}
function eventVisible(event) {
  if (eventCritical(event)) return true;
  if (state.eventAgent && event.agent_id !== state.eventAgent) return false;
  if (state.eventCategory && eventCategory(event) !== state.eventCategory) return false;
  const bookkeeping = ['heartbeat', 'model', 'tool', 'turn', 'elapsed'];
  return state.allEvents || !(bookkeeping.includes(event.event_type) || (event.event_type === 'task_event_committed' && bookkeeping.includes(event.payload?.source_kind)));
}
function renderEvents(target = $('events'), counter = $('event-count')) {
  if (!target) return;
  const focused = document.activeElement?.id; const fresh = node('div');
  const visible = state.events.filter(eventVisible);
  if (counter) counter.textContent = '已加载 ' + state.events.length + ' 条 · 当前显示 ' + visible.length + ' 条' + (state.hasMore ? ' · 仍有后续事件' : ' · 已到当前末尾') + '；筛选仅作用于已加载事件，关键诊断记录始终保留。';
  if (!visible.length) fresh.append(empty(state.eventsLoaded ? '当前已加载范围内没有匹配事件' : '正在读取执行事件…', '可显示全部事件、调整筛选或继续加载后续记录。'));
  if (state.eventsLoaded && state.events.some((event) => /^model_/.test(event.event_type)) && !state.events.some((event) => event.payload?.request)) fresh.append(node('p', 'inline-note', '当前已加载记录没有请求诊断元数据，实际参数未知。'));
  for (const event of visible) {
    const p = event.payload || {};
    const row = node('article', 'event-row' + (event.sequence === state.eventSequence ? ' targeted' : '')); row.id = 'event-' + event.sequence;
    const locationLink = button('#' + event.sequence, () => { state.eventSequence = event.sequence; writeRoute(); renderEvents(); $('event-' + event.sequence)?.scrollIntoView({ block: 'center' }); }, 'button link-button event-link'); locationLink.id = 'event-link-' + event.sequence; locationLink.setAttribute('aria-label', '定位事件 #' + event.sequence + ' 并保存链接位置');
    const stamp = append(node('div', 'event-time'), node('div', '', time(event.recorded_at, true)), locationLink);
    const track = append(node('div', 'event-track'), node('span', 'event-dot ' + tone(p.status || (event.event_type.includes('gap') ? 'gap' : ''))));
    const content = node('div', 'event-content');
    const head = append(node('div', 'event-header'), node('strong', '', eventLabels[event.event_type] || event.event_type));
    if (p.tool) head.append(node('span', 'badge', p.tool)); if (p.status) head.append(badge(p.status)); if (p.effect) head.append(badge(p.effect));
    if (p.stage === 'closing') head.append(node('span', 'badge', '禁工具收尾'));
    if (p.stage === 'quality_review') head.append(node('span', 'badge', '定向复核'));
    if (p.stage === 'quality_revision') head.append(node('span', 'badge', '作者修订'));
    if (p.source_kind?.startsWith('budget_closing_') || p.source_kind?.startsWith('readonly_')) head.append(node('span', 'badge', eventLabels[p.source_kind] || p.source_kind));
    content.append(head);
    const identity = [event.agent_id ? 'Agent ' + short(event.agent_id) : null, event.parent_agent_id ? '父 Agent ' + short(event.parent_agent_id) : null, event.action_id ? '动作 ' + short(event.action_id) : null, event.model_call_id ? '模型调用 ' + short(event.model_call_id) : null, known(p.duration_ms) ? duration(p.duration_ms) : null].filter(Boolean);
    if (identity.length) content.append(node('p', 'event-caption', identity.join(' · ')));
    if (p.summary || p.path || p.error_class || p.stop_code) content.append(node('p', 'event-caption', [p.summary, p.path, p.error_class, p.stop_code].filter(Boolean).join(' · ')));
    if (p.request) renderRequestSummary(content, p.request);
    if (event.event_type === 'work_notes_processed') content.append(node('p', 'event-caption', '笔记采纳 ' + number(p.notes_accepted || 0) + ' · 拒收 ' + number(p.notes_rejected || 0) + ' · 未附带 ' + number(p.notes_missing || 0) + ' · 载体 ' + number(p.notes_carrier_bytes || 0) + ' 字节（仅为记忆回执，不表示结论验收通过）'));
    if (known(p.notes_metadata_removed_bytes) || known(p.notes_metadata_retained_bytes)) content.append(node('p', 'event-caption', '后续请求笔记元数据：清理 ' + number(p.notes_metadata_removed_bytes || 0) + ' 字节 · 无法安全清理 ' + number(p.notes_metadata_retained_bytes || 0) + ' 字节'));
    if (p.report_id) content.append(node('p', 'event-caption', '子报告 ' + p.report_id + ' · 执行 ' + value(p.handoff_execution) + ' · 引用 ' + value(p.handoff_citations) + ' · 读取覆盖 ' + value(p.handoff_coverage) + ' · 运行验收 ' + value(p.handoff_verification) + '（各维度独立）'));
    const detail = node('details'); const key = state.attemptId + ':' + event.sequence;
    const summary = node('summary', '', '查看结构化记录'); summary.id = 'event-detail-' + event.sequence;
    detail.open = state.expandedEvents.has(key); detail.append(summary, node('pre', 'code-block', JSON.stringify(event, null, 2)));
    detail.addEventListener('toggle', () => { if (!detail.isConnected) return; if (detail.open) state.expandedEvents.add(key); else state.expandedEvents.delete(key); }); content.append(detail);
    if (p.evidence_id) content.append(button('查看关联证据', () => openEvidence(p.evidence_id), 'button link-button'));
    append(row, stamp, track, content); fresh.append(row);
  }
  if (state.hasMore) { const more = button('加载后续事件', async () => { const control = $('load-more-events'); if (control) control.disabled = true; try { await loadEvents(true); } finally { if ($('load-more-events')) $('load-more-events').disabled = false; } }); more.id = 'load-more-events'; fresh.append(more); }
  syncChildren(target, fresh); restoreFocus(focused);
}
function longText(root, text, key, label = '展开完整内容') {
  if (text.length <= 600) { root.append(node('div', 'prose', text)); return; }
  root.append(node('p', 'prose', summary(text, 240)));
  const details = disclosure(key); details.className = 'content-group'; details.append(node('summary', '', label), node('div', 'prose', text)); root.append(details);
}
function folded(root, task, key, label, render) {
  const details = disclosure(key + '-' + task.id); details.className = 'content-group'; details.append(node('summary', '', label)); render(details, task); root.append(details);
}
function renderDelivery(root, task) {
	const finalText = task.final_text_display ?? task.final_text;
  if (task.stop_code === 'storage_error') root.append(node('div', 'health-warning', '任务记录未可靠保存，以下仅为现场观察，不能作为已持久化证据。'));
  const digest = section('交付摘要', '引擎结果、文件效果和独立验收分别核对。');
  if (task.partial_report?.text) {
    const partial = task.partial_report;
    digest.append(node('p', 'health-warning', '部分分析／任务未完成 · 停止原因：' + value(task.stop_code)));
    digest.append(node('p', 'row-caption', '来源：' + ({ model: '模型收尾汇总', recorded: '已记录事实摘要' }[partial.source] || '来源未知') + (partial.origin_run_id ? ' · 来源运行：' + partial.origin_run_id : '')));
    if (partial.origin_run_id && task.result_run_id && partial.origin_run_id !== task.result_run_id) digest.append(node('p', 'inline-note', '此报告来自历史运行，文件当前状态须重新核实。'));
    longText(digest, partial.text, 'partial-' + task.id, '展开部分分析正文');
  }
  if (finalText) digest.append(node('p', 'prose', summary(finalText, 240)));
  else if (!task.partial_report?.text) digest.append(node('p', 'inline-note', '没有可展示的最终答复。工具附带文字不作为终答。'));
  root.append(digest);
  renderArtifacts(root, task);
  const verification = section('引擎验收', '历史验收绑定当时的工作区版本；此页面不重新执行验收。');
  if (task.verification) {
    verification.append(badge(task.verification.passed ? 'pass' : 'fail', { pass: '当时验收通过', fail: '未通过或已失效' }), node('p', 'row-caption', '绑定版本：' + value(task.verification.version)));
    if (task.verification.output) {
      const output = disclosure('verification-' + task.id); output.append(node('summary', '', '查看验收输出'), node('pre', 'code-block', task.verification.output)); verification.append(output);
    }
  } else verification.append(node('p', 'inline-note', '尚无引擎验收记录。'));
  root.append(verification);
  const external = section('独立产物验收', '外部 oracle 的结果不会修改引擎终态。');
  if (array(task.evaluations).length) external.append(evaluationTable(task.evaluations)); else external.append(node('p', 'inline-note', '没有关联的外部评测记录。')); root.append(external);
  if (finalText) { const final = section('最终答复'); longText(final, finalText, 'answer-' + task.id, '展开最终答复全文'); root.append(final); }
  if (task.quality) folded(root, task, 'quality-gate', '质量核验与风险复核', renderQualityGate);
  if (task.delivery_review) folded(root, task, 'delivery-review', '普通任务交付前处理', renderDeliveryReview);
  if (array(task.local_checks).length) folded(root, task, 'local-checks', '局部确定性核对', renderLocalChecks);
  if (task.report) folded(root, task, 'report', '证据报告与引用检查', renderEvidenceReport);
  if (task.analysis) folded(root, task, 'analysis-group', '分析发现与引用限制', renderAnalysis);
  folded(root, task, 'reading-group', '读取覆盖与工作记忆', renderReading);
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
  const history = disclosure('effects-' + task.id); history.className = 'content-group'; history.append(node('summary', '', '动作效果与版本回执 · ' + array(task.effects).length + ' 条'), effects); root.append(history);
}
function renderLocalChecks(root, task) {
  const part = section('局部确定性核对', '这些是动作当时对指定主体和关系的检查记录，不代表自然语言结论、当前来源或运行验收通过。完整核对材料保存在受限任务记录中。');
  for (const check of array(task.local_checks)) {
    const row = node('article', 'evidence-row');
    append(row, append(node('div', 'row-title'), node('strong', '', value(check.id)), badge(check.result, statusLabels)), node('p', 'row-caption', value(check.kind) + ' · 前提：' + value(check.premise_result) + ' · 运行 ' + value(check.origin_run_id)));
    if (check.origin_run_id && task.result_run_id && check.origin_run_id !== task.result_run_id) row.append(node('p', 'health-warning', '历史运行核对，不能沿用为本次依据。'));
    if (check.reason) row.append(node('p', 'row-caption', check.reason));
    if (check.file_ref || check.reason_code) row.append(node('p', 'row-caption', '文件句柄 ' + value(check.file_ref) + ' · 原因 ' + value(check.reason_code)));
    if (check.scope) row.append(node('p', 'row-caption', '证明范围：' + check.scope));
    for (const source of array(check.sources)) row.append(node('p', 'row-caption', value(source.path) + ':' + number(source.first_line) + '–' + number(source.last_line) + ' · 字节 [' + number(source.byte_start) + ',' + number(source.byte_end) + ') · 版本 ' + value(source.version)));
    part.append(row);
  }
  root.append(part);
}
function renderDeliveryReview(root, task) {
  const r = task.delivery_review; if (!r) return;
  const states = { not_run: '尚未准入', not_needed: '本次无需强制退回', admitted: '已准入，结果待确认', completed: '一次处理响应已记录', failed: '处理请求失败', outcome_unknown: '处理结果未确认，不自动重发' };
  const part = section('普通任务交付前处理', '处理机会和文档处置声明不证明重要问题无遗漏，也不代表语义或运行验收。');
  if (r.recorded_only) part.append(node('p', 'health-warning', '历史交付前处理记录，本次未重新核对当前文件。'));
  append(part, node('p', 'row-title', (states[r.status] || value(r.status)) + ' · 策略 v' + number(r.version)), node('p', 'row-caption', '准入 ' + number(r.admission_count) + ' 次；文档共 ' + number(r.documents) + '；有来源发现 ' + number(r.addressed) + '；不相关 ' + number(r.not_relevant) + '；待核实 ' + number(r.unverified) + '；未声明 ' + number(r.undeclared) + '；待重新处理 ' + number(r.ready_to_resolve)));
  if (r.origin_run_id) part.append(node('p', 'row-caption', '来源运行：' + r.origin_run_id + (task.result_run_id && task.result_run_id !== r.origin_run_id ? '（历史来源，本次未因此新增请求）' : '')));
  const comparisons = { changed: '正文与处理前草稿不同', unchanged: '正文与处理前草稿相同', not_compared: '尚无可比较的确认正文', unknown: '动作效果未知，无法比较正文' };
  for (const c of array(r.content_comparisons)) part.append(node('p', 'inline-note', '正文比较 · ' + (c.kind === 'answer' ? '终答' : value(c.path)) + '：' + (comparisons[c.status] || '比较状态未知') + '；仅比较确认时字节，不代表语义改善或当前文件状态。'));
  for (const reason of array(r.trigger_reasons)) part.append(node('p', 'inline-note', '触发依据：' + reason));
  for (const issue of array(r.unresolved)) {
    const sources = [issue.path ? issue.path + ' · 版本 ' + value(issue.version) : '', ...array(issue.evidence_ids), ...array(issue.finding_ids), ...array(issue.check_ids)].filter(Boolean).join(' · ');
    const item = node('div', 'health-warning');
    append(item, node('p', 'row-title', value(issue.id) + ' · ' + value(issue.kind)), node('p', 'prose', issue.reason));
    if (sources) item.append(node('p', 'row-caption', '依据：' + sources));
    part.append(item);
  }
  if (Number(r.omitted) > 0) part.append(node('p', 'inline-note', '另有 ' + number(r.omitted) + ' 项未在摘要展示，详见任务记录。'));
  if (r.error_class) part.append(node('p', 'inline-note', '处理状态原因：' + r.error_class));
  if (r.status === 'admitted' || r.status === 'outcome_unknown') part.append(node('p', 'health-warning', '已准入但无完成回执时结果未知；恢复不会自动重发该处理请求。'));
  part.append(node('p', 'inline-note', '候选答复、工具意图和完整核对材料仅保存在受限任务材料中，可信文件产物单独提供入口。'));
  root.append(part);
}
function renderQualityGate(root, task) {
  const q = task.quality; if (!q) return;
  const part = section('质量核验', '本地核验只证明指定关系；模型复核不代表独立语义验收或运行验收。');
  const stages = { not_run: '未运行', admitted: '已准入，结果待确认', completed: '已完成', invalid: '返回契约无效', failed: '请求失败', skipped: '无需复核' };
  const outcomes = { exploring: '读取与形成发现', checking: '正在核验', checked: '检查已记录', ready: '可提交', blocked: '质量阻断', published: '已发布' };
  append(part, node('p', 'row-title', (outcomes[q.status] || value(q.status)) + ' · 策略 v' + number(q.version)), node('p', 'row-caption', '草稿 ' + value(q.draft_id)));
  append(part, node('p', 'row-caption', '本地检查：' + (statusLabels[q.check_result] || value(q.check_result))), node('p', 'row-caption', '风险复核：' + (stages[q.review_status] || value(q.review_status))), node('p', 'row-caption', '作者修订：' + (stages[q.revision_status] || value(q.revision_status))));
  for (const issue of array(q.unresolved)) part.append(node('p', 'health-warning', '待核实：' + issue));
  part.append(node('p', 'inline-note', '草稿、计算输入及复核正文仅保存在受限任务材料中，本页面只展示质量摘要；可信文件产物单独提供入口。'));
  if (q.review_status === 'admitted' || q.revision_status === 'admitted') part.append(node('p', 'health-warning', '阶段已准入但没有完成回执时，结果未知；恢复不会自动重复请求。'));
  root.append(part);
}
function renderEvidenceReport(root, task) {
  const report = task.report; if (!report) return;
  const part = section('证据报告', '引用检查仅说明来源、版本及原文位置可核对，语义正确性和引擎验收分别表示。');
  const states = { prepared: '已准备，未确认发布', published: '已确认发布', stale: '历史产物或来源已变化', invalid: '报告无效，未发布' };
  append(part, badge(report.status, states), node('p', 'row-caption', '报告 ' + value(report.report_id) + ' · 引用 ' + value(report.reference_result) + ' · 语义评估 ' + value(report.semantic_result)), node('p', 'row-caption', '目标 ' + value(report.target_path) + ' · 产物版本 ' + value(report.output_version)));
  if (report.status === 'stale') part.append(node('p', 'health-warning', '当前文件或来源已不再匹配发布记录，历史确认不会自动重写。'));
  for (const issue of array(report.issues)) part.append(node('p', 'health-warning', issue));
  if (report.text) part.append(node('div', 'prose', report.text));
  for (const [label, artifact] of [['结构化草稿', report.draft_artifact_id], ['冻结渲染材料', report.rendered_artifact_id]]) {
    const evidence = array(task.evidence).find((item) => item.label === '工具证据 ' + artifact);
    if (artifact && evidence) part.append(button('查看' + label, () => openEvidence(evidence.id), 'button link-button'));
  }
  root.append(part);
}
function renderArtifacts(root, task) {
  if (!array(task.artifacts).length) return;
  const part = section('可信产物清单', '路径来自已确认文件回执。读取时核对产物版本；文件后来变化则不展示为历史内容。');
  for (const artifact of task.artifacts) {
    const row = node('article', 'evidence-row'); row.dataset.key = [artifact.action_id, artifact.version, artifact.evidence_id, artifact.path].join(':');
    append(row, node('strong', '', value(artifact.path)), node('p', 'row-caption', '动作 ' + value(artifact.action_id) + ' · 版本 ' + value(artifact.version)));
    if (artifact.evidence_id) row.append(button('查看产物片段', () => openEvidence(artifact.evidence_id), 'button link-button'));
    part.append(row);
  }
  root.append(part);
}
function renderReading(root, task) {
  const reading = task.reading;
  const kinds = { declared: '声明来源覆盖', observed: '已发现材料覆盖', legacy: '历史清单范围' };
  const scopeKind = reading?.scope_kind || 'legacy';
  const part = section(reading ? (kinds[scopeKind] || '范围类型未知') : '读取覆盖', '只说明指定集合、指定版本的读取范围；阅读完整、引用校验、语义正确和运行验收分别判断。');
	if (task.read_context_version) part.append(node('p', 'row-caption', '读取策略 ' + number(task.read_context_version) + (task.read_context_version === 3 ? ' · 历史组合，恢复不升级' : '')));
	if (task.read_features) {
	  const f=task.read_features; part.append(node('p', 'row-caption', '实验配置 v' + number(f.version) + ' · 来源视图 ' + (f.source_views?'开':'关') + ' · 局部核对 ' + (f.local_checks?'开':'关') + ' · 结论提示 ' + (f.claim_focus?'开':'关')));
	}
  if (task.read_scope) {
    const scope = task.read_scope;
    part.append(node('p', 'row-caption', '来源定义 v' + number(scope.version) + ' · 目录过滤 ' + value(scope.name_glob || '*') + ' · 完整阅读要求：' + (scope.require_complete ? '已开启，缺口阻断正常交付' : '未开启，仅展示缺口')));
    for (const path of array(scope.paths)) part.append(node('p', 'row-caption', '声明来源 ' + path));
  }
  if (task.read_memory) {
    const memory = task.read_memory;
    part.append(node('p', 'row-caption', '读取记忆 v' + number(memory.version) + ' · 证据 ' + number(memory.evidence_count) + ' · 笔记与摘要 ' + number(memory.note_count) + ' · 已处理声明 ' + number(memory.processed_count) + ' · 失效记录 ' + number(memory.invalidation_count) + '（累计记录，不代表当前复用资格）'));
  }
  if (!reading) { part.append(node('p', 'inline-note', '没有本次调用的结构化读取覆盖；历史缺失字段保持未知。')); root.append(part); return; }
  const labels = { complete: '集合内已完整读取', partial: '仍有未读区间', unknown: '范围或当前版本未知' };
  part.append(badge(reading.status, labels), node('p', 'row-caption', '完整文件 ' + number(reading.complete_files) + ' / ' + number(reading.total_files)));
  if (scopeKind !== 'declared') part.append(node('p', 'health-warning', scopeKind === 'legacy' ? '历史记录仅能说明当时清单，未声明任务全部来源，不补造整体完整性。' : '未声明任务全部来源；已发现集合完整也不能证明整个任务资料完整。'));
  if (reading.scope_id) part.append(node('p', 'row-caption', '范围 ' + reading.scope_id + ' · 修订 ' + value(reading.scope_revision)));
  const manifests = array(reading.manifest_ids);
  if (manifests.length || reading.manifest_id) part.append(node('p', 'row-caption', '发现清单 ' + (manifests.length ? manifests.join('、') : reading.manifest_id)));
  for (const path of array(reading.unresolved_paths)) part.append(node('p', 'health-warning', '尚未完整解析来源目录 ' + path + '；范围保持未知。'));
  if (reading.recorded_only) part.append(node('p', 'health-warning', '这里只展示历史读取观察，当前文件版本未确认。'));
  if (reading.discovery_incomplete) part.append(node('p', 'health-warning', '发现范围不完整，不能据此宣称全目录已读完。'));
  const details = disclosure('reading-' + task.id); details.append(node('summary', '', '查看逐文件范围与缺口'));
  for (const file of array(reading.files)) {
    const row = node('article', 'evidence-row'); row.dataset.key = file.path + ':' + file.version;
    append(row, node('strong', '', file.path), node('p', 'row-caption', (file.complete ? '该版本已完整读取' : '该版本读取未确认完整') + ' · ' + number(file.read_bytes) + ' / ' + number(file.total_bytes) + ' 源字节 · 版本 ' + value(file.version)));
    for (const gap of array(file.missing)) row.append(node('p', 'health-warning', '未读字节 [' + number(gap.start) + ', ' + number(gap.end) + ')，共 ' + number(Number(gap.end) - Number(gap.start)) + ' 字节'));
    details.append(row);
  }
  part.append(details); root.append(part);
}
function renderAnalysis(root, task) {
  const report = task.analysis;
  if (!report) return;
  const part = section('分析引用检查', '引用有效只证明原文位置、版本和读取依据；ANSWERED 表示报告已答复，不表示结论已经独立验收。');
  const findings = array(report.findings); const unverified = array(report.unverified);
  part.append(node('p', 'row-caption', '有依据的发现 ' + findings.length + ' 条 · 待核实 ' + unverified.length + ' 条'));
  const details = disclosure('analysis-' + task.id);
  details.append(node('summary', '', '查看逐条引用与限制'));
  const labels = { fact: '材料事实', inference: '分析推断', recommendation: '建议', unverified: '待核实' };
  for (const finding of findings) {
    const row = node('article', 'evidence-row'); row.dataset.key = finding.id;
    append(row, append(node('div', 'row-title'), node('strong', '', value(finding.id)), node('span', 'badge', labels[finding.kind] || value(finding.kind)), node('span', 'badge', finding.status === 'evidence_valid' ? '引用已核对' : '引用状态未知')), node('p', 'prose', finding.statement));
    if (finding.context) row.append(node('p', 'row-caption', '背景与适用条件：' + finding.context));
    if (array(finding.depends_on).length) row.append(node('p', 'row-caption', '依赖发现：' + finding.depends_on.join('、')));
    if (finding.limitations) row.append(node('p', 'inline-note', '限制：' + finding.limitations));
    for (const ref of array(finding.evidence)) {
      const location = value(ref.path, '路径未知') + ':' + value(ref.start_line) + (ref.end_line !== ref.start_line ? '–' + value(ref.end_line) : '');
      row.append(node('p', 'row-caption', value(ref.evidence_id) + ' · ' + location + ' · 版本 ' + value(ref.version)));
      if (ref.quote) row.append(node('pre', 'code-block', ref.quote));
      const evidence = array(task.evidence).find((item) => item.label === '工具证据 ' + ref.artifact_id || item.label === '分析原文 ' + ref.artifact_id);
      if (ref.artifact_id && evidence) row.append(button('查看动作当时的原文', () => openEvidence(evidence.id), 'button link-button'));
    }
    details.append(row);
  }
  for (const item of unverified) details.append(node('p', 'health-warning', '待核实：' + item));
  if (Number(report.unverified_dropped) > 0) details.append(node('p', 'health-warning', '另有 ' + number(report.unverified_dropped) + ' 项待核实未展开，存在记录缺口。'));
  part.append(details); root.append(part);
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
    const read = button(state.evidenceLoading.has(evidence.id) ? '正在读取…' : state.evidence.has(evidence.id) ? '重新读取' : '读取片段', () => loadEvidence(evidence.id, false), 'button small secondary'); read.disabled = state.evidenceLoading.has(evidence.id);
    append(row, append(node('div', 'row-title'), node('strong', '', evidence.label || evidence.id), read));
    const body = node('div', 'evidence-output'); const saved = state.evidence.get(evidence.id);
    if (saved) {
      body.append(node('pre', 'code-block', saved.text || '（空内容）'));
      if (saved.truncated) body.append(node('p', 'inline-note', '内容已按读取限额截断。'));
      if (saved.has_more) { const more = button('读取下一段', () => loadEvidence(evidence.id, true), 'button small secondary'); more.disabled = state.evidenceLoading.has(evidence.id); body.append(more); }
      else body.append(node('p', 'inline-note', '已读至当前证据末尾。'));
    }
    row.append(body); root.append(row);
  }
}
async function openEvidence(id) {
  if (!state.task) return;
  if (!array(state.task.evidence).some((e) => e.id === id)) { notice('该证据未包含在当前任务允许读取的引用中。'); return; }
  await selectTab('evidence'); await loadEvidence(id, false);
  $('evidence-' + id)?.scrollIntoView({ block: 'center' });
}
async function loadEvidence(id, more) {
  if (state.evidenceLoading.has(id)) return;
  const old = state.evidence.get(id); const taskID = state.taskId; const generation = state.taskGeneration;
  state.evidenceLoading.add(id); if (state.tab === 'evidence') renderTask();
  try {
    const data = await api('/api/v1/evidence/' + encodeURIComponent(id) + '?' + new URLSearchParams({ offset: more ? old?.next_offset || 0 : 0, limit: 16384 }));
    if (taskID !== state.taskId || generation !== state.taskGeneration) return;
    state.evidence.set(id, { ...data, text: (more ? old?.text || '' : '') + (data.text || '') });
  } catch (e) { if (taskID === state.taskId && generation === state.taskGeneration) notice(e.message); }
  finally { if (generation === state.taskGeneration) { state.evidenceLoading.delete(id); if (state.view === 'detail' && state.tab === 'evidence') renderTask(); } }
}
async function loadAlerts() {
  const epoch = state.navigation;
  if (!$('alert-list').hasChildNodes()) $('alert-list').append(node('p', 'loading', '正在读取异常…'));
  const data = await api('/api/v1/alerts?' + new URLSearchParams({ workspace: workspace(), q: $('alert-search').value.trim(), category: $('alert-filter').value, page: state.alertPage, page_size: state.pageSize }));
  if (!valid(epoch)) return;
  state.alertTotal = Number(data.total) || 0;
  if (!array(data.items).length && state.alertTotal && state.alertPage > Math.ceil(state.alertTotal / state.pageSize)) { state.alertPage = Math.ceil(state.alertTotal / state.pageSize); writeRoute('replace'); return loadAlerts(); }
  $('alert-count').textContent = number(state.alertTotal) + ' 个任务 · ' + number(data.alert_total) + ' 条提示';
  $('alert-summary').textContent = '第 ' + state.alertPage + ' / ' + Math.max(1, Math.ceil(state.alertTotal / state.pageSize)) + ' 页';
  $('alert-previous').disabled = state.alertPage <= 1; $('alert-next').disabled = state.alertPage * state.pageSize >= state.alertTotal;
  const fresh = node('div');
  for (const group of array(data.items)) {
    const task = group.task; const part = section(summary(task.goal) || short(task.task_id), workspaceLabel(task.workspace_id)); part.dataset.key = task.id;
    part.prepend(button('查看任务', () => openTask(task.id), 'button small secondary')); renderAlerts(part, group.alerts, task); fresh.append(part);
  }
  if (!array(data.items).length) fresh.append(empty('当前没有匹配的异常提示', '没有提示不等于已验收，仍需结合任务终态和交付证据判断。'));
  syncChildren($('alert-list'), fresh);
}
function renderAlerts(root, alerts, task) {
  if (!array(alerts).length) { root.append(empty('没有异常提示', '任务交付与验收状态仍需分别核对。')); return; }
  for (const alert of array(alerts)) {
    const row = node('article', 'alert-row'); row.dataset.key = [alert.code, alert.attempt_id, alert.sequence, alert.evidence_id, alert.message].join(':');
    append(row, node('span', 'badge warn', value(alert.code, '提示')), node('p', 'alert-description', alert.message || '请核对相关记录。'));
    const links = node('div', 'row-title');
    links.append(button(alert.sequence ? '定位事件 #' + alert.sequence : '查看任务', async () => { await openTask(task.id, alert.attempt_id || '', alert.sequence || 0); }, 'button link-button'));
    if (alert.evidence_id) links.append(button('查看证据', async () => { if (state.taskId !== task.id || !state.task) await openTask(task.id); await openEvidence(alert.evidence_id); }, 'button link-button'));
    row.append(links); root.append(row);
  }
}
async function loadEvaluations() {
  if (!$('evaluation-list').hasChildNodes()) $('evaluation-list').append(node('p', 'loading', '正在读取评测…'));
  const epoch = state.navigation; const data = await api('/api/v1/evaluations?' + new URLSearchParams({ workspace: workspace() })); if (!valid(epoch)) return;
  state.evaluations = array(data.items); renderEvaluations();
}
function renderEvaluations() {
  fillOptions($('evaluation-experiment'), [...new Set(state.evaluations.map((e) => e.experiment_id).filter(Boolean))].sort().map((id) => [id, id]), '全部实验', state.evaluationExperiment || '');
  fillOptions($('evaluation-arm'), [...new Set(state.evaluations.map((e) => e.arm).filter(Boolean))].sort().map((id) => [id, id === 'baseline' ? '旧版' : id === 'candidate' ? '新版' : id]), '全部对照组', state.evaluationArm || '');
  state.evaluationExperiment = $('evaluation-experiment').value; state.evaluationArm = $('evaluation-arm').value;
  const result = $('evaluation-result').value;
  const items = state.evaluations.filter((e) => (!state.evaluationExperiment || e.experiment_id === state.evaluationExperiment) && (!state.evaluationArm || e.arm === state.evaluationArm) && (!result || (result === 'error' ? !!e.read_error : !e.read_error && (result === 'pass' ? e.combined_pass : !e.combined_pass))));
  $('evaluation-count').textContent = items.length + ' / ' + state.evaluations.length + ' 个样本';
  const root = node('div');
  const groups = new Map(); for (const item of items) { const name = item.experiment_id || '无法读取的实验'; if (!groups.has(name)) groups.set(name, []); groups.get(name).push(item); }
  for (const [name, rows] of groups) {
    const part = node('section', 'evaluation'); part.dataset.key = name;
    part.append(append(node('div', 'evaluation-header'), node('h2', '', name), node('span', 'muted', rows.length + ' 个样本')));
    const counts = node('div', 'budget-summary');
    for (const arm of [...new Set(rows.map((e) => e.arm).filter(Boolean))]) {
      const members = rows.filter((e) => e.arm === arm); counts.append(append(node('span'), node('span', '', (arm === 'baseline' ? '旧版' : arm === 'candidate' ? '新版' : arm) + ' 综合通过'), node('strong', '', members.filter((e) => e.combined_pass && !e.read_error).length + ' / ' + members.length)));
    }
    part.append(counts, evaluationTable(rows)); root.append(part);
  }
  if (!items.length) root.append(empty('没有匹配的评测结果', state.evaluations.length ? '调整实验、对照组或通过情况筛选。' : '启动时使用 -eval-root 指定实验目录；此页面不会运行模型或评分器。'));
  syncChildren($('evaluation-list'), root);
}
function evaluationTable(items) {
  const table = node('table', 'data-table');
  const head = node('tr'); for (const label of ['样本', '综合', '产物 / 范围', '引擎 / 环境 / 评分器', '调用与费用']) head.append(node('th', '', label));
  const thead = node('thead'); thead.append(head); table.append(thead); const tbody = node('tbody');
  for (const item of items) {
    const row = node('tr'); row.dataset.key = item.id + '-result'; const identity = node('td');
    append(identity, node('div', '', value(item.case_id) + ' · ' + value(item.repeat)), node('div', 'muted', item.arm === 'baseline' ? '旧版' : item.arm === 'candidate' ? '新版' : value(item.arm)));
    if (item.task_id) identity.append(button('查看任务', async () => {
      const epoch = state.navigation; const data = await api('/api/v1/tasks?' + new URLSearchParams({ q: item.task_id, workspace: item.workspace_id || '', page_size: 200, view: 'summary' })); if (!valid(epoch)) return;
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
    if (array(item.labels).length) { const labelsRow = node('tr'); labelsRow.dataset.key = item.id + '-labels'; const cell = node('td'); cell.colSpan = 5; const labels = node('div', 'failure-labels'); item.labels.forEach((label) => labels.append(node('span', 'badge warn', label))); cell.append(labels); labelsRow.append(cell); tbody.append(labelsRow); }
    if (array(item.evidence).length) {
      const evidenceRow = node('tr'); evidenceRow.dataset.key = item.id + '-evidence'; const cell = node('td'); cell.colSpan = 5;
      const details = disclosure('evaluation-' + item.id); details.append(node('summary', '', '查看样本证据 · ' + item.evidence.length + ' 项'));
      for (const evidence of item.evidence) {
        const part = node('div', 'evidence-row'); part.dataset.key = evidence.id; const output = node('div');
        const render = () => {
          const saved = state.evaluationEvidence.get(evidence.id); if (!saved) return;
          output.replaceChildren(node('pre', 'code-block', saved.text || '（空内容）'));
          if (saved.truncated) output.append(node('p', 'inline-note', '证据已按读取限额截断。'));
          if (saved.has_more) output.append(button('读取下一段', () => load(true), 'button small secondary'));
          else output.append(node('p', 'inline-note', '已读至当前证据末尾。'));
        };
        const load = async (more) => {
          if (state.evaluationLoading.has(evidence.id)) return;
          state.evaluationLoading.add(evidence.id);
          try {
          const saved = state.evaluationEvidence.get(evidence.id);
          const data = await api('/api/v1/evidence/' + encodeURIComponent(evidence.id) + '?' + new URLSearchParams({ offset: more ? saved?.next_offset || 0 : 0, limit: 16384 }));
          state.evaluationEvidence.set(evidence.id, { ...data, text: (more ? saved?.text || '' : '') + (data.text || '') });
          if (output.isConnected) render();
          else if (state.view === 'evaluations') renderEvaluations();
          else if (state.view === 'detail' && state.tab === 'delivery') renderTask();
          } finally { state.evaluationLoading.delete(evidence.id); }
        };
        render();
        append(part, append(node('div', 'row-title'), node('span', '', evidence.label || evidence.id), button('读取证据', () => load(false), 'button small secondary')), output); details.append(part);
      }
      cell.append(details); evidenceRow.append(cell); tbody.append(evidenceRow);
    }
  }
  table.append(tbody); return append(node('div', 'table-wrap'), table);
}

function handle(action) { return (...args) => { try { return Promise.resolve(action(...args)).catch((error) => notice(error.message)); } catch (error) { notice(error.message); } }; }
document.querySelectorAll('[data-view]').forEach((el) => el.addEventListener('click', handle(() => changeView(el.dataset.view))));
$('back-tasks').addEventListener('click', handle(() => changeView('tasks')));
$('refresh').addEventListener('click', handle(async () => { $('refresh').disabled = true; try { await refreshView(); } finally { $('refresh').disabled = !state.authenticated; } }));
$('login-form').addEventListener('submit', handle(async (event) => { event.preventDefault(); const token = $('access-token').value.trim(); if (!token) return; const btn = event.currentTarget.querySelector('button'); btn.disabled = true; try { await authenticate(token); } finally { btn.disabled = false; } }));
function applyFilter() { state.page = 1; return changeView('tasks'); }
let searchTimer;
$('filters').addEventListener('submit', (event) => { event.preventDefault(); clearTimeout(searchTimer); handle(applyFilter)(); });
$('search').addEventListener('input', () => { clearTimeout(searchTimer); searchTimer = setTimeout(handle(applyFilter), 250); });
for (const id of ['status-filter', 'activity-filter', 'alerts-only', 'sort-filter']) $(id).addEventListener('change', handle(applyFilter));
$('clear-filters').addEventListener('click', handle(() => { clearTimeout(searchTimer); for (const id of ['search', 'status-filter', 'activity-filter']) $(id).value = ''; $('alerts-only').checked = false; $('sort-filter').value = 'recent'; return applyFilter(); }));
$('workspace-filter').addEventListener('change', handle(() => {
  state.page = 1; state.alertPage = 1; state.evaluationExperiment = ''; state.evaluationArm = ''; $('evaluation-experiment').value = ''; $('evaluation-arm').value = ''; renderHealth();
  const view = state.view === 'detail' && workspace() && state.task?.workspace_id !== workspace() ? 'overview' : state.view;
  if (view === 'overview' && state.view === 'detail') { resetTask(); state.taskId = ''; state.attemptId = ''; state.tab = ''; }
  return changeView(view);
}));
$('previous-page').addEventListener('click', handle(() => { state.page = Math.max(1, state.page - 1); return changeView('tasks'); }));
$('next-page').addEventListener('click', handle(() => { state.page++; return changeView('tasks'); }));
let alertTimer;
function applyAlertFilter() { state.alertPage = 1; return changeView('alerts'); }
$('alert-search').addEventListener('input', () => { clearTimeout(alertTimer); alertTimer = setTimeout(handle(applyAlertFilter), 250); });
$('alert-filter').addEventListener('change', handle(applyAlertFilter));
$('alert-previous').addEventListener('click', handle(() => { state.alertPage = Math.max(1, state.alertPage - 1); return changeView('alerts'); }));
$('alert-next').addEventListener('click', handle(() => { state.alertPage++; return changeView('alerts'); }));
for (const id of ['evaluation-experiment', 'evaluation-arm', 'evaluation-result']) $(id).addEventListener('change', () => {
  state.evaluationExperiment = $('evaluation-experiment').value; state.evaluationArm = $('evaluation-arm').value; state.navigation++; renderEvaluations(); writeRoute();
});
window.addEventListener('popstate', handle(() => { readRoute(); return changeView(state.view, false); }));
(async () => {
  const params = new URLSearchParams(location.hash.slice(1)); const token = params.get('token');
  if (location.hash) history.replaceState(null, '', location.pathname + location.search);
  try { if (token) await authenticate(token); else await initialize(); } catch (e) { if (!state.authenticated) showLogin(); notice(e.message); }
})();
