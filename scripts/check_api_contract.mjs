#!/usr/bin/env node
// T7 契约检查（FE 调用路径 ↔ 后端路由清单孤儿检测）
// 用法：node scripts/check_api_contract.mjs <routes-file>
//       node scripts/check_api_contract.mjs --selftest   （FIX-C 跨文件前缀解析的反证用例，见文件尾）
// routes-file：`METHOD /path` 每行一条（apidump -format paths 输出）
//
// G2 修复(2026-09-14)：
//   1) 比对键改为 `METHOD path`——旧实现第 18 行把 METHOD 剥掉再比，
//      GET/POST 打反（如把 POST /chat/request-human 误当已有 GET）完全检不出。
//   2) 新增"BE 有 / FE 无"反向清单（先报告不 fail，随 F1-F5 消化后再转 fail）。
//
// FIX-C (2026-09-28)：前缀常量解析从"本文件"扩到"跨文件"——
//   顾问工作台的前缀定义在 pages/advisor/shared.ts 的 `export const API = '/api/v1/advisor'`，
//   Advisor.tsx 经 `import { API } from './advisor/shared'` 后拼接消费（20 处 `${API}/…` 与
//   `API + '/…'`）。旧实现 prefixConsts 只扫本文件的 const，导入进来的前缀一概看不见，
//   advisor 族 16 条后端路由被误报进"BE有/FE无"反向清单——反向清单的"零消费"结论因此失真。
//   现在按文件解析具名相对路径 import，把目标文件的 export 前缀常量按**导入时的本地绑定名**
//   （含 as 重命名）灌进当前文件的 prefixConsts 表。解析不到的 import 静默跳过（宁漏不误）。
//   既有行为全部保留：本文件 const 优先于导入、BASE_PREFIXES 排除、二段拼接占位逻辑不动。
import { readFileSync, readdirSync, statSync, mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { join, dirname, resolve } from 'node:path'
import { tmpdir } from 'node:os'

// 每个路由编译成正则：:param → [^/]+，末尾允许拼接动态段（如 '/api/v1/org/users/' + id）
function toRe(p) {
  const body = p
    .split('/')
    .map((seg) => (seg.startsWith(':') ? '[^/]+' : seg.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
    .join('/')
  return new RegExp('^' + body + '(/[^/]*)?$')
}
// routes（[{method,path}]）→ path → 该路径支持的方法集合 + 编译正则 的 entries 表
function buildEntries(routes) {
  const byPath = new Map()
  for (const r of routes) {
    if (!byPath.has(r.path)) byPath.set(r.path, { re: toRe(r.path), methods: new Set() })
    byPath.get(r.path).methods.add(r.method)
  }
  return [...byPath.entries()]
}

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
const BASE_PREFIXES = new Set(['/', '/api', '/api/v1', '/openapi', '/openapi/v1', '/api/v1/admin', '/api/v1/advisor', '/api/v1/super', '/api/v1/org', '/api/v1/cdp', '/api/v1/tenant', '/api/v1/auth', '/api/v1/privacy'])

// 提取源码里出现的 /api/v1、/openapi、/ws 路径字面量（含模板串起始部分）
const PATH_RE = /['"`](\/(?:api\/v1|openapi|health|status|metrics|ws)[^'"`\s]*)/g
// 就近推断 HTTP 方法：路径 token 之后 160 字符内的 method 关键字，默认 GET
const METHOD_RE = /method\s*[:=]\s*['"`]([A-Z]+)['"`]/
const VERB_FN_RE = /\b(post|put|del|delete|patch)\s*\(/i

// —— FIX-C 跨文件前缀解析的三个正则/工具 ——
// 本文件前缀常量（沿用原第 84 行同款语义：const NAME = '/api|/openapi 开头字面量'）
const LOCAL_PREFIX_RE = /const\s+([A-Za-z_$][\w$]*)\s*=\s*['"`](\/(?:api|openapi)\/[^'"`\s]*)['"`]/g
// 目标文件的 export 前缀常量：必须显式 export——具名 import 只可能引到导出值，
// 放宽到任意 const 会把目标文件内部变量误当共享前缀（宁漏不误）。
const EXPORT_PREFIX_RE = /(?:^|\n)\s*export\s+const\s+([A-Za-z_$][\w$]*)\s*=\s*['"`](\/(?:api|openapi)\/[^'"`\s]*)['"`]/g
// 具名相对路径 import：`import { A, type B, C as D } from './rel'`（多行大括号亦可，[^}]* 天然跨行）
// 默认导入 / namespace 导入不解析——前缀常量按具名导出共享是本仓现状，静默跳过。
const IMPORT_NAMED_RE = /import\s+(?:type\s+)?\{([^}]*)\}\s*from\s*['"](\.{1,2}\/[^'"]+)['"]/g

// 目标文件 export 前缀表缓存：一个 shared.ts 可能被十几个页面导入，避免重复读盘解析
const exportPrefixCache = new Map()
function exportPrefixesOf(file) {
  let m = exportPrefixCache.get(file)
  if (!m) {
    m = new Map()
    let text
    try {
      text = readFileSync(file, 'utf8')
    } catch {
      text = '' // 目标文件读不到（被删/权限）静默跳过，宁漏不误
    }
    for (const em of text.matchAll(EXPORT_PREFIX_RE)) {
      m.set(em[1], em[2].replace(/\/+$/, ''))
    }
    exportPrefixCache.set(file, m)
  }
  return m
}

// 把 './advisor/shared' 之类的相对 spec 解析到仓库内真实文件：
// 补无扩展名 + .ts/.tsx + index.ts/index.tsx 四种形态（与打包器解析顺序对齐），全部落空返回 null。
function resolveImportTarget(fromFile, spec) {
  const base = resolve(dirname(fromFile), spec)
  for (const cand of [base, base + '.ts', base + '.tsx', join(base, 'index.ts'), join(base, 'index.tsx')]) {
    try {
      if (statSync(cand).isFile()) return cand
    } catch {
      // 候选不存在，继续下一个
    }
  }
  return null
}

// 扫描 root 目录，产出 FE 引用集合与孤儿/方法错配清单。
// 从顶层代码收进函数是 FIX-C 为 --selftest 铺的路：自证用例对临时目录跑同一判据，
// 主流程与自证共用一份代码，防止"用例验证的是另一套逻辑"这种自证失真。
function analyze(root, entries) {
  // 记录 FE 引用到的 `METHOD path`，用于正向孤儿 + 方法错配 + 反向清单
  const referenced = new Set()
  const orphans = new Set()
  const methodMismatch = new Set()

  for (const file of walk(root)) {
    const text = readFileSync(file, 'utf8')
    // PLAN_FIX_2026-09-21 A4/L1：原提取器只认「以 /api/v1 开头的完整字面量」，而前端大量端点
    // 靠前缀常量拼接消费——Client.tsx `const API='/api/v1'` + `${API}/chat/test`、
    // Advisor.tsx `const API='/api/v1/advisor'` + `API + '/chat/toggle-ai-reply'`——提取器看不见，
    // 反向清单被"提取盲区"虚增（一度把在用的 C 端主链路 POST /chat/test 报成前端零消费，
    // 并据此写出错误的 P1 结论）。这里按文件解析前缀常量：模板 `${NAME}/x` 与拼接
    // `NAME + '/x'` 均还原成完整路径后再参与比对。
    const prefixConsts = new Map()
    for (const m of text.matchAll(LOCAL_PREFIX_RE)) {
      prefixConsts.set(m[1], m[2].replace(/\/+$/, ''))
    }
    // FIX-C：跨文件前缀灌入——先扫本文件 const（本地优先），再按具名 import 把
    // 目标文件的 export 前缀按**导入时的本地绑定名**（as 重命名后的名字）补进来。
    for (const im of text.matchAll(IMPORT_NAMED_RE)) {
      const target = resolveImportTarget(file, im[2])
      if (!target) continue // 相对路径指不到真实文件（别名/动态等）静默跳过
      const shared = exportPrefixesOf(target)
      if (shared.size === 0) continue
      for (const rawItem of im[1].split(',')) {
        const item = rawItem.trim()
        if (!item) continue
        const sm = item.match(/^(?:type\s+)?([A-Za-z_$][\w$]*)(?:\s+as\s+([A-Za-z_$][\w$]*))?$/)
        if (!sm) continue
        const binding = sm[2] || sm[1] // 本地绑定名：`A as B` 在消费方代码里叫 B
        if (prefixConsts.has(binding)) continue // 本文件同名 const 优先，不被导入覆盖
        const v = shared.get(sm[1])
        if (v) prefixConsts.set(binding, v)
      }
    }
    const cands = []
    for (const m of text.matchAll(PATH_RE)) cands.push({ raw: m[1], idx: m.index })
    for (const m of text.matchAll(/`\$\{([A-Za-z_$][\w$]*)\}([^`]*)`/g)) {
      const v = prefixConsts.get(m[1])
      if (!v) continue
      cands.push({ raw: v + m[2], idx: m.index })
    }
    // 支持二段拼接：`API + '/customer/' + detailId + '/tags'` —— Advisor.tsx 的客户详情
    // 子操作全走这个形态（tags/stage/followup/info），单段解析会把它们误报成零消费。
    // 中间变量段以 X 占位，路由侧 `:id` 编译为 `[^/]+`，可正常命中。
    for (const m of text.matchAll(
      /\b([A-Za-z_$][\w$]*)\s*\+\s*['"`](\/[^'"`]*)['"`](?:\s*\+\s*[A-Za-z_$][\w$]*\s*\+\s*['"`](\/[^'"`]+)['"`])?/g,
    )) {
      const v = prefixConsts.get(m[1])
      if (!v) continue
      cands.push({ raw: v + m[2] + (m[3] ? 'X' + m[3] : ''), idx: m.index })
    }
    for (const cm of cands) {
      let raw = cm.raw
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
      const tail = text.slice(cm.idx, cm.idx + 200)
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

  // P2-18(2026-09-20 审计批三)：EntityCrud 的启停端点为运行时拼接（`${base}/${row.id}/enable|disable`），
  // PATH_RE 只认完整字面量导致 KnowledgeTab/TagSystemTab 全家桶在反向清单里恒为"FE 无引用"孤儿。
  // 现按 `base="…"` 字面量逐条登记 POST {base}/:id/{enable,disable} 到已引用集合——
  // 登记后反向若仍剩该族孤儿即真无对应后端路由/确无消费者，孤儿语义不再被拼接盲区稀释。
  for (const file of walk(root)) {
    const text = readFileSync(file, 'utf8')
    for (const m of text.matchAll(/\bbase=["'`](\/api[^"'`\s]+)["'`]/g)) {
      const b = m[1].replace(/\/+$/, '')
      referenced.add(`POST ${b}/:id/enable`)
      referenced.add(`POST ${b}/:id/disable`)
      // PLAN_FIX_2026-09-21 A2：P2-18 只登记了启停，CRUD 主体仍是盲区——
      // knowledge 5 张表 + tags 3 张表 + strategy templates 共 27 条被误报"前端零消费"。
      // EntityCrud 是通用表格组件（列表/新建/更新/删除全走 base），按全套 CRUD 登记。
      // 已知局限（如实声明）：这是**登记制**而非解析制——若某 base 后端只实现了子集，
      // 登记全套会掩盖该子集的真孤儿；启用/停用的方法级核对仍依赖上方静态路径解析。
      // 不登记 `GET ${b}/:id`：EntityCrud 是"列表整行编辑"模式，行数据来自列表响应，
      // 不会按 id 单条回拉——登记它会把 knowledge/tags/templates 的单条 GET 从真孤儿
      // 名单里洗掉（实测触发 2 条 STALE 白名单告警），属过度登记。
      for (const meth of ['GET', 'POST']) referenced.add(`${meth} ${b}`)
      for (const meth of ['PUT', 'DELETE']) referenced.add(`${meth} ${b}/:id`)
    }
  }

  return { referenced, orphans, methodMismatch }
}

// 反向清单白名单：有意无前端消费面的端点（理由逐条登记，勿删注释）
// P1-9(2026-09-20 审计批二)：显式白名单化——以下条目经评估"有意无前端消费面"，
// 从反向清单剔除并在此登记理由；清单外的新增孤儿仍会出现在 INFO 里提示评估。
// 若白名单条目日后被前端真实引用，会打 STALE 提示清理（防腐化）。
const REVERSE_WHITELIST = {
  'GET /api/v1/strategy/features': '特征清单供策略引擎调试/契约核对；StrategyTab 经模板编辑器维护特征键，只读聚合端点有意不做 UI',
  'GET /api/v1/strategy/stats/anchors': '锚点统计走 /super/packs/stats 与 D9 包质量视图，此裸聚合端点为 API 备查面',
  'GET /api/v1/strategy/templates/:id': '模板编辑器基于列表端点整行编辑，单模板 GET 仅 OpenAPI/脚本用途',
  'POST /api/v1/admin/config/init': '配置初始化/回滚是灾难恢复动作，刻意只留 CLI/超管接口，防管理页误触清配置',
  'POST /api/v1/admin/config/rollback': '同上（回滚）；ConfigPanel 仅 PUT 保存，恢复链路走 tools/ 脚本与超管流程',
  'GET /api/v1/advisor/list': '与在用 /advisor/customers 列表口径重复，前端零消费属实（后端收敛候选，批三 P2 评估删除或合并）',
  // FIX-C 收口批(2026-09-28)：跨文件 import 前缀解析落地后，反向清单从 54 条降到 40 条，
  // 剩下这两条 advisor 端点经核**不是提取盲区、也不是死路由**——消费方在浏览器之外：
  // 故按"有意无 SPA 消费面"登记，日后若前端真调起来会打 STALE 提示清出白名单。
  'GET /api/v1/advisor/test-drive/:id': '试驾单单条详情：SPA 走列表 + PUT 就地编辑（Advisor.tsx），逐条 GET 的消费方是 tools/uat_advisor.sh §七 的字节级回环与契约生成物 api.d.ts，刻意不做详情弹窗（一屏内已含全部字段）',
  'POST /api/v1/advisor/chat/ai-reply': '顾问手动"让 AI 回一句"：同步 AI 链路耗时 30~130s，SPA 侧走的是 /chat/clear-delay + 队列自动回复；本端点由 tools/uat_advisor.sh §九 作为服务端能力面护栏在跑（去掉它等于悄悄删掉一条真实能力）',
  // 'GET /api/v1/advisor/followups' 原登记为"提取器盲区"（Advisor.tsx `API + '/followups'`）；
  // PLAN_FIX_2026-09-21 A4 提取器已支持前缀常量拼接还原，该条目被真实引用命中并触发
  // STALE，故移出白名单。
}

function main() {
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

  const entries = buildEntries(routes)
  const { referenced, orphans, methodMismatch } = analyze('frontend-react/src', entries)

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
  const shown = reverse.filter((r) => !REVERSE_WHITELIST[r])
  if (shown.length) {
    console.log(`  INFO  BE有/FE无（${shown.length} 条，回调/超管/OpenAPI 属正常，暂不 fail；另有白名单 ${Object.keys(REVERSE_WHITELIST).length} 条见脚本注释）`)
    for (const r of shown.sort()) console.log('    ' + r)
  }
  for (const key of Object.keys(REVERSE_WHITELIST)) {
    if (referenced.has(key)) console.log(`  INFO  STALE 反向白名单条目已被前端引用，可从 REVERSE_WHITELIST 移除：${key}`)
  }

  process.exit(fail)
}

// —— FIX-C --selftest：跨文件前缀解析的反证用例 ——
// 护栏文化：守卫必须配反证。本用例对临时迷你树跑**同一份 analyze**：
//   正向：b 文件 `import { API } from './a'` + `${API}/demo/ping` → 路由必须被判"有消费"（不进反向清单）；
//   反向：把 b 文件的 import 行删掉（其余一字不动）→ 同一条由必须回到反向清单。
// 两向都过才证明判据真的建在跨文件解析上，而不是"永远说前端在用"的常绿守卫。
function selftest() {
  const root = mkdtempSync(join(tmpdir(), 'fixc-selftest-'))
  const results = []
  const check = (name, ok) => results.push({ name, ok })
  try {
    const routes = buildEntries([{ method: 'GET', path: '/api/v1/demo/ping' }])
    const aText = "export const API = '/api/v1'\n"
    const bWithImport = "import { API } from './a'\nexport function ping() { return fetch(`${API}/demo/ping`) }\n"
    const bNoImport = "export function ping() { return fetch(`${API}/demo/ping`) }\n"

    // 正向：带 import——跨文件前缀应被解析，路由命中即从反向清单消失
    const dirA = join(root, 'with')
    mkdirSync(dirA, { recursive: true })
    writeFileSync(join(dirA, 'a.ts'), aText)
    writeFileSync(join(dirA, 'b.ts'), bWithImport)
    const rA = analyze(dirA, routes)
    check('正向：import 跨文件前缀后 GET /api/v1/demo/ping 被判有消费（不进反向清单）', rA.referenced.has('GET /api/v1/demo/ping'))
    check('正向：解析出的路径全部命中后端路由（无 ORPHAN/METHOD-MISMATCH）', rA.orphans.size === 0 && rA.methodMismatch.size === 0)

    // 反向：删掉 import 行、其余不变——同一条由必须回到反向清单
    const dirB = join(root, 'without')
    mkdirSync(dirB, { recursive: true })
    writeFileSync(join(dirB, 'a.ts'), aText)
    writeFileSync(join(dirB, 'b.ts'), bNoImport)
    const rB = analyze(dirB, routes)
    check('反证：去掉 import 后 GET /api/v1/demo/ping 回到反向清单（判据不是常绿）', !rB.referenced.has('GET /api/v1/demo/ping'))

    const failed = results.filter((r) => !r.ok)
    for (const r of results) console.log(`  [${r.ok ? 'PASS' : 'FAIL'}] ${r.name}`)
    console.log(`  selftest 终局: PASS=${results.length - failed.length} FAIL=${failed.length}`)
    process.exit(failed.length ? 1 : 0)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
}

if (process.argv[2] === '--selftest') selftest()
else main()
