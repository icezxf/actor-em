# 演员字段来源与 Emby 映射

本文是 `actors.jsonl` 的字段权威说明：每个字段从哪来、优先级怎么排、最终怎么落到 Emby 的 `person.nfo`。
所有百分比都是对本地副本实测的结果，不是估算。

---

## 1. 数据源分工

| 源 | 提供什么 | 覆盖 / 规模 | 备注 |
|---|---|---|---|
| **amane** `mapping_actor.xml` | 中日名映射（别名 → 日文原名） | 5,351 演员 / 15,941 别名 | 字典骨架，更新很慢（2026-08 后无提交） |
| **MDCx** `actor-mapping.sqlite3` | 中文名（`zh_cn`）、别名、罗马字 | 22,209 演员，中文名 99.9% | 补字典用，接入后字典涨到 21,161 演员 / 64,319 别名 |
| **MDCx** `actor-profile.sqlite3` | 演艺期间、身高、**三围分项（bust/waist/hip，80%）**、罩杯、出生地、别名、结构化简介 | 21,860 演员 | 结构化字段的主力；三围是厘米，读入时换算成英寸 |
| **JavStash** | 生日、出道年、身高、**三围（band/cup/waist/hip）**、图、别名、外链 | 日系演员主力 | **生日唯一权威源**；三围排 TPDB 之前 |
| **StashDB** | 出生地（只到国家）、出道年、身高、三围（字段与 JavStash 完全一致）、图、别名、外链；**英文名**是搜 TPDB 的硬规定关键词 | 与 JavStash 同一套 schema，共用一套查询字段 | 整条记录按空位合并（生日除外，JavStash 唯一权威）；日系演员靠 JavStash 外链里的 UUID 反查 |
| **TPDB** | 三围（英寸）、体重、长简介、出道年、图 | 三围 46% / 体重 13% / 长简介 27% | 三围**兜底**：第一段可能是「下胸围+杯」，不能直接当胸围 |
| **中文维基** | 中文简介 | 命中率一般，但质量最高 | 简介第 1 顺位，需带 `WIKI_EMAIL` 防风控 |
| **dramaface**（剧面） | 中文简介（整段，质量高） | 静态 HTML，slug = `罗马字名去空格拼接`（如 `MomonogiKana`），大小写不敏感 | 简介第 2 顺位，默认开启（`DRAMAFACE=0` 关），需浏览器 UA |
| **yinrenlu**（映人录） | 中文简介（2 段）、中文艺名、罗马字、生日、出道年、出生地 | `/search?q=` → `/people/<id>`，样本 10 查 9 中 | 默认开启（`YINRENLU=0` 关）；卡片原名 + 详情原名双重对账 |
| **cinewho**（影中人） | 中文简介（长）、生日、身高、三围、中文名、英文名 | `/search.php?q=` → `/cine/<slug>`，JSON-LD `Person` 结构 | 默认开启（`CINEWHO=0` 关）；详情页原名/英文名对账 |
| **jav-idol-db** | 16 份中文简介、日文原名 / `name_zh` / 罗马字 / 生日 / 三围 / 出道 | GitHub raw 的 `data/actresses/<日文名>/profile.json`，共 114 份 | 默认开启（`IDOLDB=0` 关）；目录名即原名，按名取档几乎不串人 |
| **theactorbase** | 中文简介（约 500 字） | 静态 HTML，slug = `罗马字姓-名` | 中文简介链末端，需 `THEACTORBASE=1` 开 |
| **gfriends** | 头像图库（51 分类 / 107,720 条 / 10GB，每日更新） | 一个演员可能多张 | **头像首选**，同时给国内镜像 |
| ~~Ouxiang~~ | ~~中文简介~~ | 站点已挂 | 只保留 `provider_ids.Ouxiang` 存档 |

---

## 2. `actors.jsonl` 字段表（schema v3）

一行一个演员。空值写 `null`，不写空字符串。

### 2.1 身份

| 字段 | 类型 | 来源优先级 | 说明 |
|---|---|---|---|
| `key` | string | `javstash:` → `stashdb:` → `tpdb:` → `name:<sha1>` | 主键，降级取 |
| `name` | string | 合并字典中文名 → MDCx `name_cn` → 日文原名 | **不翻译**，只用已有中文名 |
| `name_original` | string | 字典日文名 → JavStash `name` → TPDB `name` | 日文原名 |
| `aliases` | string[] | 字典别名 + MDCx `aliases` + JavStash `aliases` + TPDB `aliases` | 去重、去自身，保持原语言；**只供检索，不写 Overview** |

### 2.2 基础属性

