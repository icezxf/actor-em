/**
 * actor-em Telegram 桥接 Worker
 *
 * 只做两件事：
 *   1. 接 Telegram webhook，校验 secret_token
 *   2. 把 update 转成 GitHub repository_dispatch，让 Actions 跑 --tg-once
 *
 * 真正的刮削、头像选取、choices 落盘都在 Actions 里完成（头像不转存 alist）。
 * Worker 不存任何状态，也不需要 KV / D1 / R2。
 */

interface Env {
  TELEGRAM_BOT_TOKEN: string; // Secret
  TELEGRAM_WEBHOOK_SECRET: string; // Secret，Telegram 回调时校验
  SETUP_KEY: string; // Secret，调 /setup 注册 webhook 时用
  GH_TOKEN: string; // Secret，需要 repo 权限的 PAT
  GH_REPO: string; // 例 icezxf/actor-em
  ALLOWED_CHAT_ID?: string; // 可选：只服务这个会话
  WEBHOOK_URL?: string; // 可选：显式指定回调地址，默认用本次请求 origin
  // 静态站选图接口用：与 Pages 数据加密同一个密码，用来校验 HMAC 签名
  SITE_PASSWORD?: string;
}

const HEADER = "x-telegram-bot-api-secret-token";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json; charset=utf-8" },
  });
}

function equal(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

async function telegram(env: Env, method: string, body: Record<string, unknown>) {
  const resp = await fetch(
    `https://api.telegram.org/bot${env.TELEGRAM_BOT_TOKEN}/${method}`,
    {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    },
  );
  const data = (await resp.json()) as { ok: boolean; description?: string };
  return data;
}

async function dispatch(env: Env, update: Record<string, unknown>) {
  const raw = JSON.stringify(update);
  const resp = await fetch(
    `https://api.github.com/repos/${env.GH_REPO}/dispatches`,
    {
      method: "POST",
      headers: {
        authorization: `Bearer ${env.GH_TOKEN}`,
        accept: "application/vnd.github+json",
        "user-agent": "actor-em-tg-bridge",
        "x-github-api-version": "2022-11-28",
      },
      body: JSON.stringify({
        event_type: "actor-command",
        // client_payload 顶层不超过 10 个字段、总大小 65KB；把 update 压成一个字符串最稳
        client_payload: {
          update: raw,
          update_id: String(update.update_id ?? ""),
        },
      }),
    },
  );
  if (!resp.ok) {
    return { ok: false, status: resp.status, body: await resp.text() };
  }
  return { ok: true };
}

// ---------- 静态站「换头像」：免 issue 直接触发 ----------
//
// 页面点选后用站点密码算 HMAC-SHA256 签名（不发明文密码），这里校验后
// 触发 GitHub repository_dispatch（avatar-pick），由 Actions 写库并重建页面。

function bytesToB64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let s = "";
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
  return btoa(s);
}

async function hmacB64(secret: string, msg: string): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  return bytesToB64(await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(msg)));
}

/** 常量时间比较，避免按字节提前返回泄露信息 */
function safeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

