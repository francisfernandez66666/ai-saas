// 顾问台「调整客户阶段」弹窗单测（P2-14 i18n 迁移补盲，2026-10-16）。
//
// 这个弹窗此前零覆盖：i18n 迁移时两个 label 文本（「目标阶段」「到店子状态」）被整体丢掉，
// 下拉框退化成没有可见标签的裸 select，而全量 274 条用例一条都没红——因为没人渲染过它。
// 本文件钉四件事：
//   1. 弹窗标题与「目标阶段」label 必须在场（丢了文本，顾问面对两个裸下拉不知道在选什么）；
//   2. 「到店子状态」label 仅 journey_stage==='arrived' 时出现（条件渲染语义 + 文本不得丢）；
//   3. 子状态下拉三个选项文案齐全（无/已试驾/已报价）——走 t() 的键若缺失会回退成键名，
//      在这里立刻露出来；
//   4. 反证：非 arrived 时子状态下拉整段不渲染（label 与 select 同生同死）。
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { StageDialog, type StageForm } from '../dialogs'

// renderDlg 以可见态渲染 StageDialog（回调全部打桩），供各用例断言 label/选项在场。
function renderDlg(form: StageForm) {
  return render(
    <StageDialog
      visible
      form={form}
      onForm={vi.fn()}
      onClose={vi.fn()}
      onConfirm={vi.fn()}
    />,
  )
}

describe('StageDialog 调整客户阶段弹窗', () => {
  it('弹窗标题与目标阶段 label 在场', () => {
    renderDlg({ journey_stage: '', journey_sub_stage: '' })
    expect(screen.getByText('调整客户阶段')).toBeInTheDocument()
    // 原来弱在哪：迁移前无任何断言覆盖本弹窗，label 文本被丢后测试全绿；
    // 现在钉 label 文本本身（dialogs.tsx StageDialog 内 <label> 的直接子文本）。
    expect(screen.getByText('目标阶段')).toBeInTheDocument()
  })

  it('未选到店时不出现子状态；选了到店才出现', () => {
    const { unmount } = renderDlg({ journey_stage: 'negotiating', journey_sub_stage: '' })
    expect(screen.queryByText('到店子状态')).toBeNull()
    // label 与 select 同生同死：反证整段条件渲染，而不是只藏了文本
    expect(screen.queryByRole('option', { name: '已试驾' })).toBeNull()
    unmount()

    renderDlg({ journey_stage: 'arrived', journey_sub_stage: '' })
    expect(screen.getByText('到店子状态')).toBeInTheDocument()
  })

  it('子状态下拉三个选项文案齐全（无/已试驾/已报价）', () => {
    renderDlg({ journey_stage: 'arrived', journey_sub_stage: '' })
    // 选项文案走 t()：键缺失时 t() 回退为键名（如 'advisor.dlgTestDriven'），这里会立刻露出来
    for (const opt of ['无', '已试驾', '已报价']) {
      expect(screen.getByRole('option', { name: opt })).toBeInTheDocument()
    }
  })
})
