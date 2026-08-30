// 正文容器样式（最大宽度居中，行高宽松）
const body: React.CSSProperties = {
  fontFamily: '-apple-system,"PingFang SC","Microsoft YaHei",sans-serif',
  maxWidth: 860,
  margin: '0 auto',
  padding: '32px 20px',
  color: '#1f2937',
  lineHeight: 1.8,
}
// 一级标题样式（主色下边框）
const h1: React.CSSProperties = { fontSize: 24, borderBottom: '2px solid var(--pri)', paddingBottom: 10 }
// 小节标题样式
const h2: React.CSSProperties = { fontSize: 18, marginTop: 32, color: '#4338ca' }
// 元信息（更新日期、版权）灰色小字样式
const meta: React.CSSProperties = { color: '#94a3b8', fontSize: 13 }
// 品牌名高亮样式（用在正文中强调平台名）
const brand = { color: '#4f46e5', fontWeight: 600 } as React.CSSProperties

// 隐私政策静态页：展示《个人信息保护法》等合规条款，无业务接口依赖
export default function PrivacyPolicy() {
  return (
    <div style={body}>
      <h1 style={h1}>跨山 LexCross 隐私政策</h1>
      <p style={meta}>最近更新日期：2026年8月27日 ｜ 版本：v2026.08.27</p>

      <p>
        跨山（LexCross，以下简称"我们"）深知个人信息对您的重要性，并致力于按照《中华人民共和国个人信息保护法》《网络安全法》《数据安全法》及 GB/T 35273《个人信息安全规范》等要求保护您的个人信息。本政策说明
        <span style={brand}>AI-SCRM 平台</span>如何收集、使用、存储、共享与保护信息。您注册即表示已阅读并同意本政策。
      </p>

      <h2 style={h2}>一、适用范围与联系人</h2>
      <p>本政策适用于您使用跨山 AI-SCRM 平台及相关服务。如有疑问，请联系：<a href="mailto:privacy@lexcross.example">privacy@lexcross.example</a>。</p>

      <h2 style={h2}>二、我们收集的信息</h2>
      <p>我们遵循"最小必要"原则，仅收集实现核心功能所需的信息：</p>
      <ul>
        <li><strong>账号与企业信息</strong>：管理员/成员用户名、密码（哈希存储）、真实姓名、企业名称、行业、部门；</li>
        <li><strong>联系信息</strong>：邮箱、手机号（用于验证码、通知与账单）；</li>
        <li><strong>对话与业务数据</strong>：您录入的客户资料、与客户的聊天内容、留资线索、标签与跟进记录（用于提供 AI 接待、策略推理与销售管理）；</li>
        <li><strong>使用与设备信息</strong>：登录 IP、设备类型、浏览器、操作日志、访问时间（用于安全风控与服务优化）。</li>
      </ul>
      <p>我们不会强制收集与上述功能无关的扩展信息；用于个性化推荐的扩展数据须经您单独授权，默认关闭。</p>

      <h2 style={h2}>三、信息使用目的</h2>
      <p>我们使用上述信息以：提供并维护 AI-SCRM 核心功能；进行身份验证与防薅风控；发送验证码、系统通知与账单；在授权范围内训练与优化模型（业务数据所有权归您，平台不主张权益）；履行法定义务。</p>

      <h2 style={h2}>四、信息共享与披露</h2>
      <p>4.1 我们<strong>不会与任何第三方共享您的个人信息</strong>，除非获得您的明确同意，或根据法律法规/政府主管部门强制性要求。</p>
      <p>4.2 为提供服务，我们可能委托处理：邮件发送服务商（如阿里云 SMTP）、支付服务商（处理账单）。我们对受托方签署保密与数据处理协议，要求其按本政策标准保护信息。</p>
      <p>4.3 因合并、收购或破产导致信息转让的，我们将要求受让方继续受本政策约束；发生公开披露的，仅在获得您明确同意或法律要求时进行。</p>

      <h2 style={h2}>五、数据存储与安全</h2>
      <p>5.1 您的数据存储于中国大陆境内（默认云数据库），不向境外传输；如未来涉及跨境，我们将依法完成安全评估或标准合同备案。</p>
      <p>5.2 我们采取加密传输与存储、基于角色的访问控制、安全审计等措施防止未经授权的访问、泄露或丢失。发生数据泄露时，我们将在法定时限内通知监管与受影响用户。</p>

      <h2 style={h2}>六、您的权利</h2>
      <p>您依法享有以下权利，可通过后台或上述邮箱行使：</p>
      <ul>
        <li>访问与复制：获取我们持有的您的个人信息副本；</li>
        <li>更正：要求更正不准确的信息；</li>
        <li>删除（被遗忘权）：要求删除您的个人信息（法律另有规定的除外），我们将在 15 个工作日内完成并反馈；</li>
        <li>撤回同意：撤回后我们将停止基于该同意的处理，但不影响撤回前已进行的处理；</li>
        <li>账户注销：您可发起注销，我们将删除或匿名化您的业务数据。</li>
      </ul>

      <h2 style={h2}>七、儿童信息</h2>
      <p>本平台面向企业客户，不面向未成年人提供；我们不会故意收集未满 14 周岁儿童的个人信息。</p>

      <h2 style={h2}>八、Cookie 与技术跟踪</h2>
      <p>除保障服务正常运行所必需的技术 Cookie 外，用于分析或营销的跟踪类 Cookie 须经您授权，您可在浏览器中管理。</p>

      <h2 style={h2}>九、政策更新</h2>
      <p>本政策可能随法律法规或产品实践变更；重大变更将提前通知并更新"最近更新日期"，旧版本存档供查阅。</p>

      <p style={meta}>© 2026 跨山 LexCross · AI-SCRM 平台。本政策为通用模板，正式使用前请经合格法律专业人士审阅。</p>

      <div className="footer-legal">
        <a href="/user-agreement">用户协议</a> · <a href="/privacy-policy">隐私政策</a> · 跨山 LexCross AI-SCRM 平台
      </div>
    </div>
  )
}
