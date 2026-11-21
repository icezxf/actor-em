# actor-em

给 Emby 用的演员元数据库。以 **MDCx 的演员档案为主干**，用 JavStash / TPDB / 维基 / dramaface /
映人录 / 影中人 / jav-idol-db / theactorbase / gfriends 把每个演员补齐到 Emby 的 12 项标准，
产出一份扁平数据 + 一份 Excel。

任何程序读 `actors.json` 就能拿到全部演员信息，不依赖媒体服务器，也不需要数据库。

```
工作流 1（手动 / 数据源有更新时）
  amane 映射表 XML ─┐
  MDCx actor-mapping┴─> actor_dict.csv（68k 别名 / 21k 演员）
  MDCx profile + mapping ─> mdcx_profiles.csv（2 万行档案宽表，根目录可直接查）

工作流 2（每天 04:00 UTC）
  actor_dict.csv ──> 演员清单（全量）
  MDCx profile ────> 三围/出生地/罩杯/身高/标签 ──┐
  JavStash ────────> 生日（唯一权威）+ 主页 ID     │
  TPDB ────────────> 三围兜底（英寸原文）          ├─> actors.jsonl（真源）
  维基 / dramaface ───> 中文简介                  │
  映人录 / 影中人 / jav-idol-db ─> 中文简介+空位字段 ┤      └─> actors.json + manifest + xlsx
  gfriends ────────> 头像（+ jsDelivr 国内镜像）   ┘
```

字段从哪个源来、优先级怎么排、怎么落成 Emby 的 `person.nfo`，见
[`docs/actor-fields.md`](docs/actor-fields.md)——**那份是字段口径的唯一权威**，本 README 只讲怎么跑。

## 工作流

| 工作流 | 职责 | 触发 | 产物 |
|---|---|---|---|
| `base-db.yml` | 合并 amane 映射表 + MDCx `actor-mapping` 成中文字典；并把 MDCx 两个库导成档案宽表 | 手动（可勾 `force` 忽略缓存） | `actor_dict.csv`、`mdcx_profiles.csv`、`data/base_sources.json` |
| `actor-sync.yml` | 导出 MDCx 档案 → 多源刮削 → 落库 → 派生 JSON/Excel | 每天 04:00 UTC + 手动（`limit`/`concurrency`/`dry_run`/`refresh`/`refresh_all`/`fill_missing`/`fill_fields`/`backfill`） | 入库 `actors.jsonl`、`actors.json`、`actors.manifest.json`；Excel 走 artifact |
| `tg-command.yml` | 处理一条 Telegram update：刮削 → 发候选图 → 换图 → 提交状态 | Cloudflare Worker 派发 | 同上（按需） |
| `pages.yml` | 把 `actors.jsonl` 派生成静态站并发 GitHub Pages | 模板/脚本改动、`actor-sync` 跑完、手动 | Pages（`site/` 不入库） |

`base-db` 是**前置条件**：它没跑过，`actor-sync` 和 `tg-command` 都会在「Check dictionary」那步直接失败。

三者共用 `concurrency: actor-data`，不会同时写数据。`tg-command` 的环境变量与
`actor-sync` 保持一致（同样挂 `MDCX_CSV` / `GFRIENDS` / `THEACTORBASE`），
否则从 Telegram 里发名字刮出来的人会缺一头一尾——没有出生地/罩杯/出道年/标签/头像。

增量都靠指纹，不变就不提交：

- amane：HTTP `ETag`，304 直接复用旧文件
- MDCx：比对 Release asset 的 `digest`（sha256）
- 两源都没变 → `exit 0`，不产生空提交

> `build-actor-list.yml`（扫 alist nfo 生成 `actors.txt`）已**删除**——清单现在直接来自
> `actor_dict.csv` 的全量演员。`--scan-nfo` 命令还在，需要时本地手工跑一次即可，
> 不用再为它配 `ALIST_*` 那套 Secret。

## 产物

