#!/usr/bin/env node
// T7 契约检查（FE 调用路径 ↔ 后端路由清单孤儿检测）
// 用法：node scripts/check_api_contract.mjs <routes-file>
// routes-file：`METHOD /path` 每行一条（apidump -format paths 输出）
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

const routesFile = process.argv[2]
if (!routesFile) {
  console.error('用法: node scripts/check_api_contract.mjs <routes-file>')
  process.exit(2)
}

const routes = readFileSync(routesFile, 'utf8')
  .split('\n')
  .map((l) => l.trim())
  .filter(Boolean)
  .map((l) => l.replace(/^[A-Z]+\s+/, ''))
  .map((p) => p.split('?')[0])

// 每个路由编译成正则：:param → [^/]+，末尾允许拼接动态段（如 '/api/v1/org/users/' + id）
const patterns = routes.map((p) => {
  const body = p
    .split('/')
    .map((seg) => (seg.startsWith(':') ? '[^/]+' : seg.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
    .join('/')
  return new RegExp('^' + body + '(/[^/]*)?$')
})

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

// 前端常见的“前缀常量”（API = '/api/v1' 再拼具体路径），本身不是端点
const BASE_PREFIXES = new Set(['/', '/api', '/api/v1', '/openapi', '/api/v1/admin', '/api/v1/advisor', '/api/v1/super', '/api/v1/org', '/api/v1/cdp', '/api/v1/tenant'])

// 提取源码里出现的 /api/v1、/openapi、/ws 路径字面量（含模板串起始部分）
const PATH_RE = /['"`](\/(?:api\/v1|openapi|health|status|metrics|ws)[^'"`\s]*)/g

const orphans = new Set()
for (const file of walk('frontend-react/src')) {
  const text = readFileSync(file, 'utf8')
  for (const m of text.matchAll(PATH_RE)) {
    let p = m[1]
    // 模板/拼接动态部分归一化：${...} → /X
    p = p.replace(/\$\{[^}]*\}/g, 'X')
    p = p.replace(/\?.*$/, '') // 去 query
    p = p.replace(/\/+$/, '') || '/'
    if (BASE_PREFIXES.has(p)) continue
    // ws URL 形如 /api/v1/ws/client，已在路由表
    if (!patterns.some((re) => re.test(p))) {
      orphans.add(`${p}  (${file})`)
    }
  }
}

if (orphans.size) {
  for (const o of [...orphans].sort()) console.log('    ' + o)
}
