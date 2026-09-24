// Playwright 真浏览器 E2E（19 项）：落地页/定价/注册/登录漏斗/受保护路由/端点连通/E10 文档站渲染，
// 外加 390px 窄屏三台（Admin/Super 折叠下拉、Org 单栏不炸版，P1-10 批二）、D2 AI 贡献度卡片、
// D4 看板数字下钻（第 19 项）、主动触达队列 Tab（第 18 项）、
// S2 找回密码通道文案（2026-09-23 批二：页面上不得再出现"服务端日志"这类内部实现提示）。需 9090 服务在跑。
import { test, expect } from '@playwright/test';
import type { APIRequestContext } from '@playwright/test';

const BASE = 'http://localhost:9090';

// D3 修复(2026-09-16，AUDIT_DEFECT_VERIFY)：seed 每次启动都会把出厂弱密码账号重新置
// must_change_password=true（seed/seed_tenant.go:127），MustChangePasswordGuard（middleware/org.go:117）
// 随即对除 change-password/auth/me 外的全部路径返回 403——旧 spec 的 [200,400] 期望在"重启后的干净环境"
// 必挂（实测 strategy test 用例 403）。这里做 API 回环自适应：检测到强改密标记 → 改密（服务端清标记，
// B4 同时 bump token_version 故需重登）→ 再改回 admin123 恢复出厂凭据。change-password 无"新旧相同拒绝"
// 限制（auth_password.go:89-96 仅验旧密码+强度），回环不污染任何账号密码，smoke/uat 脚本继续用 admin123。
async function adminToken(request: APIRequestContext): Promise<string> {
  const login = (password: string) =>
    request
      .post(`${BASE}/api/v1/auth/login`, { data: { username: 'admin', password } })
      .then(r => r.json());
  let j = await login('admin123');
  let token = (j.data?.token || '') as string;
  if (token && j.data?.user?.must_change_password) {
    const tmp = 'E2eTemp2026Pass';
    await request.post(`${BASE}/api/v1/auth/change-password`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { old_password: 'admin123', new_password: tmp },
    });
    j = await login(tmp);
    token = j.data?.token || '';
    await request.post(`${BASE}/api/v1/auth/change-password`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { old_password: tmp, new_password: 'admin123' },
    });
    j = await login('admin123');
    token = j.data?.token || '';
  }
  return token;
}

// 1. Landing page loads
test('landing page loads with hero', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('text=AI 驱动')).toBeVisible({ timeout: 10000 });
});

// 2. Pricing page shows packages
test('pricing page shows packages', async ({ page }) => {
  await page.goto('/pricing');
  await expect(page.getByRole('heading', { name: '选择适合您的套餐' })).toBeVisible({ timeout: 10000 });
});

// 3. Login page renders
test('login page renders form', async ({ page }) => {
  await page.goto('/login');
  await expect(page.locator('input').first()).toBeVisible();
});

// 4. Login API endpoint works
test('login API returns token', async ({ request }) => {
  const r = await request.post(`${BASE}/api/v1/auth/login`, {
    data: { username: 'admin', password: 'admin123' },
  });
  expect(r.ok()).toBeTruthy();
  const j = await r.json();
  expect(j.code).toBe(0);
  expect(j.data?.token).toBeTruthy();
});

// 5. Register page loads
test('register page renders', async ({ page }) => {
  await page.goto('/register');
  await expect(page.locator('input').first()).toBeVisible({ timeout: 10000 });
});

// 6. Protected route redirects to login
test('protected route redirects to login', async ({ page }) => {
  await page.goto('/admin');
  await page.waitForURL(url => url.pathname.includes('/login'), { timeout: 10000 });
  expect(page.url()).toContain('/login');
});