| 文件 | 说明 | 谁写 |
|---|---|---|
| `actor_dict.csv` | 中文字典，4 列：`别名,日文名,中文名,来源` | `base-db.yml` |
| `mdcx_profiles.csv` | MDCx 两个 sqlite 导出的宽表（约 4MB / 2 万行）：`name,name_cn,aliases,birthplace,country,height,cup,career_start,tags,bio,romaji,birthday,bust,waist,hip`，aliases/tags 用 `|` 分隔。在仓库根目录，可直接点开查 | `base-db.yml` |
| `data/base_sources.json` | 增量状态：amane 的 etag+sha256、MDCx 的 tag+sha256 | `base-db.yml` |
| `actors.jsonl` | **唯一真源**，一行一个演员，追加友好、diff 干净（含 skipped / no_source，留档不重抓） | `actor-sync.yml` |
| `actors.json` | 由 jsonl 派生的整体对象 `{schema_version, generated_at, count, actors[]}`，**只含 `ok` / `partial`**，给消费方用 | `actor-sync.yml` |
| `actors.manifest.json` | 健康度快照：各状态数量、各源命中率、上轮耗时 | `actor-sync.yml` |
| `actors.xlsx` | 只读表格（带筛选、冻结首行，改它不回流）。**不在仓库里**，是 `actor-sync` 运行结束挂出的 artifact：Actions → 该次运行 → 页面底部 Artifacts → `actors-xlsx`（保留 90 天） | `actor-sync.yml` |
| `data/actor_dict.local.csv` | 手工补充字典，覆盖主字典同名条目 | 你 |
| `data/avatar_choices.json` | 人工选图结果（key → 选中的图片 URL），优先于自动抓取 | bot |
| `data/avatar_review.json` | 选图交互状态（发过的消息 id、getUpdates 游标） | bot |

单条记录的完整示例见 [`actors.example.json`](actors.example.json)。

### 静态站（GitHub Pages）

`scripts/build_site.py` 把 `actors.jsonl` 派生成一个纯静态的搜索页，`pages.yml` 负责生成与发布；
模板在 `site_src/`（`index.html` + `assets/app.js` + `assets/style.css`），生成物 `site/` 不入库。

```bash
python3 scripts/build_site.py actors.jsonl   # 本地生成，产物在 site/
python3 -m http.server -d site 8000          # 打开 http://localhost:8000
```

页面能做的：按名字/别名/标签搜（含 `E罩杯`）、按罩杯和身高筛、按名字/出生年/身高/罩杯/出道年排序、
只看有图、点开看三围/生日/出身/别名/简介/外链。

体积上的取舍：主索引 `site/data/actors.json` 1.6 万条约 3.3MB（gzip 后约 1MB），
字段压成定长数组、头像域名缩写（`~g/` gfriends、`~t/` thepodb、`~j/` javstash、`~s/` stashdb）
由前端展开；简介和主页链接只有约 9% 的人有，拆成 16 个 `data/detail/<i>.json` 分片，
点开详情才拉对应那一片。

## 快速开始（本地）

```bash
# 1. 准备密钥（TPDB / JavStash 至少一个，否则所有演员都会判成 no_source）
cp secrets.env.example secrets.env   # 填好，文件已被 .gitignore 排除

# 2. 生成中文字典（仓库里已有 actor_dict.csv，一般不用重生成）
python3 scripts/build_base_db.py                    # 自动下两个源；都没变会直接跳过
python3 scripts/build_base_db.py --offline ~/mdcx_db   # 用本地已有的 xml/sqlite

# 3. 导出 MDCx 档案（把 sqlite 折成扁平 CSV）
python3 scripts/export_profiles.py --offline ~/mdcx_db -o /tmp/mdcx_profiles.csv

# 4. 先空跑看看会抓谁
MDCX_CSV=/tmp/mdcx_profiles.csv ACTORS_DRY_RUN=1 go run ./cmd/actorsync --limit 3 --verbose

# 5. 真跑
MDCX_CSV=/tmp/mdcx_profiles.csv GFRIENDS=1 THEACTORBASE=1 ACTORS_CONCURRENCY=6 \
  go run ./cmd/actorsync --limit 100

# 6. 全量首铺（约 21k 人，建议本地跑或调大 ACTORS_LIMIT）
MDCX_CSV=/tmp/mdcx_profiles.csv GFRIENDS=1 THEACTORBASE=1 ACTORS_CONCURRENCY=10 \
  go run ./cmd/actorsync
```