| 字段 | 类型 | 来源优先级 | 说明 |
|---|---|---|---|
| `gender` | string? | JavStash → TPDB → cinewho | `female` / `male` |
| `birth_date` | string? | **JavStash → MDCx 兜底 → 中文人物站兜底** | 见下方「生日的来源」 |
| `birth_date_source` | string? | 跟 `birth_date` 同时写 | `javstash` / `mdcx` / `yinrenlu` / `cinewho` / `idoldb`，空表示没有生日 |
| `death_date` | — | 不采集 | 忌日明确不要，字段直接删 |
| `birth_place` | string? | MDCx bio 的「出身:」（44.3%，到都道府县）→ MDCx profile `birthplace`（43.7%）→ JavStash `country` → StashDB `country`（这两个只到「日本」） | **都道府县级一律带国家**，如 `日本·东京都`；落到 Emby `ProductionLocations` |
| `country` | string? | 从 `birth_place` 反推（出生地是日本都道府县 → `JP`）→ JavStash → StashDB → TPDB `birthplace_code` | 两位码；**MDCx 没有国家码字段**，只能从出生地推 |

**出生地一律带国家**：库里存的和导出给 Emby 的是同一个值，都道府县级一律拼成
`日本·东京都`（分隔符 `·`），本来只到国家的保留 `日本`；港澳台这类「中国XX」的
国家名本身已是省级，不再叠一层。国家码缺失但认得出是日本都道府县时，顺手把
`country` 补成 `JP`。`--backfill` 会把存量记录按同一口径改写，不必重抓。

**生日的两级来源**：JavStash 是权威，它给了就绝不覆盖；只有它没给时才用 MDCx。

| | 覆盖率 | 与 JavStash 一致率 | 处理 |
|---|---|---|---|
| JavStash | 40.9%（866 条实测） | — | 直接写，`birth_date_source=javstash` |
| MDCx `birthday` | 65.2%（14,254 条） | 75.8%（178 条都有里 135 条一致） | 只在空位补，`birth_date_source=mdcx` |
| yinrenlu / cinewho / jav-idol-db | 三站合计只补前两站剩下的空位 | 页面原名与候选名精确一致才采 | `birth_date_source` 写对应站名，便于单独剔除 |

JavStash + MDCx 把覆盖率从 40.9% 提到 **65.7%**（+215 条 / 866 条）。冲突的 24.2% 里 79% 连年份都不同，多发生在短名和常见名上（`AI`、`MINAMO`、`SOPHIA`、`かわいさとみ`），本质是 MDCx 按名字索引撞了同名异人。

**为什么不担心"引入错误数据"**：兜底只在 `birth_date` 为空时生效，JavStash 命中时这个分支根本走不到，所以对已有权威值零影响。新加的三个中文站还要先过原名对账，比 MDCx 的纯名字索引更严；下游若只要权威值，按 `birth_date_source == "javstash"` 过滤即可。

### 2.3 身体

> **口径统一**：三围一律**英寸**，第一段优先用**「底围+罩杯」**的欧美标法：
> `36J-25-39`。没有罩杯数据时第一段退回胸围本身：`44-25-39`。
> MDCx 给的是厘米分项（覆盖 80%），读入时换算；TPDB 本来就是英寸。

| 字段 | 类型 | 来源优先级 | 说明 |
|---|---|---|---|
| `height_cm` | int? | MDCx `height`（75.5%）→ JavStash → TPDB | 纯数字 |
| `weight_kg` | int? | 只有 TPDB `extras.weight`（JavStash / StashDB 的 Performer **没有 weight 字段**，查了会整条 schema 校验失败） | 覆盖很低 |
| `measurements` | string? | MDCx `bust/waist/hip/cup`（**80% 齐备**）→ JavStash / StashDB `band_size + cup_size + waist_size + hip_size` → TPDB 原文 | `36J-25-39`（底围+杯-腰-臀）或 `44-25-39`（无罩杯时首段是胸围） |
| `measurements_unit` | string? | 跟着 `measurements` 走 | 目前恒为 `in`；空表示没有三围 |
| `cup_size` | string? | MDCx profile `cup`（**73.1%**，纯字母 A–T）→ TPDB `measurements` 第二段 → JavStash / StashDB `cup_size` | 独立字段，同时参与首段拼装，并转成 `E罩杯` 标签 |

**JavStash 三围兜底**：JavStash 的演员页会直接显示 `34E-24-35`，对应 GraphQL 的四个扁平字段
`band_size`(Int) / `cup_size`(String) / `waist_size`(Int) / `hip_size`(Int)——分项都在，
所以能原样拼成我们要的「底围+杯-腰-臀」，比 TPDB 那段要二次判断的原文更可靠，
排在 TPDB 之前。两个站是同一套 stash-box schema（旧的嵌套 `measurements` 和 `weight` 都已删除，
查询里带上会让**整条请求**校验失败，所以字段清单只能写扁平的这四项 + `breast_type`）。

