/* 站点数据加密锁：用密码派生 AES-GCM 密钥，解密 data/**\/*.json.enc。
 *
 * 与 scripts/encrypt_site.mjs 对应：
 *   - 固定盐 "actor-em-pages-1"（16 字节）
 *   - PBKDF2-HMAC-SHA256，200000 轮，32 字节密钥
 *   - 文件格式：[12B IV][密文][16B tag]
 *
 * 密码只存在内存（可选存 sessionStorage，关标签页即失效），不发往任何服务器。
 */
(function () {
  'use strict';

  var SALT = new TextEncoder().encode('actor-em-pages-1');
  var ITER = 200000;

  var cryptoKey = null;
  var password = sessionStorage.getItem('site_pw') || '';

  async function deriveKey(pw) {
    var base = await crypto.subtle.importKey(
      'raw', new TextEncoder().encode(pw), 'PBKDF2', false, ['deriveKey']
    );
    return crypto.subtle.deriveKey(
      { name: 'PBKDF2', salt: SALT, iterations: ITER, hash: 'SHA-256' },
      base, { name: 'AES-GCM', length: 256 }, false, ['decrypt']
    );
  }

  async function decryptBuffer(buf) {
    if (!cryptoKey) throw new Error('locked');
    var iv = new Uint8Array(buf, 0, 12);
    var data = new Uint8Array(buf, 12);
    var plain = await crypto.subtle.decrypt({ name: 'AES-GCM', iv: iv }, cryptoKey, data);
    return new TextDecoder().decode(plain);
  }

  // 统一的取数入口：所有数据都是 .enc，解密后 JSON.parse
  async function fetchData(path) {
    var resp = await fetch(path + '.enc', { cache: 'no-cache' });
    if (!resp.ok) throw new Error('缺少数据文件：' + path + '.enc');
    var buf = await resp.arrayBuffer();
    return JSON.parse(await decryptBuffer(buf));
  }

  // 用 meta.json 验证密码是否正确（解不开就是密码错）
  async function unlock(pw) {
    sessionStorage.removeItem('site_pw');
    cryptoKey = await deriveKey(pw);
    try {
      await fetchData('data/meta.json');
    } catch (e) {
      cryptoKey = null;
      return false;
    }
    password = pw;
    sessionStorage.setItem('site_pw', pw);
    return true;
  }

  function isUnlocked() { return cryptoKey !== null; }

  // 自动尝试上次会话的密码
  async function tryRestore() {
    if (!password) return false;
    return unlock(password);
  }

  window.SiteLock = {
    fetchData: fetchData,
    unlock: unlock,
    isUnlocked: isUnlocked,
    tryRestore: tryRestore,
  };
})();