## 配置

优先级：**命令行 flag > 环境变量 > `config.local.go` > 默认值**。

### 数据源

| 环境变量 | 必填 | 说明 |
|---|---|---|
| `JAVSTASH_API_KEY` | ✅ | 日系主力源，**生日唯一权威**，也提供主页 ID |
| `TPDB_API_KEY` | 建议 | 三围兜底（MDCx 缺项时才用）+ 体重/长简介 |
| `STASHDB_API_KEY` | 可选 | 欧美演员兜底 |
| `WIKI_EMAIL` | 可选 | 维基 API 的 User-Agent 联系邮箱，填了更礼貌 |
| `TPDB_ENDPOINT` / `JAVSTASH_ENDPOINT` / `STASHDB_ENDPOINT` | 可选 | 换镜像/代理时用 |

### 离线数据源与无 Key 网页源

| 环境变量 | 说明 |
|---|---|
| `MDCX_CSV` | MDCx 档案 CSV（`scripts/export_profiles.py` 产出）。出生地/罩杯/身高/出道年/标签/中文名/罗马字都靠它 |
| `GFRIENDS=1` | 启用 gfriends 头像库（要拉 6MB 索引） |
| `GFRIENDS_INDEX` | 本地 `Filetree.json` 路径，填了就不联网拉；调试用 |
| `THEACTORBASE=1` | 启用 theactorbase 中文简介兜底（抓网页，比 API 慢） |

以下四个**默认开启**，不需要 Key；设成 `0` 可单独关掉以加快调试：

| 环境变量 = `0` | 关掉什么 | 对结果的影响 |
|---|---|---|
| `DRAMAFACE=0` | 猜 slug 抓 dramaface 简介 | 少一批长中文简介 |
| `YINRENLU=0` | 映人录：搜索 → 详情页 | 少中文简介、生日/出道年兜底 |
| `CINEWHO=0` | 影中人：搜索 → JSON-LD | 少中文简介、生日/身高/三围兜底 |
| `IDOLDB=0` | MagicBude/jav-idol-db 的 114 份 `profile.json` | 少少量中文简介和结构字段 |

三个中文人物站都先按**原名精确对账**才采信；串人的概率远低于按名字索引的 MDCx。

### 文件与行为

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `ACTOR_DICT` | `actor_dict.csv` | 中文字典 |
| `ACTOR_DICT_LOCAL` | `data/actor_dict.local.csv` | 手工补充字典 |
| `ACTORS_FILE` | `actors.jsonl` | 真源文件；写成 `.json` 时内容仍是 JSONL |
| `ACTORS_MANIFEST` | `actors.manifest.json` | 健康度快照 |
| `ACTORS_XLSX` | `actors.xlsx` | 导出表格 |
| `ACTORS_TXT` | `actors.txt` | 手工追加清单（可空、可不存在） |
| `ACTORS_LIMIT` | `0` | 本轮最多处理多少个，0 = 不限 |
| `ACTORS_CONCURRENCY` | `1` | 并发抓取数，首铺调到 10；**没有对应 flag，只能走环境变量** |
| `ACTORS_DRY_RUN` | — | `1` = 只打印不写盘 |
| `ACTORS_VERBOSE` | — | `1` = 打印每个源的命中情况 |
| `ACTORS_NFO_DIR` / `ALIST_*` | — | 仅 `--scan-nfo` 用；头像不转存，不需要 alist |

### Secrets 一次性配置

