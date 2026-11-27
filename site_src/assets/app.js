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
  // 欧美双字母杯（AA/DD/JJ）排序时排在同名单字母之后
  function cupRank(c) {
    if (!c) return -1;
    if (c.length === 2 && c[0] === c[1]) {
      var i = CUPS.indexOf(c[0]);
      return i < 0 ? -1 : i + 0.5;
    }
    return CUPS.indexOf(c);
  }
  var PAGE = 60;

  // country 字段存的是 ISO 码（JP/US/TW…），页面上统一显示中文名。
  // 没列出来的码直接显示原码，别假装认识。
  var COUNTRY_NAMES = {
    JP: '日本', US: '美国', KR: '韩国', CN: '中国', TW: '中国台湾', HK: '中国香港',
    MO: '中国澳门', CA: '加拿大', GB: '英国', UK: '英国', AU: '澳大利亚', FR: '法国',
    DE: '德国', RU: '俄罗斯', IT: '意大利', ES: '西班牙', BR: '巴西', PH: '菲律宾',
    TH: '泰国', VN: '越南', SG: '新加坡', MY: '马来西亚', ID: '印度尼西亚',
    NZ: '新西兰', NL: '荷兰', SE: '瑞典', NO: '挪威', DK: '丹麦', FI: '芬兰',
    CH: '瑞士', AT: '奥地利', BE: '比利时', PL: '波兰', CZ: '捷克', HU: '匈牙利',
    RO: '罗马尼亚', UA: '乌克兰', TR: '土耳其', MX: '墨西哥', AR: '阿根廷',
    CO: '哥伦比亚', CL: '智利', PE: '秘鲁', IN: '印度', IL: '以色列', ZA: '南非'
  };

  var el = {
    q: document.getElementById('q'),
    source: document.getElementById('source'),
    cup: document.getElementById('cup'),
    height: document.getElementById('height'),
    region: document.getElementById('region'),
    country: document.getElementById('country'),
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
    mXref: document.getElementById('m-xref'),
    mPickAvatar: document.getElementById('m-pick-avatar'),
    mAvatarGrid: document.getElementById('m-avatar-grid'),
    mAvatarNote: document.getElementById('m-avatar-note')
  };

  // 默认：只显示日系、按人气排序（用户希望打开就看到最热的日系演员）
  var state = { q: '', source: 'jp', cup: '', height: '', region: '', country: '', sort: 'popularity', tag: '', onlyAvatar: false };
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
      tags: tags, aliases: aliases, place: r[11], country: (r[12] || '').toUpperCase(),
      src: r[13] || 'jp', rank: r[14] || 0,
      search: (r[1] + ' ' + r[2] + ' ' + aliases.join(' ') + ' ' + tags.join(' ')).toLowerCase()
    };
  }

  // 详情分片：简介和主页链接单独按需拉，首屏索引小一半
  var DETAIL_SHARDS = 256;
  var detailCache = {};

  // gfriends 候选图：按演员名哈希取分片（每片几十 KB），只在前端点「换头像」时拉。
  // 哈希算法必须与 scripts/build_gfriends_index.py 的 shard_of() 完全一致。
  var GFRIENDS_SHARDS = 128;
  var gfriendsCache = {};

  function gfriendsShard(name) {
    var s = 0;
    for (var i = 0; i < name.length; i++) s += name.charCodeAt(i);
    return s % GFRIENDS_SHARDS;
  }

  function loadGfriends(name, cb) {
    var shard = gfriendsShard(name);
    if (gfriendsCache[shard]) {
      cb(gfriendsCache[shard][name] || []);
      return;
    }
    SiteLock.fetchData('data/gfriends/' + shard + '.json')
      .catch(function () { return {}; })
      .then(function (data) {
        gfriendsCache[shard] = data;
        cb(data[name] || []);
      });
  }

  // StashDB / JavStash 候选图：分片文件 key 是 provider id（uuid），
  // 按 id 字符码之和 % 128 分片（与 scripts/build_avatar_index.py 一致）。
  var SITE_SHARDS = 128;
  var siteImgCache = {};

  function idShard(pid) {
    var s = 0;
    for (var i = 0; i < pid.length; i++) s += pid.charCodeAt(i);
    return s % SITE_SHARDS;
  }

  function loadSiteImages(site, pid, cb) {
    var dir = site === 'stashdb' ? 'avatars_stash' : site === 'javstash' ? 'avatars_jav' : '';
    if (!dir || !pid) { cb([]); return; }
    var shard = idShard(pid);
    var ck = dir + '/' + shard;
    if (siteImgCache[ck]) { cb(siteImgCache[ck][pid] || []); return; }
    SiteLock.fetchData('data/' + dir + '/' + shard + '.json')
      .catch(function () { return {}; })
      .then(function (data) {
        siteImgCache[ck] = data;
        cb(data[pid] || []);
      });
  }

  // 选图：展示候选网格，点一张后生成 GitHub issue 链接（含 key + 图片 URL）。
  // 仓库 owner 提交 issue 后，workflow 会写回 avatar_choices.json。
  var REPO_URL = 'https://github.com/icezxf/actor-em';

  function renderAvatarPicker(a, key) {
    var grid = el.mAvatarGrid;
    grid.textContent = '';
    el.mAvatarNote.textContent = '加载候选图…';

    // 三个来源都拉：gfriends（按日文名）+ StashDB/JavStash（按 key 里的 uuid）
    var names = [a.name, a.orig].concat(a.aliases || []).filter(function (n, i, arr) {
      return n && arr.indexOf(n) === i;
    });
    var parts = (key || '').split(':');
    var site = parts[0] || '';
    var pid = parts[1] || '';
    var found = [];
    var pending = names.length + 1;

    function add(urls) {
      urls.forEach(function (u) { if (found.indexOf(u) < 0) found.push(u); });
      if (--pending <= 0) drawGrid(found);
    }

    names.forEach(function (n) { loadGfriends(n, add); });
    loadSiteImages(site, pid, add);

    function drawGrid(urls) {
      if (!urls.length) {
        el.mAvatarNote.textContent = '没找到候选图（gfriends 与数据源都没有）。';
        return;
      }
      el.mAvatarNote.textContent = '共 ' + urls.length + ' 张（gfriends + 数据源原图）。点一张选中，再点下方按钮直接换上。';
      var selectedUrl = '';
      var pickBtn = document.createElement('button');
      pickBtn.type = 'button';
      pickBtn.className = 'pick-submit';
      pickBtn.textContent = '用这张 → 提交';
      pickBtn.disabled = true;
      pickBtn.addEventListener('click', function () {
        if (!selectedUrl) return;
        if (!key) {
          el.mAvatarNote.textContent = '缺少主键，无法提交（详情数据可能未加载完）';
          return;
        }
        pickBtn.disabled = true;
        pickBtn.textContent = '提交中…';
        SiteLock.pickAvatar(key, selectedUrl).then(function () {
          pickBtn.textContent = '已提交 ✓';
          el.mAvatarNote.textContent = '已提交，约 1~2 分钟后页面会更新（后台正在写库并重建）。';
        }).catch(function (err) {
          pickBtn.disabled = false;
          pickBtn.textContent = '用这张 → 提交';
          el.mAvatarNote.textContent = '提交失败：' + (err && err.message ? err.message : err);
        });
      });

      urls.forEach(function (u, i) {
        var cell = document.createElement('button');
        cell.type = 'button';
        cell.className = 'avatar-cell';
        cell.title = '第 ' + (i + 1) + ' 张';
        var img = document.createElement('img');
        img.loading = 'lazy';
        img.src = u;
        img.alt = a.name + ' 候选 ' + (i + 1);
        img.addEventListener('error', function () { cell.classList.add('broken'); });
        cell.appendChild(img);
        cell.addEventListener('click', function () {
          Array.prototype.forEach.call(grid.querySelectorAll('.avatar-cell'), function (n) {
            n.classList.remove('on');
          });
          cell.classList.add('on');
          selectedUrl = u;
          pickBtn.disabled = false;
        });
        grid.appendChild(cell);
      });
      grid.appendChild(pickBtn);
    }
  }

  function loadDetail(id, cb) {
    var shard = id % DETAIL_SHARDS;
    if (detailCache[shard]) {
      cb(detailCache[shard][String(id)] || null);
      return;
    }
    SiteLock.fetchData('data/detail/' + shard + '.json')
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

  function year(v) {
    if (!v) return 0;
    var n = parseInt(String(v).slice(0, 4), 10);
    return isNaN(n) ? 0 : n;
  }

  function countryLabel(code) {
    var c = (code || '').toUpperCase();
    if (!c) return '';
    return COUNTRY_NAMES[c] || code;
  }

  // 判定「日本人」：优先看国家码；没有码时看定稿后的出生地（都道府县都会补成「日本·东京都」）。
  // 两样都没有的记录算「不确定」，只在「全部」里出现，不塞进任何一边。
  function isJapan(a) {
    if (a.country) return a.country === 'JP';
    return /^日本/.test(a.place || '');
  }

  function inRegion(a) {
    if (!state.region) return true;
    if (state.region === 'jp') return isJapan(a);
    // 其他：必须有明确国家码，且不是日本（未知的不算「其他」）
    if (!a.country || a.country === 'JP') return false;
    if (state.country && a.country !== state.country) return false;
    return true;
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
      if (state.source && a.src !== state.source) return false;
      if (state.onlyAvatar && !a.avatar) return false;
      if (state.cup && a.cup !== state.cup) return false;
      if (state.tag && a.tags.indexOf(state.tag) < 0) return false;
      if (!inHeight(a.height)) return false;
      if (!inRegion(a)) return false;
      if (q && a.search.indexOf(q) < 0) return false;
      return true;
    });

    var cmp = {
      name: function (x, y) { return x.name.localeCompare(y.name, 'ja'); },
      birth: function (x, y) { return year(y.birth) - year(x.birth); },
      height: function (x, y) { return (y.height || 0) - (x.height || 0); },
      cup: function (x, y) { return cupRank(y.cup) - cupRank(x.cup); },
      debut: function (x, y) { return (y.career || 0) - (x.career || 0); },
      // 人气：有排名的按 rank 升序（小=更火），没排名的（0）沉底
      popularity: function (x, y) {
        var rx = x.rank || 1e9, ry = y.rank || 1e9;
        return rx - ry || x.name.localeCompare(y.name, 'ja');
      }
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
    if (a.src === 'eu') {
      var euTag = document.createElement('span');
      euTag.className = 'srctag';
      euTag.textContent = '欧美';
      name.appendChild(euTag);
    }

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
    // 选图区域每次重开都清空，点「换头像」才加载
    el.mAvatarGrid.textContent = '';
    el.mAvatarNote.textContent = '点「换头像」查看候选图。';
    var detailKey = '';
    el.mPickAvatar.onclick = function () { renderAvatarPicker(a, detailKey); };
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
      detailKey = (d && d[3]) || '';     // 选图写回要用的数据库主键
      var bio = (d && d[0]) ? d[0] : '';
      el.mBio.textContent = bio || '暂无简介。';
      // 英文简介标个来源，免得和中文口径混淆
      var lang = document.getElementById('m-bio-lang');
      if (lang) lang.textContent = bio && /^[\x00-\x7F\s]+$/.test(bio.slice(0, 200)) ? 'English (TPDB)' : '';
      el.mLinks.textContent = '';
      // 新分片：d[2] 是 [[站点名, url], ...]，按 javstash → stashdb → tpdb 排
      var links = (d && d[2]) || [];
      if (!links.length) {
        // 兼容旧分片：只有 home 一条
        var home = d && d[1];
        if (home) links = [['主页', home]];
      }
      links.forEach(function (pair) {
        var link = document.createElement('a');
        link.href = pair[1];
        link.target = '_blank';
        link.rel = 'noopener noreferrer';
        link.textContent = pair[0] + ' ↗';
        el.mLinks.appendChild(link);
      });
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

  // 「其他」里的具体国家：按人数降序，最多列 30 个（再往后都是个位数，翻着累）
  function buildCountryFilter() {
    var counts = {};
    all.forEach(function (a) {
      if (!a.country || a.country === 'JP') return;
      counts[a.country] = (counts[a.country] || 0) + 1;
    });
    var codes = Object.keys(counts).sort(function (x, y) {
      return counts[y] - counts[x] || countryLabel(x).localeCompare(countryLabel(y), 'zh');
    });
    el.country.textContent = '';
    var total = codes.reduce(function (n, c) { return n + counts[c]; }, 0);
    var all_ = document.createElement('option');
    all_.value = '';
    all_.textContent = '全部国家（' + total + '）';
    el.country.appendChild(all_);
    codes.slice(0, 30).forEach(function (c) {
      var o = document.createElement('option');
      o.value = c;
      o.textContent = countryLabel(c) + '（' + counts[c] + '）';
      el.country.appendChild(o);
    });
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
    buildCountryFilter();
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
  el.source.addEventListener('change', function () { state.source = el.source.value; reset(); });
  el.cup.addEventListener('change', function () { state.cup = el.cup.value; reset(); });
  el.height.addEventListener('change', function () { state.height = el.height.value; reset(); });
  el.region.addEventListener('change', function () {
    state.region = el.region.value;
    var other = state.region === 'other';
    el.country.hidden = !other;
    if (!other) {
      state.country = '';
      el.country.value = '';
    }
    reset();
  });
  el.country.addEventListener('change', function () { state.country = el.country.value; reset(); });
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

  function boot() {
    Promise.all([
      SiteLock.fetchData('data/actors.json'),
      SiteLock.fetchData('data/meta.json').catch(function () { return {}; })
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
  }

  // 锁屏：先试上次会话的密码，不行就等用户输入
  var lockEl = document.getElementById('lock');
  var lockForm = document.getElementById('lock-form');
  var lockPw = document.getElementById('lock-pw');
  var lockMsg = document.getElementById('lock-msg');

  function startLocked() {
    lockEl.hidden = false;
    lockMsg.textContent = '';
    lockForm.addEventListener('submit', function (e) {
      e.preventDefault();
      lockMsg.textContent = '验证中…';
      SiteLock.unlock(lockPw.value).then(function (ok) {
        if (ok) {
          lockEl.hidden = true;
          boot();
        } else {
          lockMsg.textContent = '密码错误';
          lockPw.select();
        }
      });
    });
    lockPw.focus();
  }

  SiteLock.tryRestore().then(function (ok) {
    if (ok) { lockEl.hidden = true; boot(); }
    else startLocked();
  });
})();
