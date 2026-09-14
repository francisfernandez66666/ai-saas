#!/usr/bin/env node
// C5 WS 容量矩阵：对多个服务实例建立 N 个 /ws/client 连接，并用硬边界聊天事件验证 Redis 跨实例广播。
// 用法：MATRIX_BASES=http://127.0.0.1:9091,http://127.0.0.1:9092 MATRIX_N=1000 CUSTOMER_ID=1 VISITOR_KEY=... ./tools/ws_capacity_matrix.mjs
import http from 'node:http';

const bases = (process.env.MATRIX_BASES || 'http://127.0.0.1:9091,http://127.0.0.1:9092')
  .split(',')
  .map((x) => x.trim().replace(/\/$/, ''))
  .filter(Boolean);
const total = Number(process.env.MATRIX_N || 1000);
const holdMs = Number(process.env.HOLD_MS || 30000);
const connectConcurrency = Number(process.env.CONNECT_CONCURRENCY || 100);
const customerId = process.env.CUSTOMER_ID || '';
const visitorKey = process.env.VISITOR_KEY || '';
const tenantId = process.env.TENANT_ID || '1';
const broadcastTimeoutMs = Number(process.env.BROADCAST_TIMEOUT_MS || 20000);
const marker = process.env.BROADCAST_MARKER || `capacity-broadcast-${Date.now()}-高数题`;

if (!customerId || !visitorKey) {
  console.error('CUSTOMER_ID / VISITOR_KEY 必填');
  process.exit(2);
}
if (bases.length < 2) {
  console.error('C5 多实例矩阵至少需要两个 MATRIX_BASES');
  process.exit(2);
}

// parseWebSocketFrame 解析服务端→客户端未掩码帧，支持跨 TCP chunk 与扩展长度。
function createFrameParser(onText) {
  let buf = Buffer.alloc(0);
  return (chunk) => {
    buf = Buffer.concat([buf, chunk]);
    while (buf.length >= 2) {
      const fin = (buf[0] & 0x80) !== 0;
      const opcode = buf[0] & 0x0f;
      const masked = (buf[1] & 0x80) !== 0;
      let len = buf[1] & 0x7f;
      let offset = 2;
      if (len === 126) {
        if (buf.length < offset + 2) return;
        len = buf.readUInt16BE(offset);
        offset += 2;
      } else if (len === 127) {
        if (buf.length < offset + 8) return;
        const high = buf.readUInt32BE(offset);
        const low = buf.readUInt32BE(offset + 4);
        len = high * 0x100000000 + low;
        offset += 8;
      }
      if (masked) offset += 4;
      if (buf.length < offset + len) return;
      const payload = buf.subarray(offset, offset + len);
      buf = buf.subarray(offset + len);
      if ((opcode === 1 || opcode === 2) && fin) onText(payload.toString('utf8'));
      if (opcode === 8) return;
    }
  };
}

function upgrade(base, index) {
  return new Promise((resolve) => {
    const url = new URL(base);
    const path = `/api/v1/ws/client?customer_id=${encodeURIComponent(customerId)}&visitor_key=${encodeURIComponent(visitorKey)}`;
    const key = Buffer.from(`cap-${index}-${Date.now()}`.padEnd(16, '0').slice(0, 16)).toString('base64');
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
    const state = { socket: null, openedAt: 0, messages: [], closed: false };
    let parser = null;
    req.on('upgrade', (_res, socket) => {
      state.socket = socket;
      state.openedAt = Date.now();
      socket.setNoDelay(true);
      parser = createFrameParser((text) => {
        try { state.messages.push(JSON.parse(text)); } catch { state.messages.push({ raw: text }); }
      });
      socket.on('data', (chunk) => parser(chunk));
      socket.on('close', () => { state.closed = true; });
      socket.on('error', () => { state.closed = true; });
      resolve({ ok: true, socket, state });
    });
    req.on('response', (res) => {
      res.resume();
      resolve({ ok: false, status: res.statusCode });
    });
    req.on('error', (err) => resolve({ ok: false, error: err.message }));
    req.end();
  });
}