Actions 里没有 `config.local.go`，只能走 Secrets。逐个在网页点太啰嗦，用脚本：

```bash
cp secrets.env.example secrets.env   # 填好值，文件已被 .gitignore 排除
gh auth login                        # 只需一次
./scripts/setup-secrets.sh           # 批量写入，空值会跳过
gh secret list                       # 核对
```

## 命令行

```bash
actorsync [flags]

  --names actors.txt   手工补充清单（字典之外再加人）
  --limit N            本轮最多处理多少个（0 = 不限）
  --dry-run            只打印，不写本地文件
  --refresh 名字,名字   重抓指定演员（不在字典里的人也行，会当新人抓）
  --refresh-all        重抓库里所有演员（配合 --limit 0 才是真全量）
  --fill-missing       只重抓有缺字段的已有演员，只填空位、不覆盖已有值
  --fill-fields 字段   配合 --fill-missing，逗号分隔；留空=全部可补字段
  --review-max 6       每轮最多发几个人到 Telegram 等选择（0 = 关闭）
  --xlsx actors.xlsx   导出 Excel 的路径
  --no-xlsx            不导出 Excel
  --verbose            打印每个源的命中情况
  --data / --manifest  自定义文件路径

  # 只扫描 nfo，生成 actors.txt，不抓数据
  --scan-nfo --nfo-dir /media [--nfo-max 500]

  # 常驻交互服务：在 Telegram 里发名字即时刮削（Ctrl+C 退出）
  --tg-serve

  # 处理一条 Telegram update JSON 后退出（CF Worker + Actions 用）
  --tg-once '{"update_id":1,"message":{...}}'
```

### 清单从哪来

**字典里的全部演员**（`actor_dict.csv` 的「日文名」列，约 21,322 人）+ `actors.txt` 里手工追加的名字。
`actors.txt` 可以不存在或为空，不报错。

每轮只处理「还没入库」和「到了重试时间」的人，已 `ok` 的不会重抓。

## 数据结构

`schema_version` 为 3。空值是 `null` 不是空字符串，数组空时是 `[]`。

| 字段 | 类型 | 说明 |
|---|---|---|
| `key` | string | 主键。`javstash:` → `stashdb:` → `tpdb:` → `name:<sha1>` 降级，写入后不再变 |
| `name` | string | **中文名**，Emby 的 `Name` |
| `name_original` | string? | 日文原名 |
| `aliases` | string[] | 别名（来自 MDCx） |
| `gender` | string? | `female` / `male`，小写 |
| `birth_date` | string? | `1996-05-10`，JavStash 优先、MDCx 兜底（40.9% → 65.7%），再退中文人物站 |
| `birth_date_source` | string? | `javstash` / `mdcx` / `yinrenlu` / `cinewho` / `idoldb` |
| `birth_place` | string? | 中文地名，都道府县级带国家：`日本·东京都`；只有国家时写 `日本` |
| `country` | string? | 两位码，从出生地反推，如 `JP` |
| `height_cm` | int? | 身高厘米 |
| `weight_kg` | int? | 体重公斤。**只有 JavStash / TPDB 有**，MDCx 无此字段 |
| `measurements` | string? | **英寸**，首段是「底围+罩杯」：`36J-25-39`。没有罩杯数据时首段退回胸围：`44-25-39` |
| `measurements_unit` | string? | 恒为 `in`；空表示没有三围 |
| `cup_size` | string? | 纯字母，如 `E`；来自 MDCx（73.1%），同时拼进 `measurements` 首段 |
| `career_start` | int? | 出道年份，取 MDCx `career_period` 的首年 |
| `tags` | string[] | 标签，来自 MDCx（覆盖 43.3%，已过滤敏感/未成年相关） |
| `bio` | string? | 简介，只要中文；维基 → dramaface → 映人录 → 影中人 → jav-idol-db → theactorbase，谁长用谁 |
| `bio_lang` | string? | `zh` / `ja` / `en` |
| `avatar` | string? | 头像**网络地址**（数据源原图），不下载转存 |
| `avatar_cn` | string? | 国内可访问镜像（jsDelivr），只有 gfriends 的图有 |
| `avatar_source_site` | string? | `gfriends` / `javstash` / `tpdb` / `theactorbase` |
| `homepage_url` | string? | Emby 的 `HomePageUrl`，JavStash → TPDB 降级 |
| `provider_ids` | object | `{JavStash, StashDB, TPDB, ...}` |
| `status` | string | `ok` / `partial` / `skipped` / `no_source` / `failed` |
| `sources` | object | 各源是否命中：`mdcx` `wiki` `javstash` `stashdb` `tpdb` `theactorbase` `gfriends` |
| `fail_count` | int | 连续失败次数 |
| `retry_after` | string? | 下次可重试时间 |
| `created_at` `scraped_at` | string | UTC ISO8601 |

