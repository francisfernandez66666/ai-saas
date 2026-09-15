#!/usr/bin/env node
// T7 契约检查（FE 调用路径 ↔ 后端路由清单孤儿检测）
// 用法：node scripts/check_api_contract.mjs <routes-file>
// routes-file：`METHOD /path` 每行一条（apidump -format paths 输出）
//
// G2 修复(2026-09-14)：
//   1) 比对键改为 `METHOD path`——旧实现第 18 行把 METHOD 剥掉再比，
//      GET/POST 打反（如把 POST /chat/request-human 误当已有 GET）完全检不出。
//   2) 新增"BE 有 / FE 无"反向清单（先报告不 fail，随 F1-F5 消化后再转 fail）。
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

const routesFile = process.argv[2]
if (!routesFile) {
  console.error('用法: node scripts/check_api_contract.mjs <routes-file>')
  process.exit(2)
}

// 解析成 {method, path}
const routes = readFileSync(routesFile, 'utf8')
  .split('\n')
  .map((l) => l.trim())
  .filter(Boolean)
  .map((l) => {
    const m = l.match(/^([A-Z]+)\s+(\/\S+)$/)
    return m ? { method: m[1], path: m[2].split('?')[0] } : null
  })
  .filter(Boolean)

// 每个路由编译成正则：:param → [^/]+，末尾允许拼接动态段（如 '/api/v1/org/users/' + id）
function toRe(p) {
  const body = p
    .split('/')
    .map((seg) => (seg.startsWith(':') ? '[^/]+' : seg.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
    .join('/')
  return new RegExp('^' + body + '(/[^/]*)?$')
}
// path → 该路径支持的方法集合 + 编译正则
const byPath = new Map()
for (const r of routes) {
  if (!byPath.has(r.path)) byPath.set(r.path, { re: toRe(r.path), methods: new Set() })
  byPath.get(r.path).methods.add(r.method)
}
const entries = [...byPath.entries()]

const FILE_RE = /\.(ts|tsx)$/
function walk(dir) {
  const out = []
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist' || name.startsWith('.')) continue
    const full = join(dir, name)
    const st = statSync(full)
    if (st.isDirectory()) {
      if (name === '__tests__') continue // 测试夹具故意用假路径，不参与契约检查
      out.push(...walk(full))
    } else if (FILE_RE.test(name)) out.push(full)
  }
  return out
}

// 前端常见的"前缀常量"（API = '/api/v1' 再拼具体路径），本身不是端点
const BASE_PREFIXES = new Set(['/', '/api', '/api/v1', '/openapi', '/api/v1/admin', '/api/v1/advisor', '/api/v1/super', '/api/v1/org', '/api/v1/cdp', '/api/v1/tenant', '/api/v1/auth', '/api/v1/privacy'])

// 提取源码里出现的 /api/v1、/openapi、/ws 路径字面量（含模板串起始部分）
const PATH_RE = /['"`](\/(?:api\/v1|openapi|health|status|metrics|ws)[^'"`\s]*)/g
// 就近推断 HTTP 方法：路径 token 之后 160 字符内的 method 关键字，默认 GET
const METHOD_RE = /method\s*[:=]\s*['"`]([A-Z]+)['"`]/
const VERB_FN_RE = /\b(post|put|del|delete|patch)\s*\(/i

// 记录 FE 引用到的 `METHOD path`，用于正向孤儿 + 方法错配 + 反向清单
const referenced = new Set()
const orphans = new Set()
const methodMismatch = new Set()

for (const file of walk('frontend-react/src')) {
  const text = readFileSync(file, 'utf8')
  for (const m of text.matchAll(PATH_RE)) {
    let raw = m[1]
    raw = raw.replace(/\$\{[^}]*\}/g, 'X') // 完整模板段 → 占位
    raw = raw.replace(/\?.*$/, '') // 去 query
    // 拼接动态尾段：'/x/' + id 归一化为 '/x/X'（否则误判成列表路由的方法）
    let p = raw
    // 以 '/' 结尾说明路径靠字符串拼接动态段（'/x/' + id + '/branding'），
    // 拼出的静态前缀会丢掉变量之后的尾段，方法归属不可靠——此类只做存在性(命中即不孤儿)，不判方法。
    const dynamicConcat = raw.endsWith('/')
    if (p.endsWith('/')) p = p + 'X'
    p = p.replace(/\/+$/, '') || '/'
    // 残留未闭合模板无法可靠解析，跳过
    if (p.includes('${') || p.includes('{')) continue
    if (BASE_PREFIXES.has(p)) continue
    const hits = entries.filter(([, { re }]) => re.test(p))
    if (hits.length === 0) {
      orphans.add(`${p}  (${file})`)
      continue
    }
    const supported = new Set()
    const canonical = []
    for (const [path, { methods }] of hits) {
      for (const meth of methods) supported.add(meth)
      canonical.push(path)
    }
    const tail = text.slice(m.index, m.index + 200)
    let meth = 'GET'
    const mm = tail.match(METHOD_RE)
    const vf = tail.match(VERB_FN_RE)
    if (mm) meth = mm[1].toUpperCase()
    else if (vf) meth = vf[1].toUpperCase() === 'DEL' ? 'DELETE' : vf[1].toUpperCase()
    for (const path of canonical) referenced.add(`${meth} ${path}`)
    // 动态拼接路径不判方法（避免静态前缀误报），仅静态完整路径参与方法级校验
    if (!dynamicConcat && !supported.has(meth)) {
      methodMismatch.add(`${meth} ${p}  (命中路由支持 ${[...supported].join('/')})  (${file})`)
    }
  }
}

let fail = 0
if (orphans.size) {
  for (const o of [...orphans].sort()) console.log('  ORPHAN ' + o)
  fail = 1
}
if (methodMismatch.size) {
  for (const o of [...methodMismatch].sort()) console.log('  METHOD-MISMATCH ' + o)
  fail = 1
}

// 反向清单：BE 有路由但 FE 零引用（报告不 fail——许多是超管/OpenAPI/回调端点，前端本就不调）
const reverse = []
for (const [path, { methods }] of entries) {
  for (const meth of methods) {
    if (!referenced.has(`${meth} ${path}`)) reverse.push(`${meth} ${path}`)
  }
}
if (reverse.length) {
  console.log(`  INFO  BE有/FE无（${reverse.length} 条，回调/超管/OpenAPI 属正常，暂不 fail）`)
  for (const r of reverse.sort()) console.log('    ' + r)
}

process.exit(fail)