async function handlePick(request: Request, env: Env): Promise<Response> {
  if (request.method !== "POST") return json({ ok: false, error: "POST only" }, 405);
  if (!env.SITE_PASSWORD) return json({ ok: false, error: "SITE_PASSWORD not set" }, 503);
  if (!env.GH_TOKEN || !env.GH_REPO) return json({ ok: false, error: "GH not configured" }, 503);

  let body: { key?: string; image?: string; ts?: number; sig?: string };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return json({ ok: false, error: "invalid json" }, 400);
  }
  const key = (body.key || "").trim();
  const image = (body.image || "").trim();
  const ts = Number(body.ts || 0);
  const sig = (body.sig || "").trim();
  if (!key || !image || !image.startsWith("https://")) {
    return json({ ok: false, error: "key/image required (image must be https)" }, 400);
  }
  // 时间窗 10 分钟，防重放
  if (!ts || Math.abs(Date.now() / 1000 - ts) > 600) {
    return json({ ok: false, error: "stale timestamp" }, 401);
  }

  const expected = await hmacB64(env.SITE_PASSWORD, `${key}|${image}|${ts}`);
  if (!sig || !safeEqual(sig, expected)) {
    return json({ ok: false, error: "bad signature" }, 401);
  }

  const resp = await fetch(`https://api.github.com/repos/${env.GH_REPO}/dispatches`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${env.GH_TOKEN}`,
      accept: "application/vnd.github+json",
      "user-agent": "actor-em-pages-pick",
      "x-github-api-version": "2022-11-28",
    },
    body: JSON.stringify({
      event_type: "avatar-pick",
      client_payload: { key, image },
    }),
  });
  if (!resp.ok) {
    return json({ ok: false, error: `github ${resp.status}: ${(await resp.text()).slice(0, 200)}` }, 502);
  }
  return json({ ok: true, key });
}

const CORS_HEADERS = {
  "access-control-allow-origin": "https://icezxf.github.io",
  "access-control-allow-methods": "POST, OPTIONS",
  "access-control-allow-headers": "content-type",
};

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);

    // 健康检查
    if (url.pathname === "/" || url.pathname === "/health") {
      return json({ ok: true, service: "actor-em-tg-bridge" });
    }

    // /setup：GET 看状态，POST 注册 webhook；都要带 Authorization: Bearer <SETUP_KEY>
    // 静态站换头像（跨域，需 CORS）
    if (url.pathname === "/pick-avatar") {
      if (request.method === "OPTIONS") {
        return new Response(null, { status: 204, headers: CORS_HEADERS });
      }
      const res = await handlePick(request, env);
      for (const [k, v] of Object.entries(CORS_HEADERS)) res.headers.set(k, v);
      return res;
    }

    if (url.pathname === "/setup") {
      if (!env.SETUP_KEY || request.headers.get("authorization") !== `Bearer ${env.SETUP_KEY}`) {
        return json({ ok: false, error: "unauthorized" }, 401);
      }
      if (request.method === "GET") {
        return json(await telegram(env, "getWebhookInfo", {}));
      }
      if (request.method !== "POST") return json({ ok: false, error: "GET or POST only" }, 405);

      const hook = env.WEBHOOK_URL || `${url.origin}/telegram`;
      const res = await telegram(env, "setWebhook", {
        url: hook,
        secret_token: env.TELEGRAM_WEBHOOK_SECRET,
        allowed_updates: ["message", "callback_query"],
        drop_pending_updates: true,
      });
      if (!res.ok) return json({ ok: false, error: res.description ?? "setWebhook failed" }, 502);
      return json({ ok: true, webhook: hook });
    }

    // Telegram 回调
    if (url.pathname !== "/telegram") return json({ ok: false, error: "not found" }, 404);
    if (request.method !== "POST") return json({ ok: false, error: "POST only" }, 405);

    const provided = request.headers.get(HEADER) ?? "";
    if (!env.TELEGRAM_WEBHOOK_SECRET || !equal(provided, env.TELEGRAM_WEBHOOK_SECRET)) {
      return json({ ok: false, error: "invalid secret" }, 401);
    }

    let update: Record<string, any>;
    try {
      update = (await request.json()) as Record<string, any>;
    } catch {
      return json({ ok: false, error: "invalid json" }, 400);
    }

    const text = update.message?.text as string | undefined;
    const callback = update.callback_query?.data as string | undefined;
    if (!text && !callback) return json({ ok: true, ignored: "unsupported update" });

    const chatID = String(
      update.callback_query?.message?.chat?.id ?? update.message?.chat?.id ?? "",
    );
    if (env.ALLOWED_CHAT_ID && chatID !== env.ALLOWED_CHAT_ID) {
      return json({ ok: true, ignored: "chat not allowed" });
    }

    if (JSON.stringify(update).length > 60000) {
      return json({ ok: false, error: "update too large" }, 413);
    }

    const out = await dispatch(env, update);
    if (!out.ok) {
      // 返回非 2xx，Telegram 会重试；Actions 失败也会在仓库里留下记录
      return json({ ok: false, error: "dispatch failed", status: out.status, detail: out.body?.slice(0, 300) }, 502);
    }
    return json({ ok: true });
  },
};