### 状态机

| status | 含义 | 后续行为 | 进 `actors.json` |
|---|---|---|---|
| `ok` | 至少关键字段有值 | 不再抓 | ✅ |
| `partial` | 命中了源，但生日和简介都没有 | 不再抓（除非 `--refresh`） | ✅ |
| `skipped` | 性别为男，不是目标演员（实测 9.5%） | 不重抓 | ❌ |
| `no_source` | 所有源都查无此人（多是片商/导演/系列名混进字典） | 180 天后再试 | ❌ |
| `failed` | 网络/限流错误 | 2、4 天后重试；连续 3 次转 `no_source` | ❌ |

`actors.jsonl` 保留全部五种状态（让 `plan.go` 认出「已经试过了」，不必重抓），
`actors.json` 只导出 `ok` / `partial`。所以 **2 万行的 jsonl 不等于 2 万个演员**，
看可用人数要看 `actors.manifest.json` 的 `by_status`。

### 并发模型

抓取（网络 IO，可并发）和落库（写文件，必须串行）是分开的两段：
`FetchPhase` 只返回 `FetchResult`、不碰 DB，`ApplyPhase` 单协程按序写入。
所以 `ACTORS_CONCURRENCY=10` 不会写坏文件。

## 中文字典（很重要）

`actor_dict.csv` 是**别名 → 中文名 + 日文名**的映射，4 列：

```csv
别名,日文名,中文名,来源
小凑よつ叶,小湊よつ葉,小凑四叶,mdcx
こみなとよつば,小湊よつ葉,小凑四叶,amane
```

- 同一个日文名会有多行（每个别名一行），加载时合并
- 约 68,343 个别名 / 21,312 个演员 / 21,301 个有中文名
- **不要手改这个文件**，它是 `base-db.yml` 的产物，每次都会被覆盖

上游不仅收女优，还收导演、片商、作品系列名。这些条目在同步阶段会挨个去
JavStash / TPDB / 维基查、然后全部落空变成 `no_source`，白烧一轮请求，所以
`build_base_db.py` 的 `clean_actor_name()` 在生成清单时就把它们剔掉：

| 判据 | 例子 |
|---|---|
| 导演头衔 | `AV監督トッキー` |
| 性别符号 | `ようく♂` |
| 素人标签 | `素人(万引き娘)`、`素人（円光娘）` |
| 片商 / 系列名（整名精确匹配） | `SOD`、`WILD SEVEN`、`TECH`、`S級素人` |
| 下划线 → 规范成空格（**不丢**） | `Kristina_Rose` → `Kristina Rose` |

下划线那条是「修正」不是「删除」：拉丁名里的空格被上游写成 `_`，改回空格还能
继续搜到；片商名只做整名匹配，`TECHNO`、`SODA みく` 这类真名不会被误杀。规则改了
要同步 `scripts/test_build_base_db.py`。

旧字典留下的下划线记录（`Kristina_Rose` / `Lucy_Heart`）仍留在 `actors.jsonl` 备查，
但 `Exportable()` 会把它们挡在 `actors.json` 之外——否则新名字和旧名字会在 Emby 里
重复成两条。