**为什么 MDCx 排前面**：MDCx 的 `bust`/`waist`/`hip` 是分项字段，17,494 人三项齐备
（80.0%）；TPDB 是欧美库，只命中 46%。旧版只认 TPDB，等于白白扔掉 80% 的数据。

**首段怎么来的（`BandFromBust`）**：

```
下胸围 = 胸围 − 日标杯差          （112 − 32.5 = 79.5cm）
band   = (下胸围 + 11cm) / 2.54，取最近偶数   （→ 36）
结果   = band + 罩杯              （36J）
```

- **必须用日标杯差**（JIS L4005）：日标 J 差 32.5cm，美标 J 只差 25.4cm。换成美标差值
  反推出的 band 会系统性偏两码——实测 15,028 人里 85% 挤在 28-30，而日系演员的实际
  下胸围多是 65-75cm。
- `+11cm` 是日标转美标的放松量：日标 65/70/75cm 对应美标 30/32/34。
- **误差约一码**：日标罩杯算的是上胸围，而网站报的 `bust` 多是站姿胸围（小 2~3cm），
  反推出的下胸围因此略偏大。宁可差一码，也好过整条空着。
- 罩杯不在表里、或反推出的下胸围落在 55~120cm 之外（输入录错），首段退回纯胸围。

全库产出分布（14,865 个三项齐备 + 有罩杯的人）：band 30 占 45.5%、32 占 40.0%、
34 占 6.1%、28 占 5.7%，反推失败 0.2%。能写成「底围+杯」的占全库 67.9%。

**换算与语义的三个坑**：

1. `cmOrZero` 只接受 50~200 的值。上游偶尔混入 `34/24/35` 这种英寸串，一旦当厘米收下
   两者就分不开了，宁可留空。
2. 换算在 `MeasurementsInches()` 一处完成，抓取和 `--backfill` 共用，避免两边算出
   不同的数。
3. **TPDB 的第一段也有两种形态**：`34E-24-35`（底围+杯）和 `34-24-35`（胸围）。
   旧版把带字母的那种整条丢弃，理由是「34 是下胸围不是胸围」——现在首段本来就允许
   是底围+杯，所以原样保留，反而比丢掉有价值。

已删除：`bust_cm` / `waist_cm` / `hip_cm`——旧版把 TPDB 的英寸值当厘米存，口径是错的。

### 2.4 职业

| 字段 | 类型 | 来源优先级 | 说明 |
|---|---|---|---|
| `career_start` | int? | MDCx `career_period` 首年 → JavStash `career_start_year` → TPDB `extras.career_start_year` → 中文简介正则 | **出道年份**，填 Emby `ProductionYear` |

来源细节见第 3 节。

### 2.5 简介

| 字段 | 类型 | 来源优先级 | 说明 |
|---|---|---|---|
| `bio` | string? | 中文维基 → dramaface（短简介才升级）→ theactorbase（同上）→ 映人录 → 影中人 → jav-idol-db，**谁长用谁** | **不取英文简介，不调 DeepL** |
| `bio_lang` | string? | `zh` / `ja` | 中文维基常返回繁中 |

来源共 **6 个**，按读法分两类：

| 类型 | 源 | 做法 | 身份确认 |
|---|---|---|---|
| 按标题取正文 | 中文维基 | `zh.wikipedia` extracts | `ValidBio` + 身高对账 |
| 站内搜索 | **dramaface** | `/search.php?xml=yes` 拿 slug 再读详情 | 搜索结果的 `subject`（日文名-中文名-罗马字）逐段比对 |
| 猜 slug | theactorbase | 罗马字拼 `姓-名` / `名-姓`，最多 4 个 | 页面标题里的中文名/罗马字比对 |
| 站内搜索 + 详情页 | 映人录、影中人 | 搜名字 → 详情页 | 显示名/原名/罗马字逐个归一化比对 |
| 静态档案 | jav-idol-db | 读仓库里的 `profile.json` | 目录名（日文名）匹配 |

dramaface 很早就有站内搜索，以前只靠猜 slug：`Kana Momonogi` → `MomonogiKana` / `KanaMomonogi`
逐个试，短名还会退回末段（`Momo Sakata` → `Momo`）。现在先搜：

```
/search.php?q=三上悠亚&f=_all&m=yes&s=relevance&xml=yes
→ <doc percent="100%"><username>yuamikami</username>
   <subject><![CDATA[三上悠亜-三上悠亚-Yua Mikami]]></subject></doc>
```

