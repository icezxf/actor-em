# actor-em

从 Emby 拉演员清单，只给**库里还没有的新演员**补元数据，头像转存到 alist，结果写成根目录的 `actors.json`。
任何程序读这一个文件就能拿到全部演员信息，不需要连 Emby、也不需要数据库。

```
Emby /Persons ──> 对比 actors.json ──> 只抓新增 ──> 四源聚合 ──> alist 头像
                                                              │
                                                       actors.json + manifest
```

## 产物

| 文件 | 说明 |
|---|---|
| `actors.json` | 唯一真源。单文件对象，`{schema_version, generated_at, count, actors[]}` |
| `actors.manifest.json` | 健康度快照：各状态数量、各源命中率、上轮耗时 |

单条记录的完整示例见 [`actors.example.json`](actors.example.json)。字段说明见下方[数据结构](#数据结构)。

## 快速开始（本地）

```bash
# 1. 准备配置
cp cmd/actorsync/config.local.go.example cmd/actorsync/config.local.go
#    打开 config.local.go，填 Emby 和 alist 的地址、账号

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
| `EMBY_URL` | EmbyURL | ✅ | 例 `https://emby.example.com` |
| `EMBY_USER` / `EMBY_PASS` | EmbyUser / EmbyPass | ✅ | 建议新建**只读**账号，别用管理员 |
| `EMBY_API_KEY` | EmbyAPIKey | 可选 | 账号开了 2FA 时用这个兜底 |
| `TPDB_API_KEY` | TPDBAPIKey | 建议 | 生日、三围、体重、图的主要来源 |
| `JAVSTASH_API_KEY` | JavStashAPIKey | ✅ | 日系演员主力源 |
| `STASHDB_API_KEY` | StashDBAPIKey | 可选 | 欧美演员兜底 |
| `DEEPL_API_KEY` | DeepLAPIKey | 可选 | 英文简介译成中文，留空不翻译 |
| `WIKI_EMAIL` | WikiEmail | 可选 | 维基 API 的 User-Agent 联系邮箱 |
| `ALIST_URL` | AlistURL | ✅ | 例 `https://alist.example.com` |
| `ALIST_USER` / `ALIST_PASS` | AlistUser / AlistPass | ✅ | alist 账号 |
| `ALIST_DIR` | AlistDir | ✅ | 头像根目录，默认 `/actors` |
| `ALIST_PUBLIC_BASE` | AlistPublicBase | 可选 | 独立直链域名，默认同 `ALIST_URL` |
| `ACTOR_DICT` | DictFile | 建议 | 中文→日文名字典，默认 `data/actor_dict.csv` |
| `ACTOR_DICT_LOCAL` | DictLocalFile | 可选 | 手工补充字典，默认 `data/actor_dict.local.csv` |
| `TPDB_ENDPOINT` / `JAVSTASH_ENDPOINT` / `STASHDB_ENDPOINT` | Endpoint | 可选 | 换镜像/代理时用，默认官方地址 |
| `ACTORS_LIMIT` | Limit | 可选 | 本轮最多处理多少个，0 = 不限 |
| `ACTORS_DRY_RUN` | DryRun | 可选 | `1` = 只打印不写盘 |

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

  --limit N            本轮最多处理多少个（0 = 不限）
  --dry-run            只打印，不写文件
  --refresh 名字,名字   重抓指定演员
  --refresh-all        重抓库里所有演员（最慢）
  --force-avatar       头像已存在也重新上传
  --verbose            打印每个源的命中情况
  --data / --manifest  自定义文件路径
```

## 数据结构

`actors.json` 是一条记录一行的扁平静态数据，空值是 `null` 不是空字符串。

| 字段 | 类型 | 说明 |
|---|---|---|
| `key` | string | 主键。`javstash:` → `stashdb:` → `tpdb:` → `name:<sha1>` 降级 |
| `name` | string | **Emby 里的名字**，也是头像文件名 |
| `name_original` | string? | 数据源返回的原始名（多为日文原名） |
| `aliases` | string[] | 别名 |
| `emby_ids` | string[] | Emby Person Id，多服务器时追加 |
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
| `avatar` | string? | alist 直链，**已 percent-encode**，直接 GET |
| `avatar_path` | string? | alist 内路径 `/actors/小/小凑四叶.jpg` |
| `avatar_dir` | string? | alist 目录 `/actors/小` |
| `avatar_source` | string? | **原始网络地址**，CDN 会过期，仅溯源用 |
| `avatar_source_site` | string? | `javstash` / `tpdb` / `stashdb` |
| `avatar_sha256` | string? | 校验、去重 |
| `avatar_bytes` `avatar_w` `avatar_h` | int? | 文件大小与尺寸 |
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
| `no_source` | 四个源都查无此人 | 180 天后再试 |
| `failed` | 网络/限流错误 | 2、4 天后重试；连续 3 次转 `no_source` |

## 中文字典（很重要）

Emby 里的演员名通常是中文（`小凑四叶`），而 JavStash / TPDB 只认日文或罗马字（`小湊よつ葉`）。
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

路径规则：`{ALIST_DIR}/{名字首字符}/{Emby 里的名字}.jpg`

```
/actors/小/小凑四叶.jpg
/actors/J/John Smith.jpg
/actors/1/123.jpg
```

- 首字符是中日韩字符就原样（`小`、`あ`），拉丁字母转大写，数字原样，其余归 `_`
- 只做必要 sanitize：`\ / : * ? " < > |` 换 `_`、去首尾空白与尾部点、按 rune 截断 80 字节
- 上传前先查是否存在，**存在就跳过**（`--force-avatar` 可强制覆盖）
- 演员改名会换文件名、可能换桶，旧的会成孤儿（规模大时再加清理任务）

> 注意：`avatar_source` 是数据源的 CDN 地址，带防盗链/签名，几个月后可能 403。
> 运行时请一律用 `avatar`（alist 副本）。

## GitHub Actions

仓库 Settings → Secrets and variables → Actions 里把上表的变量逐个加进去，然后
Actions 页面手动跑一次 `actor-sync` 验证。

- 定时：每天 04:00 UTC
- 手动：可指定 `limit`、`dry_run`、`refresh`
- 有变化才 commit，无变化不产生空提交
- 同一时间只跑一轮（`concurrency`）

## 注意

- **只读 Emby，不回写**。Emby 侧的刮削继续用你原来的工具。
- **不刷新老记录**。演员补了新简介不会自动更新，用 `--refresh 名字` 手动触发。
- **不加锁**。如果两条记录用同一个名字，头像会互相覆盖（后写的胜）。
- **Ouxiang 站点可能已失效**：代码里 `ouxiangdanku.com` 若 DNS 解析不到会直接跳过（12 秒超时），不影响其他源。
- `key` 一旦写入就不再改，即使后来匹配到了 JavStash ID——避免同一人出现两条。