手工补充写 `data/actor_dict.local.csv`（两列就够，`中文名,日文名`），它覆盖主字典同名条目：

```csv
中文名,日文名
小凑四叶,小湊よつ葉
```

生成脚本也能单独跑：

```bash
python3 scripts/build_base_db.py                  # 自动下载；--force 忽略缓存；--dry-run 只统计
python3 scripts/export_profiles.py -o /tmp/mdcx_profiles.csv
```

> 简繁归一的对照表是 amane 的 `zh2Hant`（10,242 条，由 `export_profiles.py` 联网拉取，
> 或用 `--zhcdict 本地文件.json` 指定）。它**不含日文新字体**（県→县、顔→颜、豊→丰），
> 所以脚本里另有一张 `JP_VARIANT` 映射表补齐，否则会出现「爱知県」这种半日半中的出生地。
> 表拉不到时只打一行日志、跳过归一化，不会让流水线挂掉。

## 头像

取图顺序：**gfriends → 数据源原图**（JavStash / TPDB / theactorbase，取 `images[0]`）。

gfriends 覆盖 66.5%（14,188 / 21,322），目录之间有优先级：

| 优先级 | 目录 | 实测 |
|---|---|---|
| 1 | `0-Hand-Storage` | 精选库，1235×1851，但只覆盖 89 人 |
| 2 | `y-Minnano` | 覆盖 58%，1049×1372 |
| 3 | `z-DMM(骑)` / `z-DMM(步)` / `y-AVDC` / `z-Derekhsu` | 覆盖率 11~29% |
| 末 | `9-Javrave` / `8-Warashi` | 250×300 缩略图，只在没有别的目录时才用 |

**有两个坑，都写在代码注释里了：**

1. `Filetree.json` 顶层是 `{Content, Information}`，`Information` 里是数字
   （`TotalNum`/`TotalSize`/`Timestamp`），不能整体当三层 map 解析。
2. `Content/<目录>` 下 **key 和 value 的配对是坏的**——实测 36.8% 对不上，
   例如 key 是 `桃乃木かな.jpg` 而 value 指向 `AI-Fix-松嶋真麻.jpg`（已下载肉眼确认：
   文件名叫什么就是谁，错的是配对）。但同一目录里 key 集合和 value 集合**完全相同**，
   所以「这个目录里有哪些文件」可信，「哪个文件对应哪个名字」不可信。
   实现上只用 value 建「实际存在的文件名」集合，完全不拿 key 做映射，
   既不张冠李戴，也不会拼出 404。

`AI-Fix-*` 是 AI 修图过的版本，同一个人同时有两版时优先原图；只有 AI-Fix 版时才用。

> 注意：数据源原图是 CDN 链接，部分带防盗链/签名，过一段时间可能 403。
> 若头像失效，用下面的 Telegram 人工选图换一张即可。

## 人工选图（Telegram）

自动取图永远是第一张，经常不是最好看的那张。三种用法，按部署条件选：

| 方式 | 延迟 | 需要什么 |
| --- | --- | --- |
| A 批量同步顺带问 | 下一轮（约一天） | 无额外部署 |
| **B Cloudflare Worker** | 约 10–60 秒 | 一个 Worker + PAT，**不需要 NAS / 软路由 / VPS** |
| C 常驻 `--tg-serve` | 即时 | 一台常开机器 |

三者共用同一套 Bot 逻辑：发名字 → 完整刮削 → 发候选相册 → 点数字按钮换图。
差别只在「消息怎么送进来」。

**通用 Secret**（GitHub 仓库里）：

| Secret | 说明 |
| --- | --- |
| `TELEGRAM_BOT_TOKEN` | @BotFather 申请 |
| `TELEGRAM_CHAT_ID` | 你与 bot 的会话 id（或群 id） |
| `TELEGRAM_API_BASE` | 可选。境内直连 `api.telegram.org` 不通时填反代地址 |