`username` 就是详情页 slug，`subject` 是「日文名-中文名-罗马字」三段，正好用来核身份；
搜不到才退回猜 slug。每个演员最多搜 2 次 + 试 4 个 slug，比原来纯猜省请求。

theactorbase 的 slug 就是罗马字，但**词序不统一**（同一个站两种都有）：

```
三上悠亜   Mikami Yua    → /people/mikami-yua      （姓-名）
桃乃木かな Kana Momonogi  → /people/kana-momonogi   （名-姓）
松咲那奈   Matsuzaki Nana → /people/matsuzaki-nana  （姓-名）
```

它没有可用的站内搜索（`/search/<词>` 只返回空壳，结果由前端 JS 渲染），所以两种词序都试，
并且用页面标题（`三上悠亜（三上悠亚 / Mikami Yua）资料_作品`）里的中文名和罗马字核身份，
词序不同也算同一个人（`latinWordKey` 按词排序后比较）。

**简介只收中文**：`LooksChineseBio()` 要求汉字 ≥20 且汉字数 ≥3 倍假名数。
站上有英文条目（如 AIKA 的 `AIKA is a Japanese AV actress…`）和日文条目，
放进 Emby Overview 会中英混杂；夹少量假名的中文简介（`三上悠亚，原名鬼头桃菜（きとう ももな）…`）不受影响。


维基是**按标题查**而不是按人查，短名字会命中同名物，且看起来完全合法。实测
936 条里有 82% 的简介是垃圾：

```
もも  → 桃（學名：Prunus persica）是源自中国，蔷薇科李属的一种植物
DA    → Da或DA可能是指以下事物：deviantART 的常用缩写
David → 大衛王是公元前10世紀以色列聯合王國的第二任國王
```

所以 `bio` 写入前必须过 `ValidBio()`，四条全过才留（见 `internal/actorsync/bio.go`）：

| 检查 | 拦住了什么 |
|---|---|
| 不是消歧义页（`可能是指`/`可以指`/`指以下`/`是源于`…） | `JULIA` 的「Julia语言…」「あいり」的二十几行名单 |
| 含 AV 圈关键词（`AV`/`女优`/`写真偶像`/`グラビア`…） | 桃树、`アメリア・イヤハート`（美国航空先驱） |
| 不是 MDCx 属性串（`^身高:`/`^罩杯:`/`^三围:`…） | 「身高: 166cm \| 罩杯: D \| 三围: 84/60/86」，与 Overview 头部重复 |
| 不是在讲男性（`日本男`/`男優`/`男演员`…） | `つよぽん`→草彅剛、`そらる`→男性歌手 |

还有一条**跨字段对账**：简介里抽出来的身高与记录冲突超过 3cm 就丢简介。
实测 `Mio Fukada` 的简介写着「161cm 出生于 1995-11-12」，而 JavStash 给的是
`153cm / 1998-12-27` —— 抓到的是同名的另一个人。

**猜 slug 的源必须回头看标题对身份**（dramaface 现在已改成站内搜索优先，下面的历史问题
仍适用于「搜索没命中、退回猜 slug」的兜底路径）：dramaface 的 slug 是罗马字拼接，两个词的还会退回
「末段」当候选（`Momo Sakata` → `Momo`），而站上确实存在名字就叫 Momo 的别人。实测
`坂田もも` 因此拿回了「市井桃（ＭＯ☆ＭＯ）」的简介，同一段文字最终挂到 19 个名字带
「もも/桃」的人身上，全库这种成组重复的简介有 100 组、559 人。

现在 `dramaPageMatches()` 会用页面标题（`桃乃木香奈 (桃乃木かな,Kana Momonogi) 角色解析…`）
核对身份，两条任一成立才放行：标题里出现任一候选名；或 slug 够长（≥8 字符，即「姓+名」
拼接形态）且出现在标题里。代价是改名演员若别名没进候选名会被拒（宁可空着，不能错配）。

成组重复的存量简介由 `PurgeSharedBios()` 在每轮收尾（和 `--backfill`）里清理：
同一段文字出现 ≥2 次时，只保留名字出现在简介里的那条，其余清空等待重抓。

**先搜后读详情的源反过来必须精确对账**：yinrenlu / cinewho / jav-idol-db 都会把
页面上的显示名、原名（`person-native` / JSON-LD `name`）、罗马字、其他艺名与候选名
逐个归一化后比对，任一个完全一致才采信；搜索结果里同姓/同名但名字不一致的人直接跳过。
这类源因为身份已确认，简介不必再含 AV 圈关键词也能通过 `validCNBio()`
（jav-idol-db 的「2019年以MOODYZ专属身份出道…」就是这种情况）。

