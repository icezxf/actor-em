#!/usr/bin/env node
/**
 * 把静态站的 data/**\/*.json 加密成 .enc，前端用密码解密后使用。
 *
 * 为什么：GitHub Pages 没有访问控制，猜到 URL 就能下载 data 下的 JSON。
 * 这里用 AES-256-GCM 加密数据本身——页面代码仍公开，但没有密码只能拿到密文。
 *
 * 文件格式： [12 字节 IV][密文][16 字节 GCM tag]
 *   盐是固定的（见 SALT），IV 逐文件随机。这样前端只用派生一次密钥。
 *
 * 密钥派生： PBKDF2-HMAC-SHA256(password, salt, 200000) → 32 字节
 *   与前端 Web Crypto 完全一致的参数（见 site_src/assets/app.js 的 deriveKey）。
 *
 * 密码：环境变量 SITE_PASSWORD（CI 里来自 GitHub Secret）。不写仓库、不打印。
 *
 * 用法：
 *   SITE_PASSWORD=xxxx node scripts/encrypt_site.mjs
 *   node scripts/encrypt_site.mjs --dry-run
 */

import { pbkdf2Sync, randomBytes, createCipheriv } from "node:crypto";
import { readdirSync, readFileSync, writeFileSync, unlinkSync, statSync, mkdirSync } from "node:fs";
import { join, dirname, relative } from "node:path";

// 固定盐：让前端只需派生一次密钥（256 个分片各跑一次 PBKDF2 会非常慢）。
// 盐的作用是防彩虹表，固定值 + 200k 迭代仍是标准强度；IV 仍逐文件随机。
const SALT = Buffer.from("actor-em-pages-1");   // 16 字节
const IV_LEN = 12;
const ITER = 200_000;
const KEY_LEN = 32;

const args = process.argv.slice(2);
const dryRun = args.includes("--dry-run");
const siteIdx = args.indexOf("--site");
const SITE = siteIdx >= 0 ? args[siteIdx + 1] : "site";

function walk(dir, out = []) {
  for (const ent of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, ent.name);
    if (ent.isDirectory()) walk(p, out);
    else if (ent.name.endsWith(".json")) out.push(p);
  }
  return out;
}

function encryptOne(file, password) {
  const plain = readFileSync(file);
  const iv = randomBytes(IV_LEN);
  const key = pbkdf2Sync(password, SALT, ITER, KEY_LEN, "sha256");
  const cipher = createCipheriv("aes-256-gcm", key, iv);
  const ct = Buffer.concat([cipher.update(plain), cipher.final()]);
  const tag = cipher.getAuthTag();
  const out = file + ".enc";
  writeFileSync(out, Buffer.concat([iv, ct, tag]));
  unlinkSync(file); // 明文不留
  return statSync(out).size;
}

function main() {
  const password = (process.env.SITE_PASSWORD || "").trim();
  if (!dryRun) {
    if (!password) {
      console.error("缺少 SITE_PASSWORD 环境变量（不落盘、不打印）");
      process.exit(1);
    }
    if (password.length < 8) {
      console.error("SITE_PASSWORD 至少 8 位");
      process.exit(1);
    }
  }

  let files;
  try {
    files = walk(SITE);
  } catch {
    console.error(`站点目录不存在：${SITE}`);
    process.exit(1);
  }
  if (!files.length) {
    console.error("没有找到要加密的 JSON");
    process.exit(1);
  }

  const totalIn = files.reduce((n, f) => n + statSync(f).size, 0);
  console.error(`待加密 ${files.length} 个文件，共 ${(totalIn / 1048576).toFixed(1)} MB`);
  if (dryRun) {
    for (const f of files.slice(0, 10)) {
      console.error(`  ${relative(SITE, f)} (${(statSync(f).size / 1024).toFixed(0)} KB)`);
    }
    if (files.length > 10) console.error(`  … 还有 ${files.length - 10} 个`);
    return;
  }

  let done = 0;
  let outBytes = 0;
  for (const f of files) {
    outBytes += encryptOne(f, password);
    done++;
    if (done % 50 === 0) console.error(`  ${done}/${files.length}`);
  }
  console.error(
    `已加密 ${done} 个文件 → ${(outBytes / 1048576).toFixed(1)} MB（含 IV 与 tag）`,
  );
}

main();
