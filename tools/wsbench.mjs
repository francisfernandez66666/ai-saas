#!/usr/bin/env node
// C5 WebSocket 容量辅助：用 Node 原生 http upgrade 建立 N 个 ws/client 连接，
// 统计握手成功数、失败数、P95/P99 握手耗时。默认只跑 20，避免命中 IPRateLimit(ws_client 20/min)。
// 用法：WS_N=20 WS_HOLD_MS=1000 CUSTOMER_ID=... VISITOR_KEY=... node tools/wsbench.mjs
import http from 'node:http';

const base = process.env.WS_BASE || 'http://localhost:9090';
const url = new URL(base);
const n = Number(process.env.WS_N || 20);
const holdMs = Number(process.env.WS_HOLD_MS || 1000);
const customerId = process.env.CUSTOMER_ID || '';
const visitorKey = process.env.VISITOR_KEY || '';
if (!customerId || !visitorKey) {
  console.error('CUSTOMER_ID / VISITOR_KEY 必填');
  process.exit(1);
}
if (!['http:', 'https:'].includes(url.protocol)) {
  console.error('WS_BASE 必须是 http/https');
  process.exit(1);
}

const paths = [];
for (let i = 0; i < n; i++) {
  paths.push(`/api/v1/ws/client?customer_id=${encodeURIComponent(customerId)}&visitor_key=${encodeURIComponent(visitorKey)}`);
}

const started = [];
const sockets = [];
let opened = 0;
let failed = 0;

function handshake(path, idx) {
  const t0 = Date.now();
  const key = Buffer.from(`wsbench-${idx}-${Date.now()}`.padEnd(16, '0').slice(0, 16)).toString('base64');
  const req = http.request({
    hostname: url.hostname,
    port: url.port || (url.protocol === 'https:' ? 443 : 80),
    path,
    method: 'GET',
    headers: {
      Host: url.host,
      Connection: 'Upgrade',
      Upgrade: 'websocket',
      'Sec-WebSocket-Version': '13',
      'Sec-WebSocket-Key': key,
    },
    timeout: 15000,
  });
  req.on('upgrade', (_res, socket) => {
    started[idx] = Date.now() - t0;
    opened++;
    socket.on('data', () => {});
    socket.setKeepAlive(true);
    sockets.push(socket);
  });
  req.on('response', (res) => {
    started[idx] = -1;
    failed++;
    res.resume();
  });
  req.on('error', () => {
    started[idx] = -1;
    failed++;
  });
  req.end();
}

for (const [idx, p] of paths.entries()) handshake(p, idx);

await new Promise((resolve) => setTimeout(resolve, holdMs));
for (const s of sockets) {
  try { s.destroy(); } catch {}
}

const ok = started.filter((v) => v >= 0).sort((a, b) => a - b);
const pct = (q) => ok.length ? ok[Math.min(ok.length - 1, Math.floor(ok.length * q))] : null;
const out = {
  target: `${url.href.replace(/\/$/, '')}/api/v1/ws/client`,
  requested: n,
  opened,
  failed,
  hold_ms: holdMs,
  p50_ms: pct(0.50),
  p95_ms: pct(0.95),
  p99_ms: pct(0.99),
  max_ms: ok.length ? ok[ok.length - 1] : null,
};
console.log(JSON.stringify(out));