> 结果是简介覆盖率从「60%（含 82% 垃圾）」降到 **约 8%（纯净）**。空着比写着
> 「桃是蔷薇科植物」好 —— Overview 里那行元数据头已经把身高/罩杯/三围/出道年
> 都写了，简介本来也不该重复这些。
>
> MDCx 的 `minnano_bio`（覆盖 97.3%）已**完全弃用**：它是「身高: 166 \| 罩杯: D \|
> 三围: …」这种属性串（中位 61 字符、93% 含 `\|`、超 200 字的只有 17 条），
> 当简介是错的，当兜底也是错的。

新记录**到 theactorbase 为止**，不再往 MDCx 回落。

### 2.6 头像

| 字段 | 类型 | 来源优先级 | 说明 |
|---|---|---|---|
| `avatar` | string? | **gfriends 首选**，一次只取 1 张 → JavStash → TPDB → theactorbase | 原始直链 |
| `avatar_cn` | string? | 由 `avatar` 派生 jsdelivr 镜像 | 国内可访问地址，只有 gfriends 的图有 |
| `avatar_source_site` | string? | `gfriends` / `javstash` / `tpdb` / `theactorbase` | 溯源 |

gfriends 直链 `raw.githubusercontent.com/gfriends/gfriends/master/Content/<分类>/<名>.jpg`，
国内镜像 `cdn.jsdelivr.net/gh/gfriends/gfriends@master/Content/<分类>/<名>.jpg`。
文件名可能带 `AI-Fix-` 前缀（AI 修图版），同一个人两版都在时优先原图。

> **gfriends 索引里名字和文件的配对是坏的**，实测 36.8% 对不上：在
> `Content/<分类>` 下，`Filetree.json` 的 key 和 value 会被打乱（例：key 是
> `桃乃木かな.jpg`，value 却指向 `AI-Fix-松嶋真麻.jpg`；两张都下载肉眼确认过，
> 文件名叫什么就是谁，错的是配对）。但同一目录里 key 集合和 value 集合**完全相同**——
> 「有哪些文件」可信，「谁对应谁」不可信。所以实现上只拿 value 建「实际存在的文件名」
> 集合，**完全不使用 key 做名字映射**，既不张冠李戴，也不会拼出 404。
> `?t=<时间戳>` 只是缓存戳，取文件名时要剥掉。
>
> 另一层坑：索引顶层是 `{Content, Information}`，`Information` 里是数字
> （`TotalNum`/`TotalSize`/`Timestamp`），不能整体按三层 map 反序列化。

目录优先级（按实测分辨率与覆盖率排，见 `gfriendsDirOrder`）：
`0-Hand-Storage`（1235×1851，仅 89 人）→ `y-Minnano`（覆盖 58%，1049×1372）
→ `z-DMM(骑)`/`z-DMM(步)`/`y-AVDC`/`z-Derekhsu` → `9-Javrave`/`8-Warashi`（250×300 缩略图垫底）。
未列出的目录按条目数降序排。

**别名不参与 gfriends 匹配**：实测拿 MDCx 别名去补只多覆盖 0.6%（119/21,859），
而且样例多是同名不同人（`ASAMI(ロケット)`→`堤まほ`、`AYUMI(HimeMix)`→`Aoi`），
属于典型的「挂错人头像」，收益不值这个风险。


### 2.7 链接与溯源

| 字段 | 类型 | 来源 | 说明 |
|---|---|---|---|
| `homepage_url` | string? | JavStash `https://javstash.org/performers/<uuid>` → TPDB 演员页 | 填 Emby `HomePageUrl`，唯一的外链出口 |
| `provider_ids` | object | JavStash / StashDB / TPDB / Tmdb / Imdb / Wikidata / Wikipedia | |
| `status` `sources` `fail_count` `retry_after` `created_at` `scraped_at` | — | 程序自己维护 | 与 v2 一致 |

`status` 取值：`ok`（有生日或简介）／`partial`（命中源但两者都空）／`no_source`
（所有源都没这人）／`failed`（网络错误，下轮重试）／**`skipped`**（不是目标演员）。

`skipped` 目前只有一个触发条件：**性别为男**。字典来自 amane 映射表，里面混着男优
和同名的男艺人（`Panda`、`Kento Rabbit`、`Flash Brown`、`Isiah Maxwell`），实测占
**9.5%（19/200）**。这些人抓得到数据，但进演员库就是错的。标 `skipped` 而不是直接
丢弃，是为了两头兼顾：