交互行为（三种方式都一样）：

- **在库的人**：刷新元数据并更新网络头像，候选第 1 张固定是当前正在用的那张
- **不在库的人**：完整刮削补齐元数据（生日、身高、三围、简介、别名、来源 ID）后入库
- **点按钮**：把选中图的网络地址写进头像 → 写 `actors.jsonl` → 记进
  `avatar_choices.json`，以后同步不会再被自动图覆盖
- 抓取失败但库里已有记录时**保留旧数据**，不会把好好的 `ok` 改成 `no_source`

命令（B、C 支持）：

| 命令 | 作用 |
| --- | --- |
| 直接发名字 | 刮削 + 发候选图 |
| `/repick 名字` | 重抓并重新发候选（换掉已选的） |
| `/status 名字` | 只看库里现状，不联网 |
| `/list [关键词]` | 列出库里的人 |
| `/id` | 告诉你当前会话 id，首次配置用 |
| `/help` | 用法说明 |

### 方式 A：批量同步里顺带问（零部署）

**新演员**和**抓完仍没有头像的人**在批量同步收尾时把候选图发到你会话里。

1. 收尾时相册发出，下面一排 `1 2 3 4 5` 按钮
2. 你点一个。此时进程已退出，按钮会转圈到超时——但**点击已经进了 Telegram 的
   updates 队列（保留 24 小时）**
3. 下一轮启动：`getUpdates` 把点击取回来，写进 `data/avatar_choices.json`
4. 之后这个人一律用你选的图的网络地址

约束：每轮最多发 `ACTORS_REVIEW_MAX` 个（默认 6）；候选少于 2 张不发；
想改主意就删掉 `data/avatar_choices.json` 里对应的 key。
两个状态文件都要提交进仓库（workflow 已包含）。

### 方式 B：Cloudflare Worker（无本地设备）

Worker **只做两件事**：接 Telegram webhook、把 update 派发成 GitHub
`repository_dispatch`。刮削仍在 Actions 里跑一次性命令 `--tg-once`，Worker
不存状态、不需要 KV / D1 / R2，也不跑 Go。

```
Telegram ──webhook──> CF Worker ──repository_dispatch──> Actions (--tg-once)
                                                          │
                                                actors.jsonl / choices
```

**一次性部署**

```bash
cd worker
npm install

# 生成两个随机串，记下来
openssl rand -hex 24   # 给 TELEGRAM_WEBHOOK_SECRET
openssl rand -hex 24   # 给 SETUP_KEY

npx wrangler secret put TELEGRAM_BOT_TOKEN
npx wrangler secret put TELEGRAM_WEBHOOK_SECRET
npx wrangler secret put SETUP_KEY
npx wrangler secret put GH_TOKEN     # classic PAT，勾 repo；用来触发 workflow
npx wrangler deploy
```

然后在 Worker 上注册 webhook（`SETUP_KEY` 是你刚生成的那个）：

```bash
curl -X POST https://actor-em-tg.<你的子域>.workers.dev/setup \
  -H "Authorization: Bearer $SETUP_KEY"
```

webhook 模式下**不需要配置 `TELEGRAM_CHAT_ID`**：回复目标自动从消息里取，
所以给 bot 发名字就能直接收到候选图。给 bot 发 `/id` 会回你当前会话 id，
把它填进 `worker/wrangler.toml` 的 `ALLOWED_CHAT_ID` 并重新
`npx wrangler deploy`——这样别人给 bot 发消息不会白耗 Actions 额度。

**注意**

- `GH_TOKEN` 必须是 **classic PAT 且勾 `repo`**。用 `GITHUB_TOKEN` 触发的
  dispatch 不会再触发 workflow。
- Worker 注册 webhook 时带了 `drop_pending_updates: true`，会清掉旧消息；
  这是为了避免和 `getUpdates` 冲突（见下）。
