// GatusMOD client-side tweaks for the Gatus dashboard. Settings come from window.gatusmod, injected by the proxy.
(function () {
  var settings = window.gatusmod || {};
  var hideHeading = settings.headingShow === false;
  var hideSubtitle = settings.subtitleShow === false;

  // Same fallback as Home.vue: the template placeholder is left in place when Gatus renders without UI config.
  function headingText() {
    var heading = window.config && window.config.dashboardHeading;
    if (!heading || heading === '{{ .UI.DashboardHeading }}') {
      return 'Health Dashboard';
    }
    return heading;
  }

  // The heading is matched on its text instead of CSS classes, so Tailwind class changes in Gatus do not break it.
  function applyHeading() {
    if (!hideHeading && !hideSubtitle) {
      return;
    }
    var text = headingText();
    var headings = document.querySelectorAll('#app h1');
    for (var i = 0; i < headings.length; i++) {
      var h1 = headings[i];
      if (h1.getAttribute('data-gatusmod') || h1.textContent.trim() !== text) {
        continue;
      }
      h1.setAttribute('data-gatusmod', 'heading');
      if (hideHeading) {
        h1.style.display = 'none';
      }
      var subtitle = h1.nextElementSibling;
      if (hideSubtitle && subtitle && subtitle.tagName === 'P') {
        subtitle.style.display = 'none';
      }
    }
  }

  // Replaces the "Powered by Gatus" line in the Gatus footer with the GatusMOD footer. If that line cannot be
  // found (e.g. after a Gatus upgrade), the GatusMOD footer stays visible below the app instead.
  function applyFooter() {
    var custom = document.getElementById('gatusmod-footer');
    if (!custom) {
      return;
    }
    var credit = document.querySelector('#app footer a[href*="gatus.io"]');
    var target = credit && credit.parentElement;
    if (!target || target.getAttribute('data-gatusmod') === 'footer') {
      return;
    }
    target.innerHTML = custom.innerHTML;
    target.setAttribute('data-gatusmod', 'footer');
    custom.style.display = 'none';
  }

  var PERIODS = [
    { id: '30d', label: 'Last 30 days' },
    { id: '7d', label: 'Last 7 days' },
    { id: '24h', label: 'Last 24 hours' },
    { id: '1h', label: 'Last hour' }
  ];
  var REFRESH_MS = 60000;
  var stats = null; // { key, card, timer, data, warned }

  function endpointKeyFromPath() {
    var prefix = '/endpoints/';
    if (location.pathname.indexOf(prefix) !== 0) {
      return null;
    }
    var key = location.pathname.slice(prefix.length).replace(/\/+$/, '');
    try {
      return key ? decodeURIComponent(key) : null;
    } catch (e) {
      return null;
    }
  }

  function cardByTitle(text) {
    var titles = document.querySelectorAll('#app h3');
    for (var i = 0; i < titles.length; i++) {
      if (titles[i].textContent.trim() === text && titles[i].parentElement && titles[i].parentElement.parentElement) {
        return titles[i].parentElement.parentElement;
      }
    }
    return null;
  }

  // The four response-time cards: img -> CardContent -> Card -> grid.
  function responseTimeGrid() {
    var img = document.querySelector('#app img[src*="/response-times/"][src$="/badge.svg"]');
    var grid = img && img.parentElement && img.parentElement.parentElement && img.parentElement.parentElement.parentElement;
    if (!grid || grid.querySelectorAll('img[src*="/response-times/"]').length !== PERIODS.length) {
      return null;
    }
    return grid;
  }

  function hide(el, name) {
    if (el && el.getAttribute('data-gatusmod') !== name) {
      el.style.display = 'none';
      el.setAttribute('data-gatusmod', name);
    }
  }

  function stopStatistics() {
    if (!stats) {
      return;
    }
    clearInterval(stats.timer);
    if (stats.card && stats.card.parentNode) {
      stats.card.parentNode.removeChild(stats.card);
    }
    stats = null;
  }

  function fetchText(url) {
    return fetch(url, { credentials: 'include' }).then(function (r) {
      return r.ok ? r.text() : null;
    }).catch(function () { return null; });
  }

  function fetchNumber(url) {
    return fetchText(url).then(function (t) {
      var n = t === null ? NaN : parseFloat(t);
      return isNaN(n) ? null : n;
    });
  }

  // Gatus answers 0 for both uptime and response time when a period has no data.
  function fetchPeriods(key) {
    var base = '/api/v1/endpoints/' + encodeURIComponent(key);
    return Promise.all(PERIODS.map(function (p) {
      return Promise.all([fetchNumber(base + '/uptimes/' + p.id), fetchNumber(base + '/response-times/' + p.id)]).then(function (v) {
        var uptime = v[0], rt = v[1];
        var noData = uptime === null || (uptime === 0 && !rt);
        return { uptime: noData ? null : uptime, responseTime: noData ? null : rt };
      });
    }));
  }

  var relatedCache = {}; // key -> { at, promise }

  function fetchRelated(key) {
    var hit = relatedCache[key];
    if (hit && Date.now() - hit.at < REFRESH_MS) {
      return hit.promise;
    }
    var promise = fetch('/mod/api/endpoints/' + encodeURIComponent(key) + '/related', { credentials: 'include' })
      .then(function (r) { return r.ok ? r.json() : null; })
      .catch(function () { return null; });
    relatedCache[key] = { at: Date.now(), promise: promise };
    return promise;
  }

  function fetchIPerf3(key) {
    return fetch('/mod/api/endpoints/' + encodeURIComponent(key) + '/iperf3', { credentials: 'include' })
      .then(function (r) {
        if (r.status === 404) {
          return false;
        }
        return r.ok ? r.json() : null;
      })
      .catch(function () { return null; });
  }

  function fetchStatistics(key) {
    return Promise.all([fetchPeriods(key), fetchRelated(key)]).then(function (v) {
      var related = v[1];
      var icmpKey = related && related.icmp;
      var icmp = icmpKey ? fetchPeriods(icmpKey) : Promise.resolve(null);
      var iperf3 = related && related.type === 'iperf3' ? fetchIPerf3(key) : Promise.resolve(null);
      return Promise.all([icmp, iperf3]).then(function (w) {
        return { overall: v[0], icmp: w[0], iperf3: w[1] || null };
      });
    });
  }

  // The iperf3 periods in the order of PERIODS; a period without tests has no data.
  function iperf3Periods(data) {
    var byId = {};
    (data.periods || []).forEach(function (p) { byId[p.id] = p; });
    return PERIODS.map(function (p) {
      var s = byId[p.id];
      if (!s || !s.tests) {
        return { uptime: null, bandwidth: null };
      }
      return { uptime: s.passed / s.tests, bandwidth: s.bandwidth === undefined ? null : s.bandwidth };
    });
  }

  function bandwidthCell(p) {
    if (!p || p.bandwidth === null) {
      return el('span', 'gatusmod-stats-none', '-');
    }
    return el('span', '', p.bandwidth.toFixed(2) + ' Mbit/s');
  }

  function level(uptime) {
    if (uptime === null) {
      return 'none';
    }
    return uptime >= 0.95 ? 'good' : uptime >= 0.9 ? 'warn' : 'bad';
  }

  function percent(uptime) {
    return (Math.floor(uptime * 10000) / 100).toFixed(2) + '%';
  }

  function el(tag, className, text) {
    var e = document.createElement(tag);
    if (className) {
      e.className = className;
    }
    if (text !== undefined) {
      e.textContent = text;
    }
    return e;
  }

  var INFO_SVG = '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg>';

  function labelCell(text, help) {
    var th = el('th', 'gatusmod-stats-label');
    th.setAttribute('scope', 'row');
    th.appendChild(document.createTextNode(text + ' '));
    var info = el('span', 'gatusmod-stats-info');
    info.innerHTML = INFO_SVG;
    info.title = help;
    info.setAttribute('aria-label', help);
    th.appendChild(info);
    return th;
  }

  function row(text, help, periods, cell) {
    var tr = el('tr');
    tr.appendChild(labelCell(text, help));
    for (var i = 0; i < PERIODS.length; i++) {
      var td = el('td');
      td.appendChild(cell(periods ? periods[i] : null));
      tr.appendChild(td);
    }
    return tr;
  }

  function responseTimeCell(p) {
    if (!p || p.responseTime === null) {
      return el('span', 'gatusmod-stats-none', '-');
    }
    return el('span', '', Math.round(p.responseTime) + ' ms');
  }

  function pillCell(p) {
    var u = p ? p.uptime : null;
    return el('span', 'gatusmod-pill gatusmod-' + level(u), u === null ? 'no data' : percent(u));
  }

  function dotCell(p) {
    var u = p ? p.uptime : null;
    var wrap = el('span', 'gatusmod-dot-cell');
    wrap.appendChild(el('span', 'gatusmod-dot gatusmod-' + level(u)));
    wrap.appendChild(el('span', u === null ? 'gatusmod-stats-none' : '', u === null ? 'no data' : percent(u)));
    return wrap;
  }

  function legend() {
    var div = el('div', 'gatusmod-stats-legend');
    [['good', '95% and higher'], ['warn', '90% up to 95%'], ['bad', 'Below 90%'], ['none', 'No data in this period']].forEach(function (item) {
      var span = el('span');
      span.appendChild(el('span', 'gatusmod-dot gatusmod-' + item[0]));
      span.appendChild(document.createTextNode(item[1]));
      div.appendChild(span);
    });
    return div;
  }

  function renderStatistics() {
    var data = stats.data;
    var body = stats.card.querySelector('[data-gatusmod-stats-body]');
    var table = el('table', 'gatusmod-stats');
    var head = el('tr');
    head.appendChild(el('th'));
    PERIODS.forEach(function (p) { head.appendChild(el('th', 'gatusmod-stats-period', p.label)); });
    var thead = el('thead');
    thead.appendChild(head);
    table.appendChild(thead);
    var tbody = el('tbody');
    if (stats.showResponseTime) {
      tbody.appendChild(row('Response time', 'Average response time of the checks in this period.', data && data.overall, responseTimeCell));
    }
    if (data && data.icmp) {
      tbody.appendChild(row('Ping (ICMP)', 'Share of successful ping (ICMP) checks to the same host.', data.icmp, dotCell));
    }
    var ip = data && data.iperf3;
    if (ip && ip.duration) {
      var periods = iperf3Periods(ip);
      var direction = ip.reverse ? 'download' : 'upload';
      if (!ip.rate) {
        tbody.appendChild(row('Bandwidth (' + direction + ')', 'Average ' + direction + ' speed of the passed iperf3 tests in this period.', periods, bandwidthCell));
      }
      var limit = ip.rate ? ', at most ' + ip.rate : '';
      tbody.appendChild(row('iperf3 data test', 'Share of iperf3 ' + direction + ' tests (' + ip.duration + limit + ', every ' + ip.bandwidthInterval + ') that passed.', periods, dotCell));
    }
    tbody.appendChild(row('Overall (status)', 'Share of checks of this endpoint that met all conditions.', data && data.overall, pillCell));
    table.appendChild(tbody);
    var scroll = el('div', 'gatusmod-stats-scroll');
    scroll.appendChild(table);
    body.innerHTML = '';
    body.appendChild(scroll);
    body.appendChild(legend());
  }

  function refreshStatistics() {
    var current = stats;
    if (!current || document.visibilityState === 'hidden') {
      return;
    }
    fetchStatistics(current.key).then(function (data) {
      if (stats === current) {
        current.data = data;
        renderStatistics();
      }
    });
  }

  // A new card with the classes of an existing Gatus card: Card > (Header > h3) + Content. The content is the last child.
  function cardLike(template, titleText, name) {
    var header = template.firstElementChild;
    var title = header && header.firstElementChild;
    var content = header && header.nextElementSibling;
    var card = el('div', template.className);
    card.setAttribute('data-gatusmod', name);
    var h = el('div', header ? header.className : '');
    h.appendChild(el('h3', title ? title.className : '', titleText));
    card.appendChild(h);
    card.appendChild(el('div', content ? content.className : ''));
    return card;
  }

  // Replaces the response-time, uptime and health badge cards of the endpoint page with one statistics table.
  function applyStatistics() {
    var key = endpointKeyFromPath();
    if (!key) {
      stopStatistics();
      return;
    }
    if (stats && (stats.key !== key || !document.contains(stats.card))) {
      stopStatistics();
    }
    var uptimeCard = cardByTitle('Uptime Statistics');
    if (!uptimeCard) {
      // Still loading, or Gatus changed the page.
      return;
    }
    var healthCard = cardByTitle('Current Health');
    var grid = responseTimeGrid();
    if (!stats) {
      if (!uptimeCard.parentNode || !uptimeCard.querySelector('img[src*="/uptimes/"]')) {
        if (!applyStatistics.warned) {
          applyStatistics.warned = true;
          console.warn('[GatusMOD] statistics: the Uptime Statistics card has an unexpected layout, keeping the original cards');
        }
        return;
      }
      var card = cardLike(uptimeCard, 'Statistics', 'statistics');
      card.lastChild.setAttribute('data-gatusmod-stats-body', '');
      uptimeCard.parentNode.insertBefore(card, uptimeCard);
      stats = { key: key, card: card, data: null, showResponseTime: !!grid };
      stats.timer = setInterval(refreshStatistics, REFRESH_MS);
      renderStatistics();
      refreshStatistics();
    } else if (!!grid !== stats.showResponseTime) {
      stats.showResponseTime = !!grid;
      renderStatistics();
    }
    hide(uptimeCard, 'uptime-statistics');
    hide(healthCard, 'current-health');
    hide(grid, 'response-time-badges');
  }

  var CHECK_LABELS = {
    http: 'HTTP', tcp: 'TCP', udp: 'UDP', icmp: 'Ping (ICMP)', dns: 'DNS', ssh: 'SSH', tls: 'TLS', starttls: 'STARTTLS',
    ws: 'WebSocket', wss: 'WebSocket', grpc: 'gRPC', iperf3: 'iperf3', ntp: 'NTP', snmp: 'SNMP', smtp: 'SMTP',
    database: 'Database', push: 'Push', external: 'External'
  };

  // Shows the check type below the endpoint name: Gatus itself does not show it, and for the checks
  // GatusMOD runs (iperf3, NTP, ...) Gatus only knows them as external endpoints.
  function applyCheckType() {
    var key = endpointKeyFromPath();
    if (!key) {
      return;
    }
    // The header holds an h1 with the site title too; the endpoint name is the last h1 of the page.
    var headings = document.querySelectorAll('#app h1');
    var h1 = headings.length ? headings[headings.length - 1] : null;
    if (!h1) {
      return;
    }
    var group = h1.nextElementSibling && h1.nextElementSibling.tagName === 'P' &&
      h1.nextElementSibling.getAttribute('data-gatusmod') !== 'check-type' ? h1.nextElementSibling : null;
    var existing = document.querySelector('[data-gatusmod="check-type"]');
    if (existing && existing.getAttribute('data-key') === key && existing.previousElementSibling === (group || h1)) {
      return;
    }
    if (existing) {
      existing.parentNode.removeChild(existing);
    }
    var line = el('p', group ? group.className : 'gatusmod-check-type');
    line.setAttribute('data-gatusmod', 'check-type');
    line.setAttribute('data-key', key);
    line.style.display = 'none';
    (group || h1).insertAdjacentElement('afterend', line);
    fetchRelated(key).then(function (info) {
      if (!info || !info.type || !document.contains(line)) {
        return;
      }
      line.textContent = 'Check: ' + (CHECK_LABELS[info.type] || info.type) + (info.host ? ' · ' + info.host : '');
      line.style.display = '';
    });
  }

  var iperf = null; // { key, card, timer, none }

  function stopIPerf3() {
    if (!iperf) {
      return;
    }
    clearInterval(iperf.timer);
    if (iperf.card && iperf.card.parentNode) {
      iperf.card.parentNode.removeChild(iperf.card);
    }
    iperf = null;
  }

  function formatTime(iso) {
    var d = new Date(iso);
    return isNaN(d.getTime()) ? '' : d.toLocaleString();
  }

  // note is a line of text or a list of lines.
  function tile(label, value, note) {
    var t = el('div', 'gatusmod-iperf3-tile');
    t.appendChild(el('div', 'gatusmod-iperf3-label', label));
    t.appendChild(el('div', 'gatusmod-iperf3-value', value));
    (Array.isArray(note) ? note : [note]).forEach(function (line) {
      if (line) {
        t.appendChild(el('div', 'gatusmod-iperf3-note', line));
      }
    });
    return t;
  }

  var STATE_LABELS = { ready: 'Ready', busy: 'Busy (test running)', error: 'Error' };

  function renderIPerf3(data) {
    var body = iperf.card.lastChild;
    var grid = el('div', 'gatusmod-iperf3');
    var direction = data.reverse ? 'Download' : 'Upload';
    // With a rate limit the test shows whether data gets through, not the line speed.
    var label = (data.rate ? 'Data test' : 'Bandwidth') + ' (' + direction + ')';
    var setup = data.duration ? data.duration + (data.rate ? ' at max ' + data.rate : '') + (data.ip ? ', IPv' + data.ip : '') + ', every ' + data.bandwidthInterval : '';
    if (data.bandwidth !== undefined) {
      grid.appendChild(tile(label, data.bandwidth.toFixed(2) + ' Mbit/s', ['Measured ' + formatTime(data.bandwidthMeasured), '(' + setup + ')']));
    } else if (data.bandwidthFailed) {
      grid.appendChild(tile(label, 'Failed', ['Tested ' + formatTime(data.bandwidthMeasured), '(' + setup + ')']));
    } else {
      grid.appendChild(tile(label, '-', data.duration ? ['Not tested yet', '(' + setup + ')'] : 'No data test configured'));
    }
    var connected = data.checked ? (STATE_LABELS[data.state] || (data.success ? 'Reachable' : 'Unreachable')) : 'Not checked yet';
    grid.appendChild(tile('Server state', connected, data.checked ? 'Checked ' + formatTime(data.checked) : ''));
    grid.appendChild(tile('Port', data.port || '-', ''));
    grid.appendChild(tile('Connect time', data.responseTime !== undefined ? Math.round(data.responseTime) + ' ms' : '-', 'TCP connection to the iperf3 port'));
    body.innerHTML = '';
    body.appendChild(grid);
  }

  function refreshIPerf3() {
    var current = iperf;
    if (!current || current.none || document.visibilityState === 'hidden') {
      return;
    }
    fetchIPerf3(current.key).then(function (data) {
      if (iperf !== current) {
        return;
      }
      if (data === false) {
        current.none = true;
        current.card.style.display = 'none';
        return;
      }
      if (data) {
        current.card.style.display = '';
        renderIPerf3(data);
      }
    });
  }

  // Adds a card with the iperf3 results (bandwidth, server state, port) to the endpoint page of iperf3 checks.
  function applyIPerf3() {
    var key = endpointKeyFromPath();
    if (!key) {
      stopIPerf3();
      return;
    }
    if (iperf && (iperf.key !== key || !document.contains(iperf.card))) {
      stopIPerf3();
    }
    if (iperf) {
      return;
    }
    var anchor = document.querySelector('[data-gatusmod="statistics"]') || cardByTitle('Uptime Statistics');
    if (!anchor || !anchor.parentNode) {
      return;
    }
    var card = cardLike(anchor, 'iperf3', 'iperf3');
    card.style.display = 'none';
    anchor.parentNode.insertBefore(card, anchor);
    iperf = { key: key, card: card, none: false };
    iperf.timer = setInterval(refreshIPerf3, REFRESH_MS);
    refreshIPerf3();
  }

  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'visible') {
      refreshStatistics();
      refreshIPerf3();
    }
  });

  function apply() {
    applyHeading();
    applyFooter();
    applyStatistics();
    applyCheckType();
    applyIPerf3();
  }

  new MutationObserver(apply).observe(document.documentElement, { childList: true, subtree: true });
  document.addEventListener('DOMContentLoaded', apply);
})();