// 7. Auth me returns user info
// P1-11 修复(2026-09-20 审计批二)：原 `if (meRes.ok())` 条件断言——非 200 时整段跳过假绿。
// 登录前置（adminToken 回环）已保证凭据有效，/auth/me 必 200，断言无条件化。
test('auth me returns user info after login', async ({ request }) => {
  const token = await adminToken(request);
  expect(token).toBeTruthy();

  const meRes = await request.get(`${BASE}/api/v1/auth/me`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(meRes.ok()).toBeTruthy();
  const meBody = await meRes.json();
  expect(meBody.code).toBe(0);
  expect(meBody.data?.username).toBe('admin');
});

// 8. Health check endpoint
test('health check endpoint returns OK', async ({ request }) => {
  const r = await request.get(`${BASE}/health`);
  expect(r.ok()).toBeTruthy();
});

// 9. Chat test endpoint responds (may require visitor session)
// C1(PLAN_FIX_2026-09-21)：主路由由 /chat/test 改名 /chat/unauthorized（语义自解释：
// 未授权/未留资访客的聊天入口，不是测试桩）；旧路径作为 deprecated 兼容别名保留一版。
// S1 收口(2026-09-23 批二)：这里刻意**不带 customer_id** 打匿名请求，断言被 400 拒——
// 旧行为是"缺省即写进 1 号客户会话"（README 时代的测试便利），任何匿名访客都能往
// 真实客户会话灌消息。正确链路两步：先 /chat/guest 领 customer_id + visitor_key。
// 本用例因此零 AI 成本（400 在入队/生成之前就返回），也不受 20/min IP 桶影响。
async function expectAnonymousChatRejected(request: APIRequestContext, path: string) {
  const r = await request.post(`${BASE}${path}`, { data: { content: '你好' } });
  // 429 属正常：test_all 顺序跑八套 E2E 时同源 IP 桶可能已满，限流生效本身即证明端点在位
  expect([400, 429]).toContain(r.status());
  if (r.status() === 429) return;
  const j = await r.json();
  expect(j.error_code).toBe('param_error');
  expect(j.message).toMatch(/customer_id/);
}

test('chat unauthorized 拒绝无 customer_id 的匿名写(S1)', async ({ request }) => {
  await expectAnonymousChatRejected(request, '/api/v1/chat/unauthorized');
});

test('chat test 旧别名仍在位(deprecated 兼容)', async ({ request }) => {
  await expectAnonymousChatRejected(request, '/api/v1/chat/test');
});

// 10. Strategy test endpoint works
test('strategy test endpoint responds', async ({ request }) => {
  const token = await adminToken(request);

  const r = await request.post(`${BASE}/api/v1/strategy/test`, {
    headers: { Authorization: `Bearer ${token}`, 'X-Tenant-ID': '1' },
    data: { content: '你好', customer_id: 1 },
  });
  // May return 200 or 400 (super_admin needs tenant header)
  expect([200, 400]).toContain(r.status());
  const j = await r.json();
  expect(j).toHaveProperty('code');
});

// 11. E10 开放 API 文档站：/docs/api 免登录渲染后端规格（端点卡片真实出现，非空壳）
test('openapi docs page renders endpoints from spec', async ({ page }) => {
  await page.goto('/docs/api');
  await expect(page.getByText('AI-SCRM 开放 API')).toBeVisible({ timeout: 10000 });
  await expect(page.getByText('/chat/completions').first()).toBeVisible({ timeout: 10000 });
});

// 12-14. P1-10(2026-09-20 审计批二)：桌面管理台三台 390px 窄屏可达性
// 断言口径：①固定 200px 左栏在窄屏消失（选择器必须是 .t-layout__sider —— TDesign React 的
// <Aside> 输出 aside.t-layout__sider，不存在 .t-layout__aside 这个类；写错时 locator 恒为空，
// toHaveCount(0) 会在桌面宽度下也永远通过，是一条假绿断言，2026-09-23 第 18 项首跑实锤），
// 折叠为顶栏下拉菜单；
// ②整页横向滚动宽度不超视口（防内容炸版，表格横滚应局限在 .t-table__content 内）；
// ③下拉切 Tab 真实联动（/admin 选"回复速度"出配置动作条）。
async function seedDesktopLogin(page: import('@playwright/test').Page, request: APIRequestContext, impersonateTenant = '') {
  const token = await adminToken(request);
  // addInitScript 在每次导航前注入登录三键（与真实登录后的 localStorage 形态一致）
  await page.addInitScript(([t, imp]) => {
    localStorage.setItem('scrm_auth_token', t);
    localStorage.setItem('role', 'super_admin');
    localStorage.setItem('username', 'admin');
    // D4 用例带上代管租户：与顶栏下拉点选写入的是同一个键（lib/api.ts 的 IMPERSONATE_TENANT_KEY），
    // 后续所有租户作用域请求由 apiFetch/authHeaders 统一注入 X-Tenant-ID。
    if (imp) localStorage.setItem('scrm_impersonate_tenant', imp);
  }, [token, impersonateTenant]);
}

test.describe('P1-10 390px 桌面三台可达', () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test('/admin 折叠为顶栏下拉并可切 Tab', async ({ page, request }) => {
    await seedDesktopLogin(page, request);
    await page.goto('/admin');
    const menu = page.locator('select[aria-label="管理菜单"]');
    await expect(menu).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.t-layout__sider')).toHaveCount(0);
    await menu.selectOption('reply_speed');
    // 配置类 Tab 无租户作用域限制，选中后渲染底部动作条。
    // 用 button 角色精确定位：页内另有 ConfigPanel 提示文案「使用顶部"⚡ 延迟归零"按钮切换」，
    // getByText 会 strict-mode 命中 2 元素假失败（2026-09-21 终局回归实证）
    await expect(page.getByRole('button', { name: /延迟归零/ })).toBeVisible({ timeout: 10000 });
    const sw = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(sw).toBeLessThanOrEqual(392);
  });

  test('/super 折叠为顶栏下拉', async ({ page, request }) => {
    await seedDesktopLogin(page, request);
    await page.goto('/super');
    await expect(page.locator('select[aria-label="平台管理菜单"]')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.t-layout__sider')).toHaveCount(0);
    const sw = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(sw).toBeLessThanOrEqual(392);
  });

  test('/org 双栏降单栏不炸版', async ({ page, request }) => {
    await seedDesktopLogin(page, request);
    await page.goto('/org');
    await expect(page.getByRole('heading', { name: '部门树' })).toBeVisible({ timeout: 10000 });
    // 成员表 min-w-[560px] 被 overflow-x-auto 包裹，页面级横滚仍应锁在 390
    const sw = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(sw).toBeLessThanOrEqual(392);
  });
});

// 16. D2 AI 贡献度看板（真浏览器 + 真实端点）
// 与单测互补：单测只能证明 mock 数据下的渲染，这里证明"真端点 + 真浏览器 + 超管代管"链路可用。
// 前置坑（首跑即踩到，记下防复发）：超管未选「代管租户」时，Admin.tsx 会把租户作用域 Tab
// （dashboard 属于此类）整块替换为"需先选择代管租户"的橙条，DashboardTab 根本不挂载 ——
// 故必须先在该下拉框里选定租户，否则等多久都等不到卡片（断言会以"标题不存在"失败）。
// 选哪个租户见 pickOperableTenant 注释（按下标取会踩到过期/停用租户）。
// 断言口径：
//  ① 选定代管租户后卡片标题可见（端点 4xx/500 会让卡片进失败态，此处即红灯）
//  ② 切"近 7 天"必须发出 days=7 的真实请求（监听网络，防"只改样式不改数据"）
//  ③ 页面无 NaN/Infinity 文本（0 除 0 未兜底的典型症状，新租户零数据必踩）

// 代管租户选取口径（D2 卡片与主动触达 Tab 共用）：**探一个真进得去的租户**，不再按下拉框下标取。
// 旧写法 `option.nth(1)` 依赖后端返回顺序，而 cleanup_test_tenants 会删测试租户、trial 租户会到期，
// 2026-09-23 清库后实跑即踩到 nth(1) 落在已过期 trial 上（接口 402「试用期已结束」）——
// 那时断言测的是"这个租户不可用"，不是产品行为。故逐个租户用真实作用域端点探 200，探不到即前置失败。
async function pickOperableTenant(request: APIRequestContext): Promise<string> {
  const token = await adminToken(request);
  const auth = { Authorization: `Bearer ${token}` };
  const resp = await request.get(`${BASE}/api/v1/super/tenants`, { headers: auth });
  expect(resp.ok()).toBeTruthy();
  const payload = (await resp.json())?.data;
  const rows = (Array.isArray(payload) ? payload : payload?.list ?? []) as { id?: number }[];
  for (const t of rows) {
    const id = String(t?.id ?? '');
    if (!id || id === '0') continue;
    // 探针用中性的租户作用域读端点：不能用被测端点自己当探针（那会把"前置"和"结论"混在一起）
    const probe = await request.get(`${BASE}/api/v1/customers?limit=1`, { headers: { ...auth, 'X-Tenant-ID': id } });
    if (probe.ok()) return id;
  }
  throw new Error('无租户作用域可进入的租户（全为停用/过期），浏览器用例前置不成立');
}

// D4 用例专用：探一个「AI 独立接待客户数 > 0」的租户（按给定窗口）。
// 为什么单独一个探针：pickOperableTenant 只保证"进得去"，进得去的租户完全可能是零数据新租户，
// 那时"卡片数字 == 名单条数"会在 0==0 上恒绿——等式成立但什么都没测。探针只判存在性，
// 真实数字由用例自己从页面上读，绝不把探针结论当断言。
//
// 探针顺序按「最旧租户优先」：/super/tenants 硬编码 id DESC（P2-29：新注册租户不能被挤出视野），
// 而承载演示/历史数据的是 ID 最小的种子租户，从末页倒着探通常第一次即命中；
// 从首页往下探则要空跑两百多次零数据租户（当前库 254 家，其中 250+ 是历史测试租户）。
// 注意这只是**探针顺序**，不是断言口径——命中与否仍由真实接口结论决定。
async function pickTenantWithAiServed(request: APIRequestContext, days = 90): Promise<string> {
  const token = await adminToken(request);
  const auth = { Authorization: `Bearer ${token}` };
  const first = await request.get(`${BASE}/api/v1/super/tenants?page=1&page_size=100`, { headers: auth });
  expect(first.ok()).toBeTruthy();
  const f = (await first.json())?.data ?? {};
  const total = Number(f.total ?? 0);
  const pageSize = Number(f.page_size ?? 100) || 100;
  const seen = new Set<string>();
  for (let page = Math.max(1, Math.ceil(total / pageSize)); page >= 1; page--) {
    const resp = page === 1 ? first : await request.get(`${BASE}/api/v1/super/tenants?page=${page}&page_size=${pageSize}`, { headers: auth });
    if (!resp.ok()) continue;
    const rows = (((await resp.json())?.data ?? {}).list ?? []) as { id?: number }[];
    for (const t of rows.slice().reverse()) {
      const id = String(t?.id ?? '');
      if (!id || id === '0' || seen.has(id)) continue;
      seen.add(id);
      const r = await request.get(`${BASE}/api/v1/stats/ai-contribution?days=${days}`, { headers: { ...auth, 'X-Tenant-ID': id } });
      if (!r.ok()) continue;
      if (((await r.json())?.data?.ai_served_customers ?? 0) > 0) return id;
    }
  }
  throw new Error(`近 ${days} 天没有任何租户有 AI 接待数据，D4 下钻用例前置不成立`);
}

test('D2 admin AI 贡献度卡片渲染且窗口切换发真实请求', async ({ page, request }) => {
  const tid = await pickOperableTenant(request);
  await seedDesktopLogin(page, request);
  const hits: string[] = [];
  page.on('request', (r) => {
    if (r.url().includes('/stats/ai-contribution')) hits.push(r.url());
  });
  await page.goto('/admin');

  // 超管必须先选定代管租户，租户作用域 Tab 才会真正挂载
  const impSel = page.locator('select[aria-label="代管租户"]');
  await expect(impSel).toBeVisible({ timeout: 10000 });
  await impSel.selectOption(tid);

  await expect(page.getByRole('heading', { name: 'AI 贡献度' })).toBeVisible({ timeout: 15000 });
  await expect.poll(() => hits.some((u) => u.includes('days=30')), { timeout: 10000 }).toBeTruthy();
  await page.getByText('近 7 天').click();
  await expect.poll(() => hits.some((u) => u.includes('days=7')), { timeout: 10000 }).toBeTruthy();
  const body = await page.locator('body').innerText();
  expect(body).not.toMatch(/NaN|Infinity/);
});

// 17. S2 找回密码提示按后端真实通道渲染，不再向最终用户暴露"服务端日志"（2026-09-23 批二）
// 根因回放：旧页面把"验证码将输出到服务端日志（开发模式）"写死成给最终用户看的文案——
// 既泄露内部实现，又在 SMTP 已配好的生产环境说假话（用户去翻日志，其实邮件早发出去了）。
// 现在通道由 GET /auth/reset-channel 决定，页面上任何一处都不该再出现"服务端日志"。
// 之所以用真浏览器：这条契约的失败形态是"页面上多了一句话"，接口断言与 vitest jsdom 都测不准
// （异步 useEffect + 两条分支文案），必须按用户实际看到的文本判。
test('S2 找回密码页不再出现服务端日志字样', async ({ page, request }) => {
  // 先钉住后端确有该端点（返回 log 或 smtp 都算通道在位；缺失/500 说明驱动源断了）
  const r = await request.get(`${BASE}/api/v1/auth/reset-channel`);
  expect(r.ok()).toBeTruthy();
  const j = await r.json();
  expect(['smtp', 'log']).toContain(j?.data?.channel);

  await page.goto('/login');
  await page.getByText('忘记密码').click();
  await expect(page.getByRole('button', { name: '发送验证码' })).toBeVisible({ timeout: 10000 });
  const body = await page.locator('body').innerText();
  expect(body).not.toMatch(/服务端日志/);
  // 正向锁：光"没有那句话"不够（整页空白也能过），必须看到通道驱动的那句提示真的渲染出来了。
  // smtp → 指引看邮箱；log → 指引找管理员；两者之一即证明 /auth/reset-channel 已接线。
  expect(body).toMatch(/验证码将发送至账号绑定的邮箱|请联系管理员协助重置|验证码 10 分钟内有效/);
});

// 18. 主动触达队列 Tab（触达最小闭环 · 批次3/4，2026-09-23）
// 真浏览器只断"用户看得见的那一屏"三件事，与 smoke §三十二（23 项接口契约+落库）互补不重叠：
//  ① 菜单可达且真的发出 /admin/outreach/tasks 请求——Tab 挂载了但请求没发＝路径或筛选写错；
//  ② 「开关未开」横幅严格跟随后端 config.enabled：写死成常量、或后端改名，这里必红
//     （出厂 outreach_enabled=false，故默认走"必须可见"这一支）；
//  ③ 队列文本里不出现 NaN/undefined/[object Object]——零数据新租户是未兜底字段的典型现场。
//
// 代管租户选取口径同 D2 卡片：用 pickOperableTenant 探到的可用租户，勿按下标取
// （2026-09-23 清库后 nth(1) 落在已过期 trial 上，断言打的是 402 而非产品行为）。
test('主动触达 Tab 真浏览器渲染且提示跟随真实开关', async ({ page, request }) => {
  const tid = await pickOperableTenant(request);
  await seedDesktopLogin(page, request);
  const hits: string[] = [];
  page.on('request', (r) => {
    if (r.url().includes('/admin/outreach/tasks')) hits.push(r.url());
  });
  await page.goto('/admin');

  // 超管必须先选定代管租户，租户作用域 Tab 才会真正挂载（同 D2 卡片的前置坑）
  const impSel = page.locator('select[aria-label="代管租户"]');
  await expect(impSel).toBeVisible({ timeout: 10000 });
  await impSel.selectOption(tid);

  // 正向锁：先证明桌面宽度下左栏确实用 .t-layout__sider 挂载——第 13/14 项的 toHaveCount(0)
  // 是「选择器命中 0 个」的空断言，若哪天类名变了它会永远绿。这一句让类名漂移在本项即红。
  await expect(page.locator('.t-layout__sider')).toBeVisible({ timeout: 10000 });
  await page.locator('.t-menu').getByText('主动触达', { exact: true }).click();
  await expect(page.getByRole('button', { name: /新建触达/ })).toBeVisible({ timeout: 15000 });
  await expect.poll(() => hits.length, { timeout: 10000 }).toBeGreaterThan(0);

  const token = await adminToken(request);
  const api = await request.get(`${BASE}/api/v1/admin/outreach/tasks?limit=1`, {
    headers: { Authorization: `Bearer ${token}`, 'X-Tenant-ID': tid },
  });
  expect(api.ok()).toBeTruthy();
  const enabled = !!(await api.json())?.data?.config?.enabled;
  const banner = page.getByText('主动触达未开启');
  if (enabled) await expect(banner).toHaveCount(0);
  else await expect(banner).toBeVisible({ timeout: 10000 });

  const body = await page.locator('body').innerText();
  expect(body).not.toMatch(/NaN|undefined|\[object Object\]/);
});

// 19. D4 看板数字下钻到客户名单（2026-09-23 D4）
// 接口层的"名单条数 == 看板数字"由 smoke §三十四逐指标钉死（六个指标 total 与卡片字段逐字相等），
// 这里只钉用户看得见的那一次点击，四件事各不相同、缺一即失真：
//  ① 数字格子真的挂了点击（Tile 未接 onDrill 时页面看起来完全正常）；
//  ② 下钻请求带上了看板**当前**窗口——窗口切到 90 天后仍发 days=30，就是"筛选条件没跟过去"；
//  ③ 横幅上的「共 N 位客户」与卡片上那个数字逐字相同（两套判据各写一遍的典型后果在这里现形）；
//  ④ 「返回看板」回得去，且横幅真的消失（回不去的下钻等于把工作台藏了一半）。
// 前置租户由 pickTenantWithAiServed 探（零数据租户会让 ③ 在 0==0 上假绿），卡片数字则从页面读。
test('D4 看板数字可下钻且名单数量与卡片一致', async ({ page, request }) => {
  const tid = await pickTenantWithAiServed(request);
  // 代管租户直接写键而非点下拉：下拉数据源是 /super/tenants 首页（id DESC 最新 20 家，P2-29 口径），
  // 承载历史接待数据的种子租户排在 20 名之外，点选路径拿不到它——下拉本身的点选联动已由
  // 上面的 D2 卡片与主动触达两项覆盖，本项只负责下钻链路。
  await seedDesktopLogin(page, request, tid);
  const drillHits: string[] = [];
  // 卡片数字按「响应」取，不按「请求」取：请求发出去时页面渲染的还是上一个窗口的数，
  // 拿它去比本次下钻的名单必然不等（2026-09-23 全量回归首跑即以 flake 现形：期望 198、横幅另一个数）。
  const cardNums: { days: number; aiServed: number }[] = [];
  page.on('request', (r) => {
    if (r.url().includes('/stats/ai-contribution/customers')) drillHits.push(r.url());
  });
  page.on('response', (r) => {
    const m = /\/stats\/ai-contribution\?days=(\d+)/.exec(r.url());
    if (!m || r.status() !== 200) return;
    void r.json()
      .then((j) => cardNums.push({ days: Number(m[1]), aiServed: Number(j?.data?.ai_served_customers ?? -1) }))
      .catch(() => {});
  });
  await page.goto('/admin');

  await expect(page.getByRole('heading', { name: 'AI 贡献度' })).toBeVisible({ timeout: 15000 });

  await page.getByText('近 90 天').first().click();
  await expect.poll(() => cardNums.some((x) => x.days === 90 && x.aiServed > 0), { timeout: 15000 }).toBeTruthy();
  const cardNum = cardNums.filter((x) => x.days === 90).pop()!.aiServed;

  const tile = page.locator('div[role="button"]', { hasText: 'AI 独立接待客户' }).first();
  await expect(tile).toBeVisible({ timeout: 10000 });
  // 前端只是显示器：格子上那个数必须逐字等于 90 天窗口接口给的值（前端复算一遍就有两套口径了）
  await expect(tile.locator('b')).toHaveText(String(cardNum), { timeout: 10000 });

  await tile.click();
  await expect.poll(() => drillHits.length, { timeout: 15000 }).toBeGreaterThan(0);
  expect(drillHits[drillHits.length - 1]).toContain('metric=ai_served');
  expect(drillHits[drillHits.length - 1]).toContain('days=90');

  await expect(page.getByText(`共 ${cardNum} 位客户`)).toBeVisible({ timeout: 15000 });
  // 零命中态不得出现（出现即名单为空，与上面的数量等式自相矛盾）
  await expect(page.getByText('该窗口内没有命中这个指标的客户')).toHaveCount(0);

  await page.getByRole('button', { name: '返回看板' }).click();
  await expect(page.getByText('AI 贡献度下钻：')).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'AI 贡献度' })).toBeVisible({ timeout: 10000 });

  const body = await page.locator('body').innerText();
  expect(body).not.toMatch(/NaN|undefined|\[object Object\]/);
});

