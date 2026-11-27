/**
 * actor-em 静态站选图桥接 Worker
 *
 * 只做一件事：验证网页选图请求的 HMAC 签名，然后触发 GitHub Actions
 * (avatar-pick) 把选中的图片写进数据库并重建页面。
 * Telegram 交互已移除；Worker 不存状态，也不需要 KV / D1 / R2。
 */

interface Env {
  SITE_PASSWORD?: string; // Secret，与 Pages 数据加密同一密码
  GH_TOKEN: string;       // Secret，repo 权限 PAT
  GH_REPO: string;        // 例 icezxf/actor-em
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json; charset=utf-8" },
  });
}

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

function safeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

async function dispatchPick(env: Env, key: string, image: string): Promise<void> {
  const resp = await fetch(`https://api.github.com/repos/${env.GH_REPO}/dispatches`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${env.GH_TOKEN}`,
      accept: "application/vnd.github+json",
      "user-agent": "actor-em-pages-pick",
      "x-github-api-version": "2022-11-28",
    },
    body: JSON.stringify({ event_type: "avatar-pick", client_payload: { key, image } }),
  });
  if (!resp.ok) throw new Error(`github ${resp.status}: ${(await resp.text()).slice(0, 200)}`);
}

async function handlePick(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
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

  // 校验通过，立即返回；dispatch 交给后台（GitHub API 要几秒，不该让用户等）
  ctx.waitUntil(
    dispatchPick(env, key, image).catch((e) => console.error("dispatch failed", String(e))),
  );
  return json({ ok: true, key, queued: true }, 202);
}

const CORS_HEADERS = {
  "access-control-allow-origin": "https://icezxf.github.io",
  "access-control-allow-methods": "POST, OPTIONS",
  "access-control-allow-headers": "content-type",
};

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);

    if (url.pathname === "/" || url.pathname === "/health") {
      return json({ ok: true, service: "actor-em-pages-pick" });
    }
    if (url.pathname === "/pick-avatar") {
      if (request.method === "OPTIONS") {
        return new Response(null, { status: 204, headers: CORS_HEADERS });
      }
      const res = await handlePick(request, env, ctx);
      for (const [k, v] of Object.entries(CORS_HEADERS)) res.headers.set(k, v);
      return res;
    }
    return json({ ok: false, error: "not found" }, 404);
  },
};
