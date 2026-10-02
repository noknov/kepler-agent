export const number = value => Number(value || 0).toLocaleString('zh-CN');
export const duration = value => value == null ? '—' : value < 1000 ? `${Math.round(value)} ms` : value < 60000 ? `${(value / 1000).toFixed(1)} s` : value < 3600000 ? `${(value / 60000).toFixed(1)} min` : value < 86400000 ? `${(value / 3600000).toFixed(1)} h` : `${(value / 86400000).toFixed(1)} d`;
export const tracingText = state => !state ? '未知：当前进程未提供导出状态' : !state.configured ? '未配置导出' : `${state.backend} · 已接收 ${number(state.exported_batches)} 批 / 失败 ${number(state.failed_batches)} 批`;
export function failureText(rate) { return rate == null ? '—' : `${(rate * 100).toFixed(1)}%`; }

export function mount(doc, fetcher = fetch) {
 let token = '', controller, revision = 0;
 const byId = id => doc.getElementById(id);
 const node = (tag, text, className) => { const el = doc.createElement(tag); if (text != null) el.textContent = String(text); if (className) el.className = className; return el; };
 const date = value => value ? new Date(value).toLocaleString('zh-CN') : '—';
 function fill(id, children) { byId(id).replaceChildren(...children); }
 function tags(values) { return Object.entries(values || {}).map(([key, count]) => node('span', `${key} ${number(count)}`, 'tag')); }
 function table(headings, rows) {
  if (!rows.length) return node('div', '没有记录', 'empty');
  const el = node('table'), head = node('thead'), tr = node('tr'), body = node('tbody');
  headings.forEach(text => tr.append(node('th', text))); head.append(tr);
  rows.forEach(cells => { const row = node('tr'); cells.forEach(value => { const td = node('td'); td.append(value?.nodeType ? value : node('span', value)); row.append(td); }); body.append(row); });
  el.append(head, body); return el;
 }
 async function api(path, signal) {
  const response = await fetcher(path, {signal, headers: token ? {'X-Kepler-Agent-Admin-Token': token} : {}});
  if (response.status === 403) { byId('auth').hidden = false; throw new Error('请输入管理员令牌连接数据。'); }
  if (!response.ok) throw new Error(`${path.split('?')[0]} 返回 ${response.status}`);
  return response.json();
 }
 let detailRevision = 0;
 async function details(id) {
  const current = ++detailRevision;
  byId('detail').hidden = false; fill('detail-body', [node('p', '读取步骤…', 'muted')]);
  try {
   const run = await api(`/runs/${encodeURIComponent(id)}`);
   if (current !== detailRevision) return;
   fill('detail-body', [node('p', `Run ${run.id} · ${run.provider || 'unknown'} / ${run.model || 'unknown'} · ${run.termination || run.status}`), node('p', `Session ${run.session_id} · Trace ${run.trace_id || '未记录'}`, 'small'), table(['步骤', '名称', '耗时', '结果'], (run.steps || []).map(step => [step.type, step.name || '—', duration(step.duration_ms), step.error || step.finish_reason || '完成']))]);
  } catch (error) { if (current === detailRevision) fill('detail-body', [node('p', error.message, 'error')]); }
 }
 function runRows(runs, ongoing = false) {
  return (runs || []).map(run => { const link = node('button', run.id, 'link'); link.addEventListener('click', () => details(run.id)); return [link, run.model || 'unknown', run.termination || run.status, ongoing ? duration(Date.now() - new Date(run.started_at).getTime()) : duration(run.duration_ms), ongoing ? date(run.started_at) : run.error || '—']; });
 }
 function render(data) {
  byId('runs').textContent = number(data.runs); byId('rate').textContent = failureText(data.failure_rate); byId('p95').textContent = data.runs ? duration(data.p95_ms) : '—'; byId('dead').textContent = number(data.inbox?.statuses?.dead_letter);
  fill('outcomes', tags(data.statuses));
  fill('issues', [table(['运行', '模型', '终止原因', '耗时', '错误摘要'], runRows(data.recent_issues))]);
  fill('running', [table(['运行', '模型', '状态', '持续时间', '启动时间'], runRows(data.running, true))]);
  const worker = data.worker || {}, metrics = worker.metrics;
  const status = node('p', worker.ready ? 'worker ready' : `worker ${worker.state || 'unknown'} / not ready`, worker.ready ? 'good' : 'error');
  fill('sources', [node('p', 'PostgreSQL · 运行/步骤/队列'), status, node('p', `worker tracing：${tracingText(metrics?.tracing)}`, 'small'), node('p', `监控服务 tracing：${tracingText(data.observability_tracing)}`, 'small')]);
  fill('queues', [...tags(data.inbox?.statuses), node('p', `已过期 processing claim：${number(data.inbox?.expired_claims)}`), node('p', `最老 queued 等待：${duration(data.inbox?.oldest_queued_ms)}`), node('p', '会话输入', 'muted'), ...tags(data.inputs)]);
  fill('deadletters', (data.dead_letters || []).length ? data.dead_letters.map(item => { const box = node('div'); box.append(node('code', item.id), node('p', `${date(item.at)} · ${item.attempts} 次尝试`, 'muted small'), node('p', item.error || '无错误详情', 'error small')); return box; }) : [node('p', '没有死信', 'muted')]);
  byId('updated').textContent = `${date(data.start)} — ${date(data.end)} · 页面可见时每 30 秒刷新`;
 }
 async function load() {
  controller?.abort(); controller = new AbortController(); const current = ++revision;
  byId('refresh').disabled = true; byId('problem').hidden = true;
  try {
   const data = await api(`/overview?window=${encodeURIComponent(byId('window').value)}`, controller.signal);
   if (current !== revision) return; render(data); byId('auth').hidden = true;
   try { const health = await api('/health/tools', controller.signal); if (current === revision) fill('health', [node('p', `${health.overall} · ${date(health.checked_at)}`), table(['工具', '状态'], (health.tools || []).map(item => [item.name, item.status]))]); }
   catch (error) { if (current === revision) fill('health', [node('p', `健康快照不可用：${error.message}`, 'muted')]); }
  } catch (error) { if (current === revision && error.name !== 'AbortError') { byId('problem').hidden = false; byId('problem').textContent = error.message; } }
  finally { if (current === revision) byId('refresh').disabled = false; }
 }
 byId('auth-form').addEventListener('submit', event => { event.preventDefault(); token = byId('token').value.trim(); byId('token').value = ''; load(); });
 byId('refresh').addEventListener('click', load); byId('window').addEventListener('change', load);
 byId('close-detail').addEventListener('click', () => { detailRevision++; byId('detail').hidden = true; });
 const timer = setInterval(() => { if (doc.visibilityState === 'visible' && byId('auth').hidden && !byId('refresh').disabled) load(); }, 30000);
 load(); return () => { controller?.abort(); clearInterval(timer); };
}

if (typeof document !== 'undefined') mount(document);