- 记录留在 `actors.jsonl`，`plan.go` 只重试 `failed`/`no_source`，所以下次不会重复抓
- 不进 `actors.json`，下游 Emby 看不到

判定只能靠 StashDB 返回的 `gender` 字段——MDCx 的 `actor-profile` 里**没有性别字段**
（`website_fields` 只有 height / cup / birthday 等 24 种），离线阶段无从判断。

### 2.7.1 哪些记录会进下游

`actors.jsonl` 是真源，**五种状态全部保留**；`actors.json` 只导出 `ok` 和 `partial`
（`Actor.Exportable()`），另外三类不进：

| 状态 | 不进下游的原因 |
|---|---|
| `skipped` | 男性或非目标演员 |
| `no_source` | 所有源都查无此人——多是片商名、导演、系列名混进了字典 |
| `failed` | 这轮网络/限流没抓成，下轮会重试，此刻只是一行空壳 |

`ok` / `partial` 里还有一种也不导出：`name` 含下划线的老名字（`Kristina_Rose`、
`Lucy_Heart`）。`build_base_db.py` 现在会把下划线规范成空格，新名字会作为新人入库；
旧名若还留在下游，Emby 里就是同一个人的两条重复记录。真源仍保留它们备查。

留档是为了让 `plan.go` 认出「这个人已经试过了」，避免每轮重抓。真源与派生物的这层
分工，是本项目「代价只付一次」的核心——所以**不要为了表好看去删 `actors.jsonl` 里的行**。

### 2.8 标签

| 字段 | 类型 | 来源 | 说明 |
|---|---|---|---|
| `tags` | string[] | MDCx `actor-mapping.bio` 里的「标签:」串（**9,473 / 21,859 = 43.3%**） | 填 Emby `Tags` |

覆盖与分布（实测，21,859 人档案）：

- 9,473 人有标签，头部是 `熟女 3001`、`巨乳 2359`、`美人 1691`、`巨尻 884`、`美乳 872`、`美少女 684`。
- 有标签的人里人均 2.4 个，中位 2 个，最多 40 个；**≤8 个的占 99%**。
- 分隔符是 `,` / `，` / `、`，个别条目混用。
- 落到清单层（21,312 人）是 44.2%，落到进库记录（936 条实测）是 43.8%——三者口径一致，
  说明标签不依赖抓取成功与否，MDCx 命中就有。

两条处理规则：

1. **黑名单过滤**：剔除含 `未成年`、`幼児体型` 这类明确指向未成年人身体特征的词（原始数据里确有，不得进入媒体库元数据）。
2. **推 Emby 时截断到 8 个**，按全局词频从高到低取——高频词（巨乳、美人、長身）更通用，长尾词是全站只出现一两次的（如某些组合名），留着只会污染标签栏。JSONL 里保留全量。

**罩杯也进标签**（`E罩杯` / `J罩杯`）：

- 每个有罩杯的人都补一个 `X罩杯`，不管 MDCx 有没有给标签。没有 `cup_size`
  就从 `measurements` 首段反推（`34E-24-35` → `E`），反推不出来才留空。
- 放在标签列表**最前面**：第 2 条规则会按个数截断，罩杯放末尾会被切掉；
  放首位即使只剩 1 个标签也能保住。
- 幂等，大小写/空格不敏感（已有 `e罩杯` 就不再追加）；每轮收尾对全库跑一次，
  老记录不必等重抓也会补上。
- 线上 16,376 条导出记录里 11,625 条（71%）有罩杯，会全部带上这个标签。

---

## 3. 出道年份：数据源里到底有没有

**有，四个源都有字段，MDCx 覆盖最实在。**

| 源 | 字段 | 实测覆盖 | 语义 |
|---|---|---|---|
| MDCx profile | `website_fields.career_period` | **14,232 / 21,860 = 65.1%** | `2019年 -` / `1999年 - 2004年`，首年即出道年 |
| JavStash | `career_start_year: Int` | 字段在 schema 里，命中就带 | 精确的出道年，填 MDCx 的空位 |
| StashDB | `career_start_year: Int` | 同上（同一套 stash-box schema） | 精确的出道年；JavStash 链接里带 stashdb UUID 的人靠它补 |
| TPDB | `extras.career_start_year` | 待跑量统计 | 精确的出道年 |
| 中文维基 | 简介正文 `20XX年…出道` | 命中一部分 | 正则兜底 |

MDCx 的取值很干净：`'2021年-'`、`'2009年 -'`、`'2011~'` 这类占绝大多数，直接 `re.search(r'(19|20)\d{2}')` 取首段即可；
`'1999年 - 2004年'` 取 1999，正好是出道年。年份分布 1967–2025，没有脏值。

