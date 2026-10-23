(function () {
  'use strict';

  var REFRESH_MS = 3000;
  var state = { status: null, timer: null };

  function $(id) {
    return document.getElementById(id);
  }

  function clear(node) {
    while (node.firstChild) {
      node.removeChild(node.firstChild);
    }
  }

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) {
      node.className = className;
    }
    if (text !== undefined && text !== null) {
      node.textContent = String(text);
    }
    return node;
  }

  function showBanner(message, info) {
    var banner = $('banner');
    banner.textContent = message;
    banner.classList.remove('hidden');
    banner.classList.toggle('info', Boolean(info));
  }

  function hideBanner() {
    $('banner').classList.add('hidden');
  }

  async function fetchJSON(url) {
    var response = await fetch(url, {
      headers: { Accept: 'application/json' },
      cache: 'no-store'
    });
    if (response.status === 401) {
      throw new Error('需要登录：请输入管理密码（浏览器会弹出登录框）');
    }
    if (!response.ok) {
      var detail = '';
      try {
        var body = await response.json();
        detail = body.error || '';
      } catch (ignore) {
        detail = '';
      }
      throw new Error('请求失败 (' + response.status + ')' + (detail ? '：' + detail : ''));
    }
    return response.json();
  }

  function formatTime(value) {
    if (!value) {
      return '-';
    }
    var date = new Date(value);
    if (Number.isNaN(date.getTime()) || date.getFullYear() <= 1) {
      return '-';
    }
    return date.toLocaleString('zh-CN', { hour12: false });
  }

  function formatDuration(ms) {
    if (typeof ms !== 'number' || ms < 0) {
      return '-';
    }
    if (ms < 1000) {
      return ms + ' ms';
    }
    return (ms / 1000).toFixed(2) + ' s';
  }

  function formatUptime(startedAt) {
    if (!startedAt) {
      return '-';
    }
    var seconds = Math.max(0, Math.floor((Date.now() - new Date(startedAt).getTime()) / 1000));
    var days = Math.floor(seconds / 86400);
    var hours = Math.floor((seconds % 86400) / 3600);
    var minutes = Math.floor((seconds % 3600) / 60);
    var secs = seconds % 60;
    if (days > 0) {
      return days + ' 天 ' + hours + ' 小时';
    }
    if (hours > 0) {
      return hours + ' 小时 ' + minutes + ' 分钟';
    }
    if (minutes > 0) {
      return minutes + ' 分钟 ' + secs + ' 秒';
    }
    return secs + ' 秒';
  }

  function statusBadge(status) {
    var map = {
      success: ['成功', 'badge-success'],
      failed: ['失败', 'badge-failed'],
      timeout: ['超时', 'badge-timeout'],
      error: ['错误', 'badge-error']
    };
    var item = map[status] || [status || '未知', 'badge-muted'];
    return el('span', 'badge ' + item[1], item[0]);
  }

  function renderStatus(status) {
    $('version').textContent = status.version || 'dev';
    $('uptime').textContent = '已运行 ' + formatUptime(status.started_at);
    $('config-path').textContent = status.config_path || '-';
    $('server-addr').textContent = status.addr || '-';

    var mail = status.mail || {};
    var mailBadge = $('mail-badge');
    if (!mail.enabled) {
      mailBadge.className = 'badge badge-disabled';
      mailBadge.textContent = '未启用';
    } else if (mail.connected) {
      mailBadge.className = 'badge badge-success';
      mailBadge.textContent = '已连接';
    } else if (mail.last_error) {
      mailBadge.className = 'badge badge-error';
      mailBadge.textContent = '连接异常';
    } else {
      mailBadge.className = 'badge badge-warn';
      mailBadge.textContent = '等待轮询';
    }

    $('mail-host').textContent = mail.enabled ? (mail.host || '') : '';
    $('mail-username').textContent = mail.username || '-';
    $('mail-mailbox').textContent = mail.mailbox || '-';
    $('mail-interval').textContent = mail.poll_interval || '-';
    $('mail-lastpoll').textContent = formatTime(mail.last_poll_at);
    $('mail-nextpoll').textContent = mail.enabled ? formatTime(mail.next_poll_at) : '-';
    $('mail-cursor').textContent = mail.uid_validity
      ? 'UIDVALIDITY ' + mail.uid_validity + ' / LAST ' + (mail.last_uid || 0)
      : '-';

    var mailError = $('mail-error');
    if (mail.last_error) {
      mailError.className = 'error-text';
      mailError.textContent = mail.last_error;
    } else {
      mailError.className = 'muted';
      mailError.textContent = '-';
    }

    var exec = status.execute || {};
    $('exec-shell').textContent = exec.shell || '-';
    $('exec-timeout').textContent = exec.timeout || '-';
    $('exec-workdir').textContent = exec.work_dir || '-';

    var counters = status.counters || {};
    $('c-polls').textContent = counters.polls || 0;
    $('c-messages').textContent = counters.messages || 0;
    $('c-matched').textContent = counters.matched || 0;
    $('c-succeeded').textContent = counters.succeeded || 0;
    $('c-failed').textContent = counters.failed || 0;
    $('c-skipped').textContent = counters.skipped || 0;

    renderRules(status.rules || []);
  }

  function renderRules(rules) {
    var body = $('rules-body');
    clear(body);
    $('rules-count').textContent = rules.length ? '共 ' + rules.length + ' 条' : '';
    $('rules-empty').classList.toggle('hidden', rules.length > 0);

    rules.forEach(function (rule) {
      var tr = el('tr');

      var nameCell = el('td');
      nameCell.appendChild(el('div', 'rule-name', rule.name || rule.id));
      nameCell.appendChild(el('div', 'rule-id', rule.id));
      if (!rule.enabled) {
        nameCell.appendChild(el('span', 'badge badge-disabled', '已禁用'));
      }

      var matchCell = el('td', null, rule.summary || '-');
      var commandCell = el('td');
      commandCell.appendChild(el('code', 'rule-command', rule.command || ''));

      var lastCell = el('td');
      if (rule.last_run_at) {
        lastCell.appendChild(statusBadge(rule.last_status));
        lastCell.appendChild(el('div', 'muted', formatTime(rule.last_run_at)));
      } else {
        lastCell.appendChild(el('span', 'muted', '从未执行'));
      }

      tr.appendChild(nameCell);
      tr.appendChild(matchCell);
      tr.appendChild(commandCell);
      tr.appendChild(lastCell);
      body.appendChild(tr);
    });
  }

  function renderHistory(response) {
    var items = response.items || [];
    var body = $('history-body');
    clear(body);
    $('history-total').textContent = response.total ? '共 ' + response.total + ' 条' : '';
    $('history-empty').classList.toggle('hidden', items.length > 0);

    items.forEach(function (item) {
      var tr = el('tr');
      tr.tabIndex = 0;
      tr.setAttribute('title', '点击查看执行详情');
      tr.addEventListener('click', function () {
        openDetail(item.id);
      });
      tr.addEventListener('keydown', function (event) {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          openDetail(item.id);
        }
      });

      var mailCell = el('td');
      mailCell.appendChild(el('div', null, item.subject || '(无主题)'));
      mailCell.appendChild(el('div', 'muted', item.from || ''));

      tr.appendChild(el('td', 'muted', formatTime(item.started_at)));
      tr.appendChild(el('td', null, item.rule_name || item.rule_id));
      tr.appendChild(mailCell);
      tr.appendChild(el('td'));
      tr.lastChild.appendChild(statusBadge(item.status));
      tr.appendChild(el('td', 'muted', formatDuration(item.duration_ms)));
      tr.appendChild(el('td', 'mono', String(item.exit_code)));
      body.appendChild(tr);
    });
  }

  function addKV(list, label, valueNode) {
    list.appendChild(el('dt', null, label));
    var dd = el('dd');
    if (valueNode instanceof Node) {
      dd.appendChild(valueNode);
    } else {
      dd.textContent = valueNode;
    }
    list.appendChild(dd);
  }

  async function openDetail(id) {
    try {
      var detail = await fetchJSON('/api/executions/' + encodeURIComponent(id));
      var meta = $('drawer-meta');
      clear(meta);

      addKV(meta, '规则', (detail.rule_name || detail.rule_id) + ' (' + detail.rule_id + ')');
      addKV(meta, '状态', statusBadge(detail.status));
      addKV(meta, '开始时间', formatTime(detail.started_at));
      addKV(meta, '结束时间', formatTime(detail.finished_at));
      addKV(meta, '耗时', formatDuration(detail.duration_ms));
      addKV(meta, '退出码', String(detail.exit_code));
      addKV(meta, '发件人', detail.from || '-');
      addKV(meta, '主题', detail.subject || '-');
      addKV(meta, '文件夹', detail.mailbox || '-');
      addKV(meta, 'UID', String(detail.uid));
      addKV(meta, 'Message-ID', detail.message_id || '-');

      $('drawer-command').textContent = detail.command || '';
      $('drawer-stdout').textContent = detail.stdout || '(空)';
      $('drawer-stderr').textContent = detail.stderr || '(空)';
      $('drawer-error').textContent = detail.error || '';
      $('drawer-error-wrap').classList.toggle('hidden', !detail.error);
      $('drawer').classList.remove('hidden');
      $('drawer').setAttribute('aria-hidden', 'false');
    } catch (error) {
      showBanner(error.message);
    }
  }

  function closeDrawer() {
    $('drawer').classList.add('hidden');
    $('drawer').setAttribute('aria-hidden', 'true');
  }

  function bindTabs() {
    var tabs = Array.prototype.slice.call(document.querySelectorAll('.tab'));
    tabs.forEach(function (button) {
      button.addEventListener('click', function () {
        var name = button.getAttribute('data-tab');
        tabs.forEach(function (other) {
          var active = other === button;
          other.classList.toggle('active', active);
          other.setAttribute('aria-selected', active ? 'true' : 'false');
        });
        $('panel-rules').classList.toggle('hidden', name !== 'rules');
        $('panel-history').classList.toggle('hidden', name !== 'history');
      });
    });
  }

  async function refresh() {
    var results = await Promise.all([
      fetchJSON('/api/status'),
      fetchJSON('/api/executions?limit=50')
    ]);
    state.status = results[0];
    renderStatus(results[0]);
    renderHistory(results[1]);
    hideBanner();
  }

  function schedule() {
    clearTimeout(state.timer);
    state.timer = setTimeout(async function () {
      if (!document.hidden) {
        try {
          await refresh();
        } catch (error) {
          showBanner(error.message);
        }
      }
      schedule();
    }, REFRESH_MS);
  }

  async function start() {
    try {
      await refresh();
    } catch (error) {
      showBanner(error.message);
    }
    schedule();
  }

  document.addEventListener('visibilitychange', function () {
    if (!document.hidden) {
      refresh().catch(function (error) {
        showBanner(error.message);
      });
    }
  });
  $('drawer-close').addEventListener('click', closeDrawer);
  document.addEventListener('keydown', function (event) {
    if (event.key === 'Escape') {
      closeDrawer();
    }
  });

  bindTabs();
  start();
})();