function percentile(values, q) {
  if (!values.length) return null;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * q))];
}

async function mapWithConcurrency(items, limit, mapper) {
  const results = [];
  let cursor = 0;
  async function worker() {
    while (cursor < items.length) {
      const idx = cursor++;
      results[idx] = await mapper(items[idx], idx);
    }
  }
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
  return results;
}

const targets = [];
for (let i = 0; i < total; i++) targets.push(bases[i % bases.length]);
const started = Date.now();
const openedByBase = Object.fromEntries(bases.map((b) => [b, 0]));
const failedByBase = Object.fromEntries(bases.map((b) => [b, 0]));
const allStates = [];
let opened = 0;
let failed = 0;

const connectResults = await mapWithConcurrency(targets, connectConcurrency, async (base, idx) => {
  const r = await upgrade(base, idx);
  if (r.ok) {
    opened++;
    openedByBase[base]++;
    allStates.push({ base, state: r.state });
  } else {
    failed++;
    failedByBase[base]++;
  }
  return r.ok ? Date.now() - started : null;
});
const connectLatencies = connectResults.filter((v) => typeof v === 'number');
const handshakeOk = opened === total && failed === 0;
const capacityMs = Date.now() - started;

let broadcast = { attempted: false, passed: false };
if (handshakeOk) {
  const listenerA = allStates.find((x) => x.base === bases[0]);
  const listenerB = allStates.find((x) => x.base === bases[1]);
  const sendBase = new URL(bases[0]);
  broadcast.attempted = true;
  const t0 = Date.now();
  const post = await new Promise((resolve, reject) => {
    const req = http.request({
      hostname: sendBase.hostname,
      port: sendBase.port,
      path: `/api/v1/chat/test?visitor_key=${encodeURIComponent(visitorKey)}`,
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Tenant-ID': tenantId,
      },
      timeout: 30000,
    }, (res) => {
      let body = '';
      res.setEncoding('utf8');
      res.on('data', (c) => { body += c; });
      res.on('end', () => resolve({ status: res.statusCode, body }));
    });
    req.on('error', reject);
    req.on('timeout', () => req.destroy(new Error('chat/test timeout')));
    req.end(JSON.stringify({ customer_id: Number(customerId), content: marker }));
  });
  const deadline = Date.now() + broadcastTimeoutMs;
  const hasMarker = (st) => st.messages.some((m) => JSON.stringify(m).includes(marker));
  while (Date.now() < deadline && !(hasMarker(listenerA.state) && hasMarker(listenerB.state))) {
    await new Promise((r) => setTimeout(r, 100));
  }
  const latencyMs = Date.now() - t0;
  const localOK = hasMarker(listenerA.state);
  const remoteOK = hasMarker(listenerB.state);
  broadcast = {
    attempted: true,
    passed: localOK && remoteOK && post.status === 200,
    local_ok: localOK,
    remote_ok: remoteOK,
    http_status: post.status,
    latency_ms: latencyMs,
    marker,
  };
}

// 保持一段时间观察稳定（可设 0 立即退出）
if (holdMs > 0) await new Promise((r) => setTimeout(r, holdMs));
for (const x of allStates) {
  try { x.state.socket.destroy(); } catch {}
}

const output = {
  generated_at: new Date().toISOString(),
  bases,
  requested: total,
  opened,
  failed,
  connect_concurrency: connectConcurrency,
  capacity_elapsed_ms: capacityMs,
  handshake_passed: handshakeOk,
  opened_by_base: openedByBase,
  failed_by_base: failedByBase,
  handshake_p50_ms: percentile(allStates.map((x) => x.state.openedAt - started), 0.5),
  handshake_p95_ms: percentile(allStates.map((x) => x.state.openedAt - started), 0.95),
  handshake_p99_ms: percentile(allStates.map((x) => x.state.openedAt - started), 0.99),
  broadcast,
};
console.log(JSON.stringify(output, null, 2));
process.exit(handshakeOk && broadcast.passed ? 0 : 1);
