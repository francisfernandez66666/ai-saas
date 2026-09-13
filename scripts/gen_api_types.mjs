#!/usr/bin/env node
// T7 codegen：从 api.schema.json 生成前端 API 路径/响应类型，避免手写路径与响应契约漂移。
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const root = resolve(here, '..')
const schemaPath = process.argv[2] || resolve(root, 'api.schema.json')
const outPath = process.argv[3] || resolve(root, 'frontend-react/src/types/api.d.ts')

const schema = JSON.parse(readFileSync(schemaPath, 'utf8'))
const routes = Array.isArray(schema.routes) ? schema.routes : []

const seen = new Set()
const lines = []
const usedTypes = new Set()
for (const r of routes) {
  if (!r?.method || !r?.path) continue
  const key = `${r.method} ${r.path}`
  if (seen.has(key)) continue
  seen.add(key)
  const data = r.data_ts || 'unknown'
  if (data !== 'unknown') {
    for (const id of data.matchAll(/[A-Za-z_$][A-Za-z0-9_$]*/g)) {
      if (id[0] !== 'unknown') usedTypes.add(id[0])
    }
  }
  lines.push(`  ${JSON.stringify(key)}: ${data}`)
}
lines.sort()

const imports = ['ApiResp', ...usedTypes].sort()

const banner = [
  '// 自动生成：禁止手改。',
  '// 来源：api.schema.json（go run ./cmd/apidump -out api.schema.json）',
  '// 生成：scripts/gen_api_types.mjs（npm run gen:api）',
].join('\n')

const body = `${banner}
import type { ${imports.join(', ')} } from '../types'

export interface ApiRoutes {
${lines.join('\n')}
}

export type ApiPath = keyof ApiRoutes

export type ApiResponse<P extends ApiPath> = ApiResp<ApiRoutes[P]>
`

mkdirSync(dirname(outPath), { recursive: true })
writeFileSync(outPath, body)
console.log(`generated ${outPath} (${lines.length} routes)`)
