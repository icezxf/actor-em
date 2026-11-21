/* 演员库静态站：一次拉完精简索引，前端做搜索/筛选/无限滚动 */
(function () {
  'use strict';

  var AVATAR_PREFIX = {
    '~g/': 'https://cdn.jsdelivr.net/gh/gfriends/gfriends@master/Content/',
    '~t/': 'https://cdn.theporndb.net/',
    '~j/': 'https://javstash.org/',
    '~s/': 'https://stashdb.org/'
  };
  var CUPS = 'ABCDEFGHIJKLMNO'.split('');
  var PAGE = 60;

  var el = {
    q: document.getElementById('q'),
    cup: document.getElementById('cup'),
    height: document.getElementById('height'),
    sort: document.getElementById('sort'),
    onlyAvatar: document.getElementById('onlyAvatar'),
    tagbar: document.getElementById('tagbar'),
    grid: document.getElementById('grid'),
    sentinel: document.getElementById('sentinel'),
    empty: document.getElementById('empty'),
    count: document.getElementById('count'),
    generated: document.getElementById('generated'),
    theme: document.getElementById('theme'),
    modal: document.getElementById('modal'),
    mClose: document.getElementById('m-close'),
    mImg: document.getElementById('m-img'),
    mInitial: document.getElementById('m-initial'),
    mName: document.getElementById('m-name'),
    mOrig: document.getElementById('m-orig'),
    mFacts: document.getElementById('m-facts'),
    mTags: document.getElementById('m-tags'),
    mAliases: document.getElementById('m-aliases'),
    mBio: document.getElementById('m-bio'),
    mLinks: document.getElementById('m-links'),
    mXref: document.getElementById('m-xref')
  };

  var state = { q: '', cup: '', height: '', sort: 'name', tag: '', onlyAvatar: false };
  var all = [];
  var view = [];
  var shown = 0;
  var detailToken = 0;

  function avatarUrl(v) {
    if (!v) return '';
    var key = v.slice(0, 3);
    return AVATAR_PREFIX[key] ? AVATAR_PREFIX[key] + v.slice(3) : v;
  }

  function decode(r) {
    var tags = r[9] ? r[9].split('|') : [];
    var aliases = r[10] ? r[10].split('|') : [];
    return {
      id: r[0], name: r[1], orig: r[2], avatar: avatarUrl(r[3]),
      birth: r[4], height: r[5], cup: r[6], meas: r[7], career: r[8],
      tags: tags, aliases: aliases, place: r[11],
      search: (r[1] + ' ' + r[2] + ' ' + aliases.join(' ') + ' ' + tags.join(' ')).toLowerCase()
    };
  }

  // 详情分片：简介和主页链接单独按需拉，首屏索引小一半
  var DETAIL_SHARDS = 16;
  var detailCache = {};

  function loadDetail(id, cb) {
    var shard = id % DETAIL_SHARDS;
    if (detailCache[shard]) {
      cb(detailCache[shard][String(id)] || null);
      return;
    }
    fetch('data/detail/' + shard + '.json')
      .then(function (r) { return r.ok ? r.json() : {}; })
      .catch(function () { return {}; })
      .then(function (data) {
        detailCache[shard] = data;
        cb(data[String(id)] || null);
      });
  }

  // 名字索引：字典页 / MDCx 页点名字时能跳回演员详情（对照用）
  var nameIndex = {};

  function normName(s) {
    return (s || '').toLowerCase().replace(/[\s\u3000·・._\-]+/g, '');
  }

  function buildNameIndex() {
    all.forEach(function (a) {
      [a.name, a.orig].concat(a.aliases).forEach(function (n) {
        var k = normName(n);
        if (k && !nameIndex[k]) nameIndex[k] = a;
      });
    });
  }

  function cupRank(c) {
    var i = CUPS.indexOf(c);
    return i < 0 ? -1 : i;
  }

  function year(v) {
    if (!v) return 0;
    var n = parseInt(String(v).slice(0, 4), 10);
    return isNaN(n) ? 0 : n;
  }

  function inHeight(h) {
    var r = state.height;
    if (!r) return true;
    if (!h) return false;
    if (r === '-150') return h < 150;
    if (r === '170-') return h >= 170;
    var p = r.split('-');
    return h >= parseInt(p[0], 10) && h < parseInt(p[1], 10);
  }

  function current() {
    var q = state.q.trim().toLowerCase();
    var out = all.filter(function (a) {
      if (state.onlyAvatar && !a.avatar) return false;
      if (state.cup && a.cup !== state.cup) return false;
      if (state.tag && a.tags.indexOf(state.tag) < 0) return false;
      if (!inHeight(a.height)) return false;
      if (q && a.search.indexOf(q) < 0) return false;
      return true;
    });

    var cmp = {
      name: function (x, y) { return x.name.localeCompare(y.name, 'ja'); },
      birth: function (x, y) { return year(y.birth) - year(x.birth); },
      height: function (x, y) { return (y.height || 0) - (x.height || 0); },
      cup: function (x, y) { return cupRank(y.cup) - cupRank(x.cup); },
      debut: function (x, y) { return (y.career || 0) - (x.career || 0); }
    }[state.sort];
    out.sort(cmp);
    return out;
  }

  function badge(text, cls) {
    var s = document.createElement('span');
    s.className = 'badge' + (cls ? ' ' + cls : '');
    s.textContent = text;
    return s;
  }

  function card(a) {
    var node = document.createElement('article');
    node.className = 'card';
    node.tabIndex = 0;
    node.dataset.id = a.id;

    var thumb = document.createElement('div');
    thumb.className = 'thumb';
    var initial = document.createElement('span');
    initial.className = 'initial';
    initial.textContent = (a.name || '?').slice(0, 1);
    thumb.appendChild(initial);
    if (a.avatar) {
      var img = document.createElement('img');
      img.loading = 'lazy';
      img.alt = a.name;
      img.src = a.avatar;
      img.addEventListener('error', function () { img.classList.add('hide'); });
      thumb.appendChild(img);
    }

    var info = document.createElement('div');
    info.className = 'info';

    var name = document.createElement('div');
    name.className = 'name';
    name.textContent = a.name;
    info.appendChild(name);

    if (a.orig && a.orig !== a.name) {
      var sub = document.createElement('div');
      sub.className = 'sub';
      sub.textContent = a.orig;
      info.appendChild(sub);
    }

    var meta = document.createElement('div');
    meta.className = 'meta';
    if (a.cup) meta.appendChild(badge(a.cup + '罩杯', 'cup'));
    if (a.height) meta.appendChild(badge(a.height + 'cm'));
    if (a.birth) meta.appendChild(badge(String(year(a.birth))));
    if (meta.children.length) info.appendChild(meta);

    var tags = a.tags.filter(function (t) { return /罩杯$/.test(t) === false; }).slice(0, 3);
    if (tags.length) {
      var chips = document.createElement('div');
      chips.className = 'chips';
      tags.forEach(function (t) {
        var s = document.createElement('span');
        s.textContent = t;
        chips.appendChild(s);
      });
      info.appendChild(chips);
    }

    node.appendChild(thumb);
    node.appendChild(info);
    node.addEventListener('click', function () { openDetail(a); });
    node.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        openDetail(a);
      }
    });
    return node;
  }

  function renderMore() {
    if (shown >= view.length) return;
    var frag = document.createDocumentFragment();
    var end = Math.min(shown + PAGE, view.length);
    for (var i = shown; i < end; i++) frag.appendChild(card(view[i]));
    el.grid.appendChild(frag);
    shown = end;
    if (shown >= view.length) observer.unobserve(el.sentinel);
  }

  function reset() {
    view = current();
    shown = 0;
    el.grid.textContent = '';
    el.empty.hidden = view.length > 0;
    el.count.textContent = view.length + ' / ' + all.length + ' 人';
    observer.observe(el.sentinel);
    renderMore();
  }

  var observer = new IntersectionObserver(function (entries) {
    if (entries[0].isIntersecting) renderMore();
  }, { rootMargin: '600px' });

  function fact(text, cls) { return badge(text, cls); }

  function openDetail(a) {
    detailToken++;
    var token = detailToken;
    el.mName.textContent = a.name;
    el.mOrig.textContent = a.orig && a.orig !== a.name ? a.orig : '';
    el.mInitial.textContent = (a.name || '?').slice(0, 1);
    el.mImg.classList.remove('hide');
    if (a.avatar) {
      el.mImg.src = a.avatar;
      el.mImg.alt = a.name;
      el.mImg.style.display = '';
    } else {
      el.mImg.removeAttribute('src');
      el.mImg.style.display = 'none';
    }

    el.mFacts.textContent = '';
    if (a.birth) el.mFacts.appendChild(fact('生日 ' + a.birth));
    if (a.height) el.mFacts.appendChild(fact(a.height + ' cm'));
    if (a.cup) el.mFacts.appendChild(fact(a.cup + ' 罩杯', 'cup'));
    if (a.meas) el.mFacts.appendChild(fact(a.meas));
    if (a.career) el.mFacts.appendChild(fact(a.career + ' 出道'));
    if (a.place) el.mFacts.appendChild(fact(a.place));

    el.mTags.textContent = '';
    a.tags.forEach(function (t) {
      var s = document.createElement('span');
      s.textContent = t;
      el.mTags.appendChild(s);
    });

    el.mAliases.textContent = a.aliases.length ? a.aliases.join(' · ') : '—';

    // 简介和主页在分片里，先占位再填，避免弹层卡住
    el.mBio.textContent = '加载中…';
    el.mLinks.textContent = '';
    loadDetail(a.id, function (d) {
      if (token !== detailToken) return; // 弹层已经换成别人了，别覆盖
      el.mBio.textContent = (d && d[0]) ? d[0] : '暂无中文简介。';
      el.mLinks.textContent = '';
      var home = d && d[1];
      if (!home) return;
      var link = document.createElement('a');
      link.href = home;
      link.target = '_blank';
      link.rel = 'noopener noreferrer';
      link.textContent = home.indexOf('javstash') >= 0 ? 'JavStash 主页' : 'TPDB 主页';
      el.mLinks.appendChild(link);
    });

    // 对照入口：跳到字典 / MDCx 表，按日文原名搜
    el.mXref.textContent = '';
    [['mdcx', 'MDCx 档案'], ['dict', '字典条目']].forEach(function (pair) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'linkish';
      b.textContent = pair[1] + ' ↗';
      b.addEventListener('click', function () {
        closeDetail();
        if (window.SiteTables) window.SiteTables.jump(pair[0], a.orig || a.name);
      });
      el.mXref.appendChild(b);
    });

    el.modal.hidden = false;
    document.body.style.overflow = 'hidden';
  }

  function closeDetail() {
    el.modal.hidden = true;
    document.body.style.overflow = '';
  }

  function buildFilters() {
    var cupCount = {};
    var tagCount = {};
    all.forEach(function (a) {
      if (a.cup) cupCount[a.cup] = (cupCount[a.cup] || 0) + 1;
      a.tags.forEach(function (t) {
        if (/罩杯$/.test(t)) return;
        tagCount[t] = (tagCount[t] || 0) + 1;
      });
    });

    CUPS.forEach(function (c) {
      if (!cupCount[c]) return;
      var o = document.createElement('option');
      o.value = c;
      o.textContent = c + ' 罩杯（' + cupCount[c] + '）';
      el.cup.appendChild(o);
    });

    Object.keys(tagCount).sort(function (x, y) { return tagCount[y] - tagCount[x]; }).slice(0, 12)
      .forEach(function (t) {
        var b = document.createElement('button');
        b.type = 'button';
        b.textContent = t;
        b.dataset.tag = t;
        b.addEventListener('click', function () {
          if (state.tag === t) {
            state.tag = '';
            b.classList.remove('on');
          } else {
            state.tag = t;
            Array.prototype.forEach.call(el.tagbar.children, function (n) { n.classList.remove('on'); });
            b.classList.add('on');
          }
          reset();
        });
        el.tagbar.appendChild(b);
      });
  }

  function applyTheme(mode) {
    if (mode) document.documentElement.dataset.theme = mode;
    else delete document.documentElement.dataset.theme;
  }

  function initTheme() {
    var saved = localStorage.getItem('theme');
    applyTheme(saved);
    el.theme.addEventListener('click', function () {
      var isDark = document.documentElement.dataset.theme === 'dark' ||
        (!document.documentElement.dataset.theme &&
          window.matchMedia('(prefers-color-scheme: dark)').matches);
      var next = isDark ? 'light' : 'dark';
      localStorage.setItem('theme', next);
      applyTheme(next);
    });
  }

  var timer = null;
  el.q.addEventListener('input', function () {
    clearTimeout(timer);
    timer = setTimeout(function () {
      state.q = el.q.value;
      reset();
    }, 120);
  });
  el.cup.addEventListener('change', function () { state.cup = el.cup.value; reset(); });
  el.height.addEventListener('change', function () { state.height = el.height.value; reset(); });
  el.sort.addEventListener('change', function () { state.sort = el.sort.value; reset(); });
  el.onlyAvatar.addEventListener('change', function () { state.onlyAvatar = el.onlyAvatar.checked; reset(); });
  el.mClose.addEventListener('click', closeDetail);
  el.modal.addEventListener('click', function (e) { if (e.target === el.modal) closeDetail(); });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape') closeDetail(); });

  initTheme();

  // 给 tables.js 用：按名字找演员、打开详情
  window.ActorSite = {
    find: function (n) { return nameIndex[normName(n)] || null; },
    open: function (a) { if (a) openDetail(a); },
    refresh: function () { if (all.length) reset(); }
  };

  Promise.all([
    fetch('data/actors.json').then(function (r) { return r.json(); }),
    fetch('data/meta.json').then(function (r) { return r.json(); }).catch(function () { return {}; })
  ]).then(function (res) {
    all = res[0].rows.map(decode);
    buildNameIndex();
    buildFilters();
    reset();
    if (res[1] && res[1].generated_at) {
      el.generated.textContent = '更新于 ' + res[1].generated_at.replace('T', ' ').replace('Z', ' UTC');
    }
  }).catch(function (err) {
    el.count.textContent = '加载失败';
    el.empty.hidden = false;
    el.empty.textContent = '数据加载失败：' + err.message;
  });
})();
