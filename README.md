# actor-em

读 `actors.txt` 里的待抓清单（也可以从 alist 的 nfo 自动生成），只给**库里还没有的演员**补元数据，
头像直接用数据源给的网络地址（不下载、不转存），结果写成根目录的 `actors.json`，同时导出 `actors.xlsx` 方便手工查询。
任何程序读这一个 JSON 就能拿到全部演员信息，不依赖媒体服务器，也不需要数据库。

```
actors.txt（手工 / nfo 扫描）         Telegram 发名字（--tg-serve）
      │                                      │
      ▼                                      ▼
对比 actors.json ──> 只抓新增 ──> 五源聚合 ──> 网络头像地址
                                              │
                                    actors.json + manifest + actors.xlsx
```

三种用法，任选：

| 方式 | 触发 | 延迟 | 适合 |
|---|---|---|---|
| GitHub Actions 定时 | 每天 04:00 UTC | 一天 | 无人值守地补全清单 |
| `--tg-serve` 常驻服务 | 你在 Telegram 里发名字 | 秒级 | 临时想补谁、顺手挑图 |
| 本地命令行 | `actorsync --refresh 名字` | 立即 | 调试、跑一次性任务 |

## 三个工作流

| 工作流 | 职责 | 触发 |
|---|---|---|
| `build-actor-list.yml` | 扫 alist 目录里的 `.nfo`，把 `<actor><name>` 收集成 `actors.txt` | 每周一 + 手动 |
| `actor-sync.yml` | 按 `actors.txt` 增量刮削，产出 json/manifest/xlsx | 每天 04:00 UTC + 手动 |
| `tg-command.yml` | 处理一条 Telegram update：刮削 → 发候选图 → 换图 → 提交状态 | Cloudflare Worker 派发 |

三者共用一个 `concurrency` group，不会同时写 `actors.json`。

## 产物

| 文件 | 说明 |
|---|---|
| `actors.txt` | 待抓清单，一行一个名字；`#` 开头是注释 |
| `actors.json` | 唯一真源。单文件对象，`{schema_version, generated_at, count, actors[]}` |
| `actors.manifest.json` | 健康度快照：各状态数量、各源命中率、上轮耗时 |
| `actors.xlsx` | 由 json 导出的只读表格，带筛选和冻结首行，**改它不会回流** |
| `data/avatar_choices.json` | 人工选图结果（key → 选中的图片 URL），优先于自动抓取 |
| `data/avatar_review.json` | 选图交互状态（发过的消息 id、getUpdates 游标），丢了会重复发图 |

