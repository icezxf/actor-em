/* 字典 / MDCx 两个对照视图：标签页切换、服务端预生成的表格、按需加载简介 */
(function () {
  'use strict';

  var PAGE = 100;

  var views = ['actors', 'dict', 'mdcx'];
  var current = 'actors';

  var el = {
    tabs: document.getElementById('tabs'),
    q: document.getElementById('q'),
    count: document.getElementById('count'),
    actors: document.getElementById('view-actors'),
    dict: document.getElementById('view-dict'),
    mdcx: document.getElementById('view-mdcx'),
    dictSrc: document.getElementById('dict-src'),
    dictBody: document.getElementById('dict-body'),
    dictSentinel: document.getElementById('dict-sentinel'),
    dictEmpty: document.getElementById('dict-empty'),
    mdcxSort: document.getElementById('mdcx-sort'),
    mdcxBody: document.getElementById('mdcx-body'),
    mdcxSentinel: document.getElementById('mdcx-sentinel'),
    mdcxEmpty: document.getElementById('mdcx-empty')
  };

  var PLACEHOLDER = {
    actors: '搜名字 / 别名 / 标签，如 三上、桥本有菜、E罩杯',
    dict: '搜别名 / 日文名 / 中文名，如 三上、Mikami',
    mdcx: '搜日文名 / 中文名 / 罗马字，如 三上、Yua Mikami'
  };

  var data = { dict: null, mdcx: null };   // {fields, rows}
  var loaded = { dict: false, mdcx: false };
  var view = { dict: [], mdcx: [] };       // 过滤+排序后的行
  var shown = { dict: 0, mdcx: 0 };
  var bioCache = {};

  function norm(s) {
    return (s || '').toLowerCase();
  }

  function fmt(n) {
    return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  }

  function cell(text, cls) {
    var td = document.createElement('td');
    if (cls) td.className = cls;
    td.textContent = text == null ? '' : text;
    return td;
  }

  // 名字单元格：能对上演员库的就做成可点，点开详情（这就是「对照」的核心）
  function nameCell(text) {
    var td = document.createElement('td');
    if (!text) {
      td.className = 'muted';
      td.textContent = '—';
      return td;
    }
    var actor = window.ActorSite ? window.ActorSite.find(text) : null;
    if (actor) {
      var a = document.createElement('a');
      a.href = '#';
      a.className = 'name-link';
      a.textContent = text;
      a.addEventListener('click', function (e) {
        e.preventDefault();
        window.ActorSite.open(actor);
      });
      td.appendChild(a);
    } else {
      td.textContent = text;
    }
    return td;
  }

  function loadJSON(path) {
    return fetch(path).then(function (r) {
      if (!r.ok) throw new Error(path + ' ' + r.status);
      return r.json();
    });
  }

  // ---------- 字典 ----------

  function filterDict() {
    var q = norm(el.q.value.trim());
    var src = el.dictSrc.value;
    var rows = data.dict ? data.dict.rows : [];
    view.dict = rows.filter(function (r) {
      if (src && r[2] !== src) return false;
      if (!q) return true;
      return norm(r[0]).indexOf(q) >= 0 || norm(r[1]).indexOf(q) >= 0 ||
        norm(r[3]).indexOf(q) >= 0;
    });
    shown.dict = 0;
    el.dictBody.textContent = '';
    el.dictEmpty.hidden = view.dict.length > 0;
    el.count.textContent = fmt(view.dict.length) + ' 条字典 / ' + fmt(rows.length) + ' 条';
    renderDict();
  }

  function renderDict() {
    if (shown.dict >= view.dict.length) return;
    var frag = document.createDocumentFragment();
    var end = Math.min(shown.dict + PAGE, view.dict.length);
    for (var i = shown.dict; i < end; i++) {
      var r = view.dict[i];
      var tr = document.createElement('tr');
      tr.appendChild(nameCell(r[0]));
      tr.appendChild(cell(r[1]));
      tr.appendChild(cell(r[2], 'tag'));
      tr.appendChild(cell((r[3] || '').split('|').filter(Boolean).join(' · '), 'muted'));
      frag.appendChild(tr);
    }
    el.dictBody.appendChild(frag);
    shown.dict = end;
  }

  // ---------- MDCx ----------

  function mdcxKey(r) {
    return [r[0], r[1], r[2], r[3]].join(' ').toLowerCase();
  }

  function filterMdcx() {
    var q = norm(el.q.value.trim());
    var rows = data.mdcx ? data.mdcx.rows : [];
    var out = [];
    for (var i = 0; i < rows.length; i++) {
      if (!q || mdcxKey(rows[i]).indexOf(q) >= 0) out.push({ r: rows[i], idx: i });
    }
    var sort = el.mdcxSort.value;
    if (sort === 'career') {
      out.sort(function (x, y) { return (y.r[12] || 0) - (x.r[12] || 0); });
    } else if (sort === 'height') {
      out.sort(function (x, y) { return (y.r[7] || 0) - (x.r[7] || 0); });
    } else {
      out.sort(function (x, y) { return x.r[0].localeCompare(y.r[0], 'ja'); });
    }
    view.mdcx = out;
    shown.mdcx = 0;
    el.mdcxBody.textContent = '';
    el.mdcxEmpty.hidden = out.length > 0;
    el.count.textContent = fmt(out.length) + ' 条档案 / ' + fmt(rows.length) + ' 条';
    renderMdcx();
  }

  function meas(r) {
    var parts = [];
    if (r[9]) parts.push(r[9]);
    if (r[10]) parts.push(r[10]);
    if (r[11]) parts.push(r[11]);
    return parts.length ? parts.join('-') : '';
  }

  function renderMdcx() {
    if (shown.mdcx >= view.mdcx.length) return;
    var frag = document.createDocumentFragment();
    var end = Math.min(shown.mdcx + PAGE, view.mdcx.length);
    for (var i = shown.mdcx; i < end; i++) {
      var item = view.mdcx[i];
      var r = item.r;
      var tr = document.createElement('tr');
      tr.className = 'clickable';
      tr.appendChild(nameCell(r[0]));
      tr.appendChild(cell(r[1]));
      tr.appendChild(cell(r[4]));
      tr.appendChild(cell([r[5], r[6]].filter(Boolean).join(' · ')));
      tr.appendChild(cell(r[7] ? r[7] + '' : ''));
      tr.appendChild(cell(r[8]));
      tr.appendChild(cell(meas(r)));
      tr.appendChild(cell(r[12] ? r[12] + '' : ''));
      tr.appendChild(cell((r[13] || '').split(/[|,、]/).filter(Boolean).slice(0, 4).join(' · '), 'muted'));
      tr.dataset.idx = item.idx;
      frag.appendChild(tr);
    }
    el.mdcxBody.appendChild(frag);
    shown.mdcx = end;
  }

  function toggleMdcxRow(tr) {
    var next = tr.nextElementSibling;
    if (next && next.classList.contains('expand')) {
      next.parentNode.removeChild(next);
      return;
    }
    var idx = tr.dataset.idx;
    var r = data.mdcx.rows[idx] || [];
    var shard = idx % 8;

    var row = document.createElement('tr');
    row.className = 'expand';
    var td = document.createElement('td');
    td.colSpan = 9;
    td.textContent = '简介加载中…';
    row.appendChild(td);
    tr.parentNode.insertBefore(row, tr.nextSibling);

    var show = function (bio) {
      td.textContent = '';
      var dl = document.createElement('dl');
      dl.className = 'kv';
      [['罗马字', r[2]], ['别名', r[3]], ['生日', r[4]], ['出生地', [r[5], r[6]].filter(Boolean).join(' · ')],
        ['三围', meas(r)], ['出道', r[12]], ['标签', r[13]], ['简介', bio || '（无）']]
        .forEach(function (pair) {
          if (!pair[1]) return;
          var dt = document.createElement('dt');
          dt.textContent = pair[0];
          var dd = document.createElement('dd');
          dd.textContent = pair[1];
          dl.appendChild(dt);
          dl.appendChild(dd);
        });
      td.appendChild(dl);
    };

    if (bioCache[shard]) {
      show(bioCache[shard][String(idx)] || '');
      return;
    }
    loadJSON('data/mdcx_bio/' + shard + '.json')
      .catch(function () { return {}; })
      .then(function (d) {
        bioCache[shard] = d;
        show(d[String(idx)] || '');
      });
  }

  // ---------- 切换 ----------

  function show(name) {
    if (views.indexOf(name) < 0) name = 'actors';
    current = name;
    Array.prototype.forEach.call(el.tabs.children, function (b) {
      b.classList.toggle('on', b.dataset.view === name);
    });
    el.actors.hidden = name !== 'actors';
    el.dict.hidden = name !== 'dict';
    el.mdcx.hidden = name !== 'mdcx';
    el.q.placeholder = PLACEHOLDER[name];

    if (name === 'actors') {
      if (window.ActorSite && window.ActorSite.refresh) window.ActorSite.refresh();
      return;
    }
    if (!loaded[name]) {
      el.count.textContent = '加载中…';
      loadJSON('data/' + name + '.json').then(function (d) {
        data[name] = d;
        loaded[name] = true;
        if (name === 'dict') filterDict(); else filterMdcx();
      }).catch(function (err) {
        el.count.textContent = '加载失败';
        (name === 'dict' ? el.dictEmpty : el.mdcxEmpty).hidden = false;
        (name === 'dict' ? el.dictEmpty : el.mdcxEmpty).textContent = '数据加载失败：' + err.message;
      });
      return;
    }
    if (name === 'dict') filterDict(); else filterMdcx();
  }

  el.tabs.addEventListener('click', function (e) {
    var b = e.target.closest('button[data-view]');
    if (b) show(b.dataset.view);
  });

  var timer = null;
  el.q.addEventListener('input', function () {
    if (current === 'actors') return;   // 演员视图由 app.js 自己处理
    clearTimeout(timer);
    timer = setTimeout(function () {
      if (current === 'dict') filterDict(); else filterMdcx();
    }, 120);
  });

  el.dictSrc.addEventListener('change', filterDict);
  el.mdcxSort.addEventListener('change', filterMdcx);

  el.mdcxBody.addEventListener('click', function (e) {
    if (e.target.closest('a')) return;  // 点名字是跳详情，不展开
    var tr = e.target.closest('tr.clickable');
    if (tr) toggleMdcxRow(tr);
  });

  new IntersectionObserver(function (entries) {
    if (entries[0].isIntersecting) renderDict();
  }, { rootMargin: '400px' }).observe(el.dictSentinel);

  new IntersectionObserver(function (entries) {
    if (entries[0].isIntersecting) renderMdcx();
  }, { rootMargin: '400px' }).observe(el.mdcxSentinel);

  // 演员详情里的「MDCx 档案 / 字典条目」按钮走这里
  window.SiteTables = {
    jump: function (name, term) {
      show(name);
      if (term) {
        el.q.value = term;
        setTimeout(function () {
          if (name === 'dict') filterDict(); else filterMdcx();
        }, 0);
      }
    }
  };
})();