// 20. 获客活码后台页（获客批 · 批次4，2026-09-23）
// 接口层的四十余条契约由 smoke §三十五钉死（建码/归因/下钻/跨租户/清场），本项只管
// 用户看得见的那一屏，四件事各不相同、缺一即失真：
//  ① 菜单可达且真的发出 /admin/acquisition/codes——Tab 挂了但请求没发＝路径或筛选写错；
//  ② 五个客户级格子可点、而「扫码」格子**不可点**：后端 400 只证明服务端守住单位纪律，
//     前端要是把扫码做成可点的，用户点下去看到的是错误提示，等于产品自己造矛盾；
//  ③ 点开名单后横幅「共 N 位客户」逐字等于格子上那个数，且下钻请求带上页面**当前**窗口
//     （切到 90 天仍发 days=30，就是筛选条件没跟过去）；
//  ④ 二维码与落地链接用的是后端下发的 link——前端复拼一次，三级基址口径就有了第二个真相源。
// 前置数据用 API 现造一个码并让一位访客扫它（零数据时 ③ 会在 0==0 上假绿）；
// 码名固定前缀 e2e活码，收尾只负责停用（产品侧无删除），行由 tools/cleanup_test_tenants.sh 回收。
test('获客活码后台页可建码、漏斗可下钻且扫码格子不给点', async ({ page, request }) => {
  const tid = await pickOperableTenant(request);
  const token = await adminToken(request);
  const auth = { Authorization: `Bearer ${token}`, 'X-Tenant-ID': tid };
  const stamp = Date.now();
  const codeName = `e2e活码渠道位-${stamp}`;

  const created = await request.post(`${BASE}/api/v1/admin/acquisition/codes`, {
    headers: auth, data: { name: codeName, channel: '抖音', remark: 'playwright' },
  });
  expect(created.ok()).toBeTruthy();
  const cd = (await created.json())?.data ?? {};
  const code = String(cd.code ?? '');
  const link = String(cd.link ?? '');
  expect(link).toContain(`/client?code=${code}`);
  const codeId = Number(cd.id ?? 0);
  expect(code).toMatch(/^[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{8}$/);

  // 造一位真扫了这个码的客户：公开侧不带任何租户头，与真实扫码同路径
  await request.post(`${BASE}/api/v1/acquisition/${code}/scan`, { data: { visitor_key: `vk_e2e_${stamp}` } });
  const guest = await request.post(`${BASE}/api/v1/chat/guest`, {
    headers: { 'X-Tenant-ID': tid, 'Content-Type': 'application/json' }, data: { code },
  });
  expect(guest.ok()).toBeTruthy();
  expect((await guest.json())?.data?.acquisition?.applied).toBe(true);

  await seedDesktopLogin(page, request, tid);
  const listHits: string[] = [];
  const drillHits: string[] = [];
  page.on('request', (r) => {
    if (r.url().includes('/admin/acquisition/codes') && !r.url().includes('/customers')) listHits.push(r.url());
    if (r.url().includes('/admin/acquisition/codes/') && r.url().includes('/customers')) drillHits.push(r.url());
  });
  await page.goto('/admin');
  await page.locator('.t-menu').getByText('获客活码', { exact: true }).click();
  await expect(page.getByRole('button', { name: /新建活码/ })).toBeVisible({ timeout: 15000 });
  await expect.poll(() => listHits.length, { timeout: 15000 }).toBeGreaterThan(0);

  // 本用例自己造的那一行（同租户可能还有别人建的码，故按名字定位，勿按下标取）
  const row = page.locator('tr', { hasText: codeName }).first();
  await expect(row).toBeVisible({ timeout: 15000 });
  await expect(row.getByText(code, { exact: true })).toBeVisible();

  // ② 单位纪律在前端的落地：扫码是纯文本，五个客户级格子是 button
  await expect(row.getByText('扫码', { exact: true })).toHaveCount(0); // 表头列名不在行内
  const scansCell = row.locator('td', { hasText: /^\s*1\s*$/ }).first();
  await expect(scansCell.locator('[role="button"]')).toHaveCount(0);
  const newTile = row.locator('[role="button"]', { hasText: '新增客户' }).first();
  await expect(newTile).toBeVisible();
  await expect(newTile.locator('strong')).toHaveText('1');

  // ③ 换窗口 → 下钻请求必须跟着换；横幅数字与格子数字逐字相等
  await page.locator('.t-select input.t-input__inner').first().click();
  await page.locator('.t-select-option', { hasText: '近 90 天' }).first().click();
  await newTile.click();
  await expect.poll(() => drillHits.length, { timeout: 15000 }).toBeGreaterThan(0);
  const last = drillHits[drillHits.length - 1];
  expect(last).toContain('metric=new');
  expect(last).toContain('days=90');
  await expect(page.getByTestId('acq-drill-total')).toHaveText('1', { timeout: 15000 });

  // 返回后横幅消失、列表回得来（回不去的下钻等于把后台藏了一半）
  await page.getByRole('button', { name: /返回活码列表/ }).click();
  await expect(page.getByTestId('acq-drill-total')).toHaveCount(0);
  await expect(row).toBeVisible({ timeout: 10000 });

  // ④ 二维码弹窗：链接逐字用后端值，图片请求真的打向本码
  const qrReq: string[] = [];
  page.on('request', (r) => { if (r.url().includes('/qr.png')) qrReq.push(r.url()); });
  await row.getByRole('button', { name: /二维码/ }).click();
  await expect.poll(() => qrReq.length, { timeout: 15000 }).toBeGreaterThan(0);
  expect(qrReq[0]).toContain(`/codes/${codeId}/qr.png`);
  // 链接逐字用后端值：弹窗里显示的就是接口返回的 link（前端不自己拼域名，否则物料上的域
  // 与扫码后进的域会不一致）。必须作用域到 .t-dialog__body —— 列表每行也有同一个链接文本，
  // 全局 getByText 会 strict-mode 命中多元素（首跑实锤：行链接 + 弹窗 div 共 3 个）。
  await expect(page.locator('.t-dialog__body').getByText(link, { exact: true })).toBeVisible({ timeout: 10000 });
  await page.getByRole('button', { name: '关闭' }).click();

  const body = await page.locator('body').innerText();
  expect(body).not.toMatch(/NaN|undefined|\[object Object\]/);

  // 收尾：停用即对外不可见（没有删除接口是刻意的，行由清理脚本按 e2e活码 前缀回收）
  const off = await request.post(`${BASE}/api/v1/admin/acquisition/codes/${codeId}/status`, {
    headers: auth, data: { active: false },
  });
  expect(off.ok()).toBeTruthy();
});

// 21. R-4 超管代管租户检索：搜索真走服务端，且换一批结果不会把当前代管那一家弄丢（2026-09-24 残项收口）
// 起因：/super/tenants 此前只有分页没有检索，代管下拉拿的是后端缺省首页，清库后承载历史
// 数据的种子租户排在最新若干家之外，超管在界面上再也代管不到它——当时的 e2e 是直接写
// localStorage 绕过的（见 seedDesktopLogin 注释），那是绕过不是修复。
// 后端那条检索链由 smoke §三十八 无条件钉死（含"page_size=1 时最旧一家看不见"的对照前提）；
// 本项只钉界面这一段，四件事各不重复：
//  ① 点「搜索」真的发出带 q= 的请求——在前端挑数据的话，租户一多就又只剩首页；
//  ② 下拉候选与服务端同一关键字的命中结果**逐字相同**（页面没自己加戏也没漏）；
//  ③ 选定后租户作用域请求真带上 X-Tenant-ID（下拉只是显示了、没接进请求头＝白做）；
//  ④ 再搜一个谁都不命中的关键字，当前代管那一家**必须还挂在下拉里**：它一消失，下拉显示
//     空白，看着像"代管被取消了"，而请求还在往那个租户发 X-Tenant-ID——这条链上最危险的错位。
test('超管代管租户检索走服务端且换批结果不丢当前代管', async ({ page, request }) => {
  const tid = await pickOperableTenant(request);
  const token = await adminToken(request);
  const auth = { Authorization: `Bearer ${token}` };
  // 关键字取该家的编码（无编码则取名称）——只用它自己的信息，不猜库里的其它租户
  const byId = await request.get(`${BASE}/api/v1/super/tenants?q=${encodeURIComponent(tid)}&page_size=100`, { headers: auth });
  const byIdRows = ((((await byId.json())?.data ?? {}).list ?? []) as { id?: number; name?: string; code?: string }[]);
  // 纯数字关键字还带名称/编码模糊这条腿，所以必须按 ID 精确挑出那一行，不能拿 list[0]
  const row = byIdRows.find((t) => String(t.id) === tid) ?? {};
  const keyword = String(row.code || row.name || '');
  expect(keyword, `租户 ${tid} 既无编码也无名称，本项前置不成立`).toBeTruthy();
  const expectResp = await request.get(`${BASE}/api/v1/super/tenants?q=${encodeURIComponent(keyword)}&page_size=100`, { headers: auth });
  const expectIds = ((((await expectResp.json())?.data ?? {}).list ?? []) as { id?: number }[])
    .map((t) => String(t.id ?? '')).filter((s) => s && s !== '0').sort();
  expect(expectIds, '服务端按该关键字应有命中').toContain(tid);

  await seedDesktopLogin(page, request);
  const qUrls: string[] = [];
  const sentHeaders: string[] = [];
  page.on('request', (r) => {
    if (r.url().includes('/super/tenants') && r.url().includes('q=')) qUrls.push(r.url());
    const h = r.headers()['x-tenant-id'];
    if (h) sentHeaders.push(h);
  });
  const impSel = page.locator('select[aria-label="代管租户"]');
  const optionValues = () => impSel.locator('option')
    .evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value).filter(Boolean).sort());

  await page.goto('/admin');
  await expect(impSel).toBeVisible({ timeout: 10000 });

  await page.getByLabel('搜索租户').fill(keyword);
  await page.getByRole('button', { name: '搜索' }).click();
  await expect.poll(() => qUrls.length, { timeout: 15000 }).toBeGreaterThan(0);
  await expect.poll(optionValues, { timeout: 15000 }).toEqual(expectIds);

  await impSel.selectOption(tid);
  await expect.poll(() => sentHeaders.includes(tid), { timeout: 20000 }).toBe(true);
  expect(await page.evaluate((k) => localStorage.getItem(k), 'scrm_impersonate_tenant')).toBe(tid);

  // ④ 换一批"谁都不命中"的搜索结果：候选清空，但当前代管那一家不能跟着消失
  await page.getByLabel('搜索租户').fill('绝无此租户zzz');
  await page.getByRole('button', { name: '搜索' }).click();
  await expect.poll(optionValues, { timeout: 15000 }).toEqual([tid]);
  await expect(impSel).toHaveValue(tid);
});
