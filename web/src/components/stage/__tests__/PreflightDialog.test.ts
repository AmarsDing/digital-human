import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import PreflightDialog from '../PreflightDialog.vue'
import type { LivePreflightResult } from '../../../services/api/live'

function result(ready: boolean): LivePreflightResult {
  return {
    checkId: 'check-1',
    preflightToken: 'token-1',
    personaId: 'p1',
    revision: 7,
    ready,
    mode: 'local-preview',
    draftId: 'd1',
    draftRevision: 2,
    inputRevisions: {},
    resultDigest: 'sha256:test',
    observedAtMs: 1000,
    expiresAtMs: 2000,
    checks: [{
      code: 'tts', status: ready ? 'pass' : 'fail', blocking: !ready,
      source: 'capability_probe', observedAtMs: 1000, detail: ready ? '' : '未配置'
    }]
  }
}

describe('PreflightDialog', () => {
  it('预检未通过时禁止开播，重新预检只发出 retry', async () => {
    const w = mount(PreflightDialog, {
      props: { open: true, result: result(false), busy: false, error: '语音合成服务尚未配置' }
    })

    const start = w.findAll('button').find((button) => button.text() === '开始直播')!
    expect(start.attributes('disabled')).toBeDefined()
    expect(w.get('[role="alert"]').text()).toContain('语音合成服务尚未配置')
    await start.trigger('click')
    expect(w.emitted('start')).toBeUndefined()

    const retry = w.findAll('button').find((button) => button.text() === '重新预检')!
    await retry.trigger('click')
    expect(w.emitted('retry')).toHaveLength(1)
    expect(w.emitted('start')).toBeUndefined()
    w.unmount()
  })

  it('预检通过后仍需用户明确点击才发出 start', async () => {
    const w = mount(PreflightDialog, {
      props: { open: true, result: result(true), busy: false, error: '' }
    })

    expect(w.emitted('start')).toBeUndefined()
    const start = w.findAll('button').find((button) => button.text() === '开始直播')!
    expect(start.attributes('disabled')).toBeUndefined()
    await start.trigger('click')
    expect(w.emitted('start')).toHaveLength(1)
    w.unmount()
  })
})