按既定规则「MDCx 优先」，最终取 MDCx `career_period` 首年；MDCx 没有的再依次问 JavStash、StashDB、TPDB、简介正则。
线上 16,376 条导出记录里 **10,860 条（66.3%）有出道年**——比 MDCx 单独的 65.1% 略高，
多出来的就是 JavStash / TPDB 补上的那批。

---

## 4. Emby `person.nfo` 映射

Emby Person 的官方元数据字段一共 12 项（语义以 dev.emby.media 的 Item-Information 原文为准），逐项对账：

| # | Emby 字段 | 官方语义 | 我们填什么 | 状态 |
|---|---|---|---|---|
| 1 | `Name` | 人物名 | `name`（中文名） | ✅ |
| 2 | `PremiereDate` | *For persons this is **date of birth*** | `birth_date`（JavStash 优先，MDCx 兜底） | ✅ |
| 3 | `ProductionYear` | 年份 | `career_start`（**出道年份**） | ✅ |
| 4 | `HomePageUrl` | 主页 | `homepage_url`（**JavStash 演员页**） | ✅ |
| 5 | `Overview` | 简介 | 一行元数据头 + `bio` 正文 | ✅ |
| 6 | `ProductionLocations` | *For Persons, this will have one entry and will contain their **place of birth*** | `birth_place` | ✅ |
| 7 | `ProviderIds` | 外部 ID 字典 | `provider_ids` | ✅ |
| 8 | `ImageTags` / 主图 | 主图 | `avatar` 落成 `folder.jpg` | ✅ |
| 9 | `Tags` | 标签 | `tags`（MDCx 标签，过滤后取前 8 个） | ✅ |
| 10 | `SortName` | 排序名 | 不写 | ➖ Emby 按 `Name` 排，中文正常 |
| 11 | `EndDate` | *For Persons this represents **death date*** | 不写 | ➖ 忌日不要 |
| 12 | `LockedFields` | 锁定字段 | 不写 | ➖ 留给你手改 |

**结论：12 项里 9 项有值、3 项有意留空。没有「想填但没源」的字段。**

### `Tags` 怎么用

MDCx 的标签是站点分类词，不是维基那种规范标签，但它本身有用——`巨乳`、`長身`、`苗条`、`美臀`
这类词在 Emby 里能直接当筛选条件用，比空着强。规则见 2.8 节：黑名单过滤 + 截断 8 个。

两点提醒：

1. 标签**简繁并存**，实测有 7 组同义冲突：`美乳輪 233 / 美乳轮 3`、`剛毛 224 / 刚毛 3`、
   `貧乳 137 / 贫乳 3`、`SM女優 17 / SM女优 1`、`陰毛`、`東京熱`、`無`，合计约 400 条。
   不归一会让同义词在 Emby 里裂成两个标签，用 amane 的 `zhcdict.json` 统一转简体即可（10242 条单字映射）。
   （注意：`長身` 只有繁体一种写法，没有 `长身`，别被直觉带偏。）
2. 标签只在 MDCx 有，**JavStash / TPDB 不提供**，所以 28% 的演员没有标签，这是数据源的边界，不是 bug。

### 第 10–12 项之外，还有一批信息是无处安放的

Emby Person **结构上就没有**身高、三围、罩杯、体重、别名字段。
这些不是"Emby 的某个字段我们没填"，而是**根本没有那个字段**，所以只能进 `Overview`，见下一节。

---

## 5. Overview 头部格式

```
【三围 32D-23-34｜身高 158cm｜体重 48kg｜出道 2019｜出身 东京都】

（简介正文）
```

规则：

1. **只有一行**，用 `｜` 分隔，五项固定顺序：三围 → 身高 → 体重 → 出道 → 出身。
2. 任何一项缺值，整项不出现，不留 `身高 -` 这种占位。
3. 五项全缺，就不写这一行，Overview 直接是纯简介。
4. 头部与正文之间空一行；`bio` 为空时，Overview 只有这一行，不留空行。
5. 不写别名、不写官网/社交——别名留在 JSONL，外链统一走 `HomePageUrl`。

实际渲染（数据全）：

```
【三围 32D-23-34｜身高 158cm｜体重 48kg｜出道 2019｜出身 东京都】

小湊よつ葉，日本女演员，1996年5月10日出生于东京都。2019年出道……
```

数据不全时：

```
【出道 2019｜出身 东京都】

小湊よつ葉，日本女演员……
```

> `出身` 与 `ProductionLocations` 同值，保留在头部是因为 Emby 的人物页不一定会展示
> `ProductionLocations`，而简介是必看的。若你嫌重复，删掉这一项即可，其他不受影响。

