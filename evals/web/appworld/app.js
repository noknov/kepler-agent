const element = id => document.getElementById(id);
const labels = {
  running: '运行中', completed: '完成', pending: '等待', starting: '启动中',
  failed: '失败', execution_failed: '存在运行错误', agent_timeout: '超时',
  agent_error: '执行错误', canceled: '已取消',
};
let current;

function duration(seconds) {
  if (seconds == null) return '—';
  if (seconds < 60) return `${Math.round(seconds)} 秒`;
  return `${Math.floor(seconds / 60)} 分 ${Math.round(seconds % 60)} 秒`;
}

function renderSummary(data) {
  element('identity').textContent = `${data.name} · ${data.model} · ${labels[data.status] || data.status}`;
  element('done').textContent = `${data.finished} / ${data.total}`;
  element('passed').textContent = data.passed;
  element('errors').textContent = data.execution_errors;
  element('rate').textContent = data.finished ? `${(100 * data.passed / data.finished).toFixed(1)}%` : '—';
  element('bar').max = data.total || 1;
  element('bar').value = data.finished;
  element('split').textContent = data.splits.map(split =>
    `${split.name}：${labels[split.status] || split.status}${split.total ? `（${split.total} 题）` : ''}`
  ).join('　');
  element('error').textContent = data.error ? `${data.error.type}: ${data.error.message}` : '';
}

function renderActivity(data) {
  const container = element('active');
  container.replaceChildren();
  const tasks = data.tasks.filter(task => task.status === 'running');
  if (!tasks.length) container.textContent = data.status === 'running' ? '准备下一题…' : '当前无运行任务';
  for (const task of tasks) {
    const title = document.createElement('div');
    const elapsed = (Date.now() - Date.parse(task.started_at)) / 1000;
    title.textContent = `${task.split} / ${task.task} · 已运行 ${duration(elapsed)}`;
    const list = document.createElement('ul');
    for (const event of task.activity) {
      const item = document.createElement('li');
      item.textContent = event.type + (event.tool ? ` · ${event.tool}` : '');
      list.append(item);
    }
    container.append(title, list);
  }
}

function matchesFilter(task, filter) {
  switch (filter) {
    case 'started': return task.status !== 'pending';
    case 'errors': return !['pending', 'running', 'completed'].includes(task.status);
    case 'failed': return task.success === false;
    default: return true;
  }
}

function appendCell(row, text, className = '') {
  const cell = document.createElement('td');
  cell.textContent = text;
  cell.className = className;
  row.append(cell);
}

function renderRows(data) {
  const container = element('rows');
  container.replaceChildren();
  for (const task of data.tasks.filter(task => matchesFilter(task, element('filter').value))) {
    const row = document.createElement('tr');
    const statusClass = task.status === 'running' ? 'running' :
      ['completed', 'pending'].includes(task.status) ? '' : 'bad';
    appendCell(row, task.split);
    appendCell(row, task.task);
    appendCell(row, labels[task.status] || task.status, statusClass);
    appendCell(row, task.success === true ? '通过' : task.success === false ? '未通过' : '—',
      task.success === true ? 'ok' : task.success === false ? 'bad' : '');
    appendCell(row, duration(task.duration_seconds));
    appendCell(row, task.model_requests ?? '—');
    appendCell(row, task.tool_calls ?? '—');
    container.append(row);
  }
}

async function refresh() {
  try {
    const response = await fetch('/api/progress', {cache: 'no-store'});
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    current = await response.json();
    renderSummary(current);
    renderActivity(current);
    renderRows(current);
    element('fresh').textContent = `更新于 ${new Date().toLocaleTimeString()}`;
  } catch (error) {
    element('fresh').textContent = `更新失败：${error.message}`;
  } finally {
    setTimeout(refresh, 5000);
  }
}

element('filter').onchange = () => { if (current) renderRows(current); };
refresh();