单条记录的完整示例见 [`actors.example.json`](actors.example.json)。字段说明见下方[数据结构](#数据结构)。

## 快速开始（本地）

```bash
# 1. 准备配置
cp cmd/actorsync/config.local.go.example cmd/actorsync/config.local.go
#    打开 config.local.go，填 alist 的地址、账号和数据源 key

# 1.5 生成待抓清单（从 alist 的 nfo）
make run ARGS="--scan-nfo --nfo-dir /media"
#    或者手工往 actors.txt 里写名字，一行一个

# 2. 先空跑看看会抓谁
make run ARGS="--limit 3 --dry-run"

# 3. 真跑
make run ARGS="--limit 10"

# 4. 全量（首次冷启动）
make run
```

`config.local.go` 已被 `.gitignore` 忽略，不会进 git。它是通过 `-tags local` 编译进来的，
所以不带这个文件时（比如 CI）也能正常编译，参数全走环境变量。

## 配置

优先级：**命令行 flag > 环境变量 > config.local.go > 默认值**。

| 环境变量 | 对应配置 | 必填 | 说明 |
|---|---|---|---|
| `ACTORS_TXT` | NamesFile | ✅ | 待抓清单，默认 `actors.txt` |
| `ACTORS_XLSX` | XlsxFile | 可选 | 导出的 Excel，默认 `actors.xlsx` |
| `ALIST_NFO_DIR` | NFODir | 扫 nfo 时必填 | alist 里 nfo 所在目录 |
| `TPDB_API_KEY` | TPDBAPIKey | 建议 | 生日、三围、体重、图的主要来源 |
| `JAVSTASH_API_KEY` | JavStashAPIKey | ✅ | 日系演员主力源 |
| `STASHDB_API_KEY` | StashDBAPIKey | 可选 | 欧美演员兜底 |
| `DEEPL_API_KEY` | DeepLAPIKey | 可选 | 英文简介译成中文，留空不翻译 |
| `WIKI_EMAIL` | WikiEmail | 可选 | 维基 API 的 User-Agent 联系邮箱 |
| `ACTOR_DICT` | DictFile | 建议 | 中文→日文名字典，默认 `data/actor_dict.csv` |
| `ACTOR_DICT_LOCAL` | DictLocalFile | 可选 | 手工补充字典，默认 `data/actor_dict.local.csv` |
| `TPDB_ENDPOINT` / `JAVSTASH_ENDPOINT` / `STASHDB_ENDPOINT` | Endpoint | 可选 | 换镜像/代理时用，默认官方地址 |
| `ACTORS_LIMIT` | Limit | 可选 | 本轮最多处理多少个，0 = 不限 |
| `ACTORS_DRY_RUN` | DryRun | 可选 | `1` = 只打印不写盘 |
| `TELEGRAM_BOT_TOKEN` / `TELEGRAM_CHAT_ID` | TelegramToken / TelegramChatID | 可选 | 两个都填才启用人工选图 |
| `TELEGRAM_API_BASE` | TelegramAPIBase | 可选 | 反代地址；境内直连 `api.telegram.org` 不通时必填 |
| `ACTORS_REVIEW_MAX` | ReviewMax | 可选 | 每轮最多发几个人等选择，默认 6，`0` = 关闭 |
| `ACTORS_REVIEW_STATE` / `ACTORS_CHOICES` | ReviewStateFile / ChoicesFile | 可选 | 默认 `data/avatar_review.json`、`data/avatar_choices.json` |

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

  --names actors.txt   清单文件，唯一的清单来源
  --limit N            本轮最多处理多少个（0 = 不限）
  --dry-run            只打印，不写本地文件
  --refresh 名字,名字   重抓指定演员
  --refresh-all        重抓库里所有演员（最慢）
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

清单只来自 `actors.txt`，一行一个名字，`#` 开头是注释。两种生成方式：

1. 手工写（最直接，100 个演员也就几分钟）
2. `--scan-nfo --nfo-dir /media`：递归读 alist 目录里所有 `.nfo` 的 `<actor><name>`，去重后写回

nfo 扫描会跳过隐藏目录（`.recycle`、`$RECYCLE.BIN`），只认 `<actor>` 下的 `<name>`，
不会把 `<movie><name>` 的片名当成演员。

## 数据结构

`actors.json` 是一条记录一行的扁平静态数据，空值是 `null` 不是空字符串。
`schema_version` 为 2，已移除 Emby 相关的 `emby_ids` 字段。

| 字段 | 类型 | 说明 |
|---|---|---|
| `key` | string | 主键。`javstash:` → `stashdb:` → `tpdb:` → `name:<sha1>` 降级 |
| `name` | string | 清单里的名字，也是头像文件名 |
| `name_original` | string? | 数据源返回的原始名（多为日文原名） |
| `aliases` | string[] | 别名 |
| `gender` | string? | `female` / `male` |
| `birth_date` | string? | `1996-05-10` |
| `death_date` | string? | 目前所有源都不返回，恒为 null（占位） |
| `birth_place` | string? | 中文地名，如 `日本` |
| `country` | string? | 两位码，如 `JP` |
| `height_cm` `weight_kg` `waist_cm` `hip_cm` | int? | 身体数据 |
| `cup_size` | string? | 如 `D` |
| `career_start` | int? | 出道年份 |
| `bio` | string? | 简介，优先中文 |
| `bio_lang` | string? | `zh` / `ja` / `en` |
| `avatar` | string? | 头像的**网络地址**（数据源原图 URL），直接用 |
| `avatar_source` | string? | 同 `avatar`，溯源用 |
| `avatar_source_site` | string? | `javstash` / `tpdb` / `stashdb` / `manual` |
| `avatar_path` `avatar_dir` `avatar_sha256` `avatar_bytes` `avatar_w` `avatar_h` | — | 旧版 alist 转存字段，现已不再写入，恒为 `null` |
| `provider_ids` | object | `{JavStash, StashDB, TPDB, Tmdb, Imdb, Wikipedia, ...}` |
| `status` | string | `ok` / `partial` / `no_source` / `failed` |
| `sources` | object | 各源是否命中 |
| `fail_count` | int | 连续失败次数 |
| `retry_after` | string? | 下次可重试时间 |
| `created_at` `scraped_at` | string | UTC ISO8601 |

### 状态机

| status | 含义 | 后续行为 |
|---|---|---|
| `ok` | 抓到生日或简介 | 不再抓 |
| `partial` | 命中了源，但生日和简介都没有 | 不再抓（除非 `--refresh`） |
| `no_source` | 所有源都查无此人 | 180 天后再试 |
| `failed` | 网络/限流错误 | 2、4 天后重试；连续 3 次转 `no_source` |

## 中文字典（很重要）

清单里的演员名通常是中文（`小凑四叶`），而 JavStash / TPDB 只认日文或罗马字（`小湊よつ葉`）。
**没有这层映射，日系演员几乎一条都搜不到。**

字典分两份，两列 CSV：

| 文件 | 来源 | 能不能手改 |
|---|---|---|
| `data/actor_dict.csv` | `scripts/gen_dict.py` 从 [amane 映射表](https://github.com/sqzw-x/amane) 生成，约 1 万条 | ❌ 会被重新生成覆盖 |
| `data/actor_dict.local.csv` | 你自己补的，覆盖主字典同名条目 | ✅ 随便改 |

```bash
python3 scripts/gen_dict.py        # 刷新主字典（workflow 每天也会刷）
```

手工补充写进 `data/actor_dict.local.csv`：

```csv
中文名,日文名
小凑四叶,小湊よつ葉
三上悠亚,三上悠亜
```

主字典来自 QMS 同一份数据源（`mapping_actor.xml`），但它并不覆盖所有译名——
比如它只有 `小凑よつ叶`，没有 `小凑四叶`。这种缺口就靠 `.local.csv` 补，
两者在加载时合并，不冲突。

查找顺序是：字典 → Ouxiang 站点 → 维基跨语言重定向 → 直接用原名搜。
所以字典不是唯一途径，但它最稳、最快，也最省请求。

## 头像

头像不再下载转存，直接把数据源给的图片网络地址写进 `avatar`（`avatar_source` 同值）。
所以无需 alist，也不用配 `ALIST_DIR` 等头像相关参数。

取图顺序：JavStash → StashDB → TPDB，且每个源都只取 `images[0]`——上游给的顺序，
不一定是最好看的那张。要人工挑，见下一节。

> 注意：这些地址是数据源的 CDN 链接，部分带防盗链/签名，过一段时间可能 403。
> 若发现头像失效，用下面的 Telegram 人工选图换一张即可。

## 人工选图（Telegram）

自动取图永远是 `images[0]`，经常不是最好看的那张。三种用法，按部署条件选：

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
- **点按钮**：把选中图的网络地址写进头像 → 写 `actors.json` → 记进
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
                                        actors.json / choices
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
- Actions 冷启动要 10–60 秒，所以发名字后不会“秒回”，点按钮也要等同样时间。
- 每次点击都跑一遍 Actions，按量计费；`ALLOWED_CHAT_ID` 一定要设。
- `actor-sync.yml` 里 `ACTORS_REVIEW_MAX` 已设为 `0`（webhook 与 getUpdates 互斥，
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
  -v "$PWD/actors.json:/app/actors.json" \
  -v "$PWD/data:/app/data" \
  actor-em
```

`actors.json` 和 `data/` 必须挂进去——bot 会写这两个地方。
同样要把批量那边的 `ACTORS_REVIEW_MAX` 设为 `0`，否则两边抢 update。

## GitHub Actions

仓库 Settings → Secrets and variables → Actions 里把上表的变量逐个加进去，然后
Actions 页面手动跑一次 `actor-sync` 验证。

- 定时：每天 04:00 UTC
- 手动：可指定 `limit`、`dry_run`、`refresh`
- 有变化才 commit，无变化不产生空提交
- 同一时间只跑一轮（`concurrency`）

## 注意

- **不刷新老记录**。演员补了新简介不会自动更新，用 `--refresh 名字` 手动触发。
- **不加锁**。如果两条记录用同一个名字，头像会互相覆盖（后写的胜）。
- **Ouxiang 站点可能已失效**：代码里 `ouxiangdanganku.com`（偶像档案库）若 DNS 解析不到会直接跳过（12 秒超时），不影响其他源。
- `key` 一旦写入就不再改，即使后来匹配到了 JavStash ID——避免同一人出现两条。
