// Playwright 真浏览器 E2E（17 项）：落地页/定价/注册/登录漏斗/受保护路由/端点连通/E10 文档站渲染，
// 外加 390px 窄屏三台（Admin/Super 折叠下拉、Org 单栏不炸版，P1-10 批二）、D2 AI 贡献度卡片、
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
// 断言口径：①固定 200px 左栏（.t-layout__aside）在窄屏消失，折叠为顶栏下拉菜单；
// ②整页横向滚动宽度不超视口（防内容炸版，表格横滚应局限在 .t-table__content 内）；
// ③下拉切 Tab 真实联动（/admin 选"回复速度"出配置动作条）。
async function seedDesktopLogin(page: import('@playwright/test').Page, request: APIRequestContext) {
  const token = await adminToken(request);
  // addInitScript 在每次导航前注入登录三键（与真实登录后的 localStorage 形态一致）
  await page.addInitScript(([t]) => {
    localStorage.setItem('scrm_auth_token', t);
    localStorage.setItem('role', 'super_admin');
    localStorage.setItem('username', 'admin');
  }, [token]);
}

test.describe('P1-10 390px 桌面三台可达', () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test('/admin 折叠为顶栏下拉并可切 Tab', async ({ page, request }) => {
    await seedDesktopLogin(page, request);
    await page.goto('/admin');
    const menu = page.locator('select[aria-label="管理菜单"]');
    await expect(menu).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.t-layout__aside')).toHaveCount(0);
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
    await expect(page.locator('.t-layout__aside')).toHaveCount(0);
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
// 断言口径：
//  ① 选定代管租户后卡片标题可见（端点 4xx/500 会让卡片进失败态，此处即红灯）
//  ② 切"近 7 天"必须发出 days=7 的真实请求（监听网络，防"只改样式不改数据"）
//  ③ 页面无 NaN/Infinity 文本（0 除 0 未兜底的典型症状，新租户零数据必踩）
test('D2 admin AI 贡献度卡片渲染且窗口切换发真实请求', async ({ page, request }) => {
  await seedDesktopLogin(page, request);
  const hits: string[] = [];
  page.on('request', (r) => {
    if (r.url().includes('/stats/ai-contribution')) hits.push(r.url());
  });
  await page.goto('/admin');

  // 超管必须先选定代管租户，租户作用域 Tab 才会真正挂载
  const impSel = page.locator('select[aria-label="代管租户"]');
  await expect(impSel).toBeVisible({ timeout: 10000 });
  const firstTenant = await impSel.locator('option').nth(1).getAttribute('value');
  expect(firstTenant).toBeTruthy();
  await impSel.selectOption(firstTenant!);

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
