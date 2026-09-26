import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'

export default tseslint.config(
  { ignores: ['dist'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2020,
      globals: {
        console: 'readonly',
        document: 'readonly',
        window: 'readonly',
        setTimeout: 'readonly',
        setInterval: 'readonly',
        clearTimeout: 'readonly',
        clearInterval: 'readonly',
        fetch: 'readonly',
        URL: 'readonly',
        FormData: 'readonly',
        alert: 'readonly',
        confirm: 'readonly',
        prompt: 'readonly',
        localStorage: 'readonly',
        location: 'readonly',
        history: 'readonly',
        navigator: 'readonly',
        XMLHttpRequest: 'readonly',
        AbortController: 'readonly',
      },
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
      '@typescript-eslint/no-explicit-any': 'warn',
      '@typescript-eslint/no-unused-vars': ['warn', { argsIgnorePattern: '^_' }],
      // G3 收口(2026-09-14)：
      //  1) CJK 界面 JSX 文本里用全角空格（U+3000）做视觉分隔是有意为之，非笔误——
      //     no-irregular-whitespace 放行注释与 JSX 文本，仍拦截字符串/模板外的真实误入。
      //  2) eslint-plugin-react-hooks v6 的 set-state-in-effect / refs / immutability
      //     属 React Compiler 前瞻建议（"effect 内同步 setState 可能多余"），
      //     现有 loadX() in useEffect 是刻意取数模式，暂降为 warn，不作 CI 硬门。
      'react-hooks/set-state-in-effect': 'warn',
      'react-hooks/refs': 'warn',
      'react-hooks/immutability': 'warn',
      'no-irregular-whitespace': ['error', { skipComments: true, skipJSXText: true }],
    },
  },
  {
    // FIX-8(2026-09-27)：金额口径单点的**负向闸**。
    // 规则只有一条——界面输入框里是元、中间步骤与提交体里全是分（整数），换算只认 lib/money。
    // 为什么用 AST 选择器而不是 grep `/ 100`：本批给每处替换都留了中文说明注释
    // （"旧写法 (cents/100).toFixed(0) 把 ¥99.50 显示成 ¥100"），粗 grep 会把这些注释当成
    // 违规命中、逼人去删说明或把闸放宽——本仓已有"负向 grep 锁误伤注释"的教训。
    // 走 no-restricted-syntax 只看真实代码节点：注释、字符串里的 `/ 100` 一律不打。
    // 范围限定页面/组件层：lib/money.ts 自己当然要除 100，那是单点本体。
    files: ['src/pages/**/*.{ts,tsx}', 'src/components/**/*.{ts,tsx}'],
    rules: {
      'no-restricted-syntax': [
        'error',
        {
          selector: 'BinaryExpression[operator="/"] > Literal[value=100]',
          message:
            '金额换算请走 lib/money（fenToYuan / fenToYuanCompact / yuanToFen），不要写裸 `x / 100`（FIX-8：两处浮点往返 + toFixed(0) 会把 ¥99.50 显示成 ¥100，页面价与扣款额不一致）',
        },
      ],
    },
  },
)
