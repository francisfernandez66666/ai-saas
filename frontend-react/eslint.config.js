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
)