- Actions 冷启动要 10–60 秒，所以发名字后不会"秒回"，点按钮也要等同样时间。
- 每次点击都跑一遍 Actions，按量计费；`ALLOWED_CHAT_ID` 一定要设。
- `actor-sync.yml` 里 `ACTORS_REVIEW_MAX` 应设为 `0`（webhook 与 getUpdates 互斥，
  两边同时收会拿到 `409 Conflict` 并互相吃掉点击）。想用方式 A 就手动改回 `6`，
  但**不要和 Worker 同时开**。
- 本地调试单条消息：`actorsync --tg-once '{"update_id":1,"message":{...}}'`。

### 方式 C：常驻交互服务（本地设备）

```bash
actorsync --tg-serve
```

用 25 秒长轮询收消息，是**客户端主动出站 HTTPS**，不需要公网 IP、证书或域名。
家里 NAS、软路由、树莓派、任意 VPS 都能跑。`Dockerfile` 和 systemd 配置可用：

```bash
docker build -t actor-em .
docker run -d --name actor-em --restart unless-stopped \
  --env-file secrets.env \
  -v "$PWD/actors.jsonl:/app/actors.jsonl" \
  -v "$PWD/actor_dict.csv:/app/actor_dict.csv:ro" \
  -v "$PWD/data:/app/data" \
  actor-em
```

`actors.jsonl` 和 `data/` 必须挂进去——bot 会写这两个地方。
同样要把批量那边的 `ACTORS_REVIEW_MAX` 设为 `0`，否则两边抢 update。

## GitHub Actions

仓库 Settings → Secrets and variables → Actions 里把上表的变量逐个加进去，然后按顺序：

1. 手动跑一次 `base-db`，确认 `actor_dict.csv` 有 3MB 左右、2 万多演员
2. 手动跑一次 `actor-sync`（`limit=10` 先验证链路）
3. 没问题再放开 `limit=0` 首铺

- 定时：每天 04:00 UTC
- 手动：可指定 `limit`、`concurrency`、`dry_run`、`refresh`、`refresh_all`、`fill_missing`、`fill_fields`、`backfill`
  - 全量重抓：`refresh_all=true` + `limit=0`（四种改写模式互斥，不能同时勾）
  - 只补缺字段：`fill_missing=true`，默认补全部可补字段；只想补简介和生日就填 `fill_fields=bio,birthday`
  - 补离线字段：`backfill=true`（不联网），顺带把存量出生地补成 `日本·东京都`
  - 补缺字段只认 `ok/partial` 记录，重抓后逐字段填空，已有值/status/头像都保留
- 有变化才 commit，无变化不产生空提交
- 同一时间只跑一轮（`concurrency: actor-data`）
- `actor-sync` 超时 350 分钟

## 注意

- **默认不刷新老记录**。演员补了新简介不会自动更新；用 `--refresh 名字` 单抓、
  `--refresh-all --limit 0` 全量覆盖重抓，或 `--fill-missing` 只挑缺字段的记录补值。
- **不在 MDCx 里的人也能抓**。MDCx 只是字段地基，不是准入闸门：它查不到就只记一条 miss，
  继续走维基 → JavStash → StashDB/TPDB → 中文站。`--refresh 名字` 里的名字如果字典和
  `actors.txt` 都没有，会临时补进本轮清单（库里有记录就刷新，没有就当新人抓）；
  代价是没有 MDCx 那份三围/罩杯/出生地/标签打底，只能靠在线源兜。
- **不加锁**。如果两条记录用同一个名字，头像会互相覆盖（后写的胜）。
- `key` 一旦写入就不再改，即使后来匹配到了 JavStash ID——避免同一人出现两条。
- **三围会换算**。MDCx 的厘米分项换算成英寸，并用日标杯差反推底围，写成 `36J-25-39`
  这种欧美标法（误差约一码，见 `docs/actor-fields.md`）。TPDB 的原文本来就是这个写法，直接留用。
- **`docs/actor-fields.md` 与代码里的 `schema_version` 必须同步**，改字段先改文档。