---

## 6. 明确不采集

| 字段 | 数据源有没有 | 不采的原因 |
|---|---|---|
| 血型 `blood_type` | MDCx bio 28.5% 有 | 不需要 |
| 星座 / 生肖 | theactorbase 有 | 不需要 |
| 忌日 `EndDate` | 全都查不到 | 活人用不上 |
| 英文简介 | TPDB 27% 有 | 只要中文，宁缺毋滥 |
| DeepL 翻译 | — | 已移除，机翻简介不如没有 |
| 事务所 `agency` | MDCx bio 的「事务所:」29.7%（4,699 条） | Emby 无字段，Overview 已精简到五项，塞不进去 |
| 官网 / Twitter | MDCx 17.6% / 15.7% | `HomePageUrl` 只放一个，已给 JavStash |
| 种族 `ethnicity`、眼色、发色、真假胸、纹身、穿孔、退役年、同名区分 | JavStash / TPDB 部分有 | Emby Person 无任何字段承载，纯增体积 |

最后一行是对标 StashDB `Performer` 全字段看下来缺的 8 项。它们的共同点是：
**Emby 侧没有任何出口**。要接的话得先有自定义展示层，现阶段不划算。

---

## 7. 已知缺口

| 缺口 | 现状 | 影响 |
|---|---|---|
| 体重 | TPDB 仅 13% | 多数人 Overview 头部没有体重 |
| 三围 | TPDB 90% | 日系演员覆盖尚可，欧美片源差 |
| 中文简介 | 维基 + theactorbase 合计覆盖率待跑量统计 | MDCx 字段串只能算「有字」，不算简介 |
| gfriends 索引错配 | key→value 配对 36.8% 错乱（集合本身完整） | 已绕开：不用 key 做映射，只取 value 里实际存在的文件名；代价是错配目录里那一版图被放弃，改由其他目录兜底 |
| gfriends 覆盖率 | 任意目录命中 66.5%（14,188 / 21,322） | 剩下 33.5% 退到数据源原图（JavStash/TPDB），画质与稳定性差一些 |
| JavStash 生日 / 出道年 | 字段存在，填充率未知 | 首轮跑量后统计；生日没有替代源，只能以 JavStash 为准 |
| `birth_place` 简繁/废值 | profile 出生地 48 个取值，79.8% 是都道府县（统一繁体「東京都」），另有 `海外` 1,929 条是废值；bio 的「出身」是简繁混杂（`东京都 1574` / `東京都 326`） | 入库前必须简繁归一 + 剔「海外/国外」，否则同一个人会出现两种出生地 |
| 出道年份 | MDCx 覆盖 65.1% | 剩下 35% 靠 JavStash/TPDB/正则补，仍有约两成查不到 |

---

## 8. 双向对账：JSONL 每个字段最后去了哪

正着查（Emby 要什么）见第 4 节，反着查（每个字段有没有出口）：

| JSONL 字段 | Emby 落点 |
|---|---|
| `name` | `Name` |
| `birth_date` | `PremiereDate` |
| `birth_place` | `ProductionLocations`（+ Overview 头部） |
| `career_start` | `ProductionYear`（+ Overview 头部） |
| `bio` | `Overview` 正文 |
| `height_cm` / `weight_kg` / `measurements` | `Overview` 头部（Emby 无对应字段；三围写成 `36J-25-39` 这种底围+罩杯） |
| `avatar` / `avatar_cn` | 主图，**必须落成 `folder.jpg`**，网络 URL 不能直接用 |
| `homepage_url` | `HomePageUrl` |
| `provider_ids` | `ProviderIds` |
| `tags` | `Tags`（黑名单过滤 → 简繁归一 → 截断 8 个） |
| `name_original` / `aliases` | 不落 Emby，只供检索（Overview 已定不要别名） |
| `gender` / `country` | 不落 Emby（`country` 信息已含在 `birth_place`） |
| `cup_size` | 不落 Emby（没有罩杯字段；但已拼进 `measurements` 首段，如 `36J`） |
| `key` / `status` / `sources` / `fail_count` / `retry_after` / `created_at` / `scraped_at` | 不落 Emby，内部记账 |

一句话：**11 个 JSONL 字段喂满了 Emby Person 全部 9 个可用字段，进不去的都是 Emby 没有地方放。**

唯一的实现前提是头像：Emby 的人物头像必须落地成 `metadata/people/<首字母>/<名>/folder.jpg` 或走 API 上传二进制，
只用网络 URL 在人物页会显示为空——这和「头像只存地址不下载」的旧设计冲突，下游要单独补一步下载。
