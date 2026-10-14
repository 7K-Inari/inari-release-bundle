#!/usr/bin/env node
// Single-origin shim for the kind UI e2e (scripts/e2e/ui Playwright suite).
//
// In production the Gateway routes /api/v1 and / on the console's host, so
// the SPA talks to the API same-origin. In kind there is no gateway: the
// console and the server are separate port-forwards, and the server sends no
// CORS headers (never needed in production), so a browser on the console
// port could not call the API port. This shim listens on one port and routes
// /api/* to the server forward and everything else to the console forward,
// reproducing the production topology for the browser.
//
// Env:
//   LISTEN_PORT      port to listen on (default 8080)
//   CONSOLE_UPSTREAM console port-forward base URL (default http://127.0.0.1:18080)
//   API_UPSTREAM     server port-forward base URL  (default http://127.0.0.1:18090)

import http from "node:http";

const LISTEN = Number(process.env.LISTEN_PORT || 8080);
const CONSOLE_UPSTREAM = new URL(process.env.CONSOLE_UPSTREAM || "http://127.0.0.1:18080");
const API_UPSTREAM = new URL(process.env.API_UPSTREAM || "http://127.0.0.1:18090");

const HOP_BY_HOP = new Set([
  "connection",
  "keep-alive",
  "transfer-encoding",
  "upgrade",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
]);

function proxy(req, res, upstream) {
  const out = http.request(
    {
      hostname: upstream.hostname,
      port: upstream.port,
      path: req.url,
      method: req.method,
      headers: { ...req.headers, host: upstream.host },
      // No keep-alive to the port-forwards: a kubectl port-forward stream
      // that died silently would otherwise be reused, hanging the request
      // until TCP timeout (observed as multi-minute browser stalls). A fresh
      // socket per request fails fast against a dead forward, and the
      // supervisor loop in e2e-stack.yaml restarts the forward.
      agent: new http.Agent({ keepAlive: false }),
      // Fail fast instead of hanging forever on a stalled stream: the
      // browser's fetch rejects, the app's error state renders, and the
      // next poll/suite retry re-attempts against the restarted forward.
      timeout: 30_000,
    },
    (up) => {
      const headers = {};
      for (const [k, v] of Object.entries(up.headers)) {
        if (!HOP_BY_HOP.has(k)) headers[k] = v;
      }
      res.writeHead(up.statusCode ?? 502, headers);
      up.pipe(res);
    },
  );
  out.on("timeout", () => {
    out.destroy(new Error("ui-proxy upstream timeout"));
  });
  out.on("error", (err) => {
    // Headers may already be flushed (stall mid-body): never writeHead
    // twice — that throws ERR_HTTP_HEADERS_SENT and kills the process.
    if (res.headersSent) {
      res.destroy(err);
      return;
    }
    res.writeHead(502, { "content-type": "text/plain" });
    res.end(`ui-proxy upstream error: ${err.message}`);
  });
  req.pipe(out);
}

http
  .createServer((req, res) => {
    const upstream = req.url.startsWith("/api/") ? API_UPSTREAM : CONSOLE_UPSTREAM;
    proxy(req, res, upstream);
  })
  .listen(LISTEN, "127.0.0.1", () => {
    console.log(
      `ui-proxy: 127.0.0.1:${LISTEN}  /api/* -> ${API_UPSTREAM.origin}  /* -> ${CONSOLE_UPSTREAM.origin}`,
    );
  });
