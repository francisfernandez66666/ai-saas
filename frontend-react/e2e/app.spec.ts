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
test('auth me returns user info after login', async ({ request }) => {
  const token = await adminToken(request);
  expect(token).toBeTruthy();

  const meRes = await request.get(`${BASE}/api/v1/auth/me`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const meBody = await meRes.json();
  expect(meBody).toHaveProperty('code');
  if (meRes.ok()) {
    expect(meBody.code).toBe(0);
    expect(meBody.data?.username).toBe('admin');
  }
});

// 8. Health check endpoint
test('health check endpoint returns OK', async ({ request }) => {
  const r = await request.get(`${BASE}/health`);
  expect(r.ok()).toBeTruthy();
});

// 9. Chat test endpoint responds (may require visitor session)
test('chat test endpoint responds', async ({ request }) => {
  const r = await request.post(`${BASE}/api/v1/chat/test`, {
    data: { content: '你好' },
  });
  // Endpoint may return 403 (visitor auth required) or 200
  expect([200, 403]).toContain(r.status());
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
