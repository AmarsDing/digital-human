import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

vi.mock('../../../services/api/panels', async (importOriginal) => {
  const orig = await importOriginal<typeof import('../../../services/api/panels')>()
  return {
    ...orig,
    listSongs: vi.fn(),
    getSongStatus: vi.fn(),
    playSong: vi.fn(),
    stopSong: vi.fn(),
    deleteSong: vi.fn(),
    uploadSong: vi.fn()
  }
})

import { deleteSong, getSongStatus, listSongs, playSong } from '../../../services/api/panels'
import SongsPanel from '../SongsPanel.vue'

const listMock = vi.mocked(listSongs)
const statusMock = vi.mocked(getSongStatus)
const playMock = vi.mocked(playSong)
const deleteMock = vi.mocked(deleteSong)

function songState(source?: 'real' | 'fake' | 'unconfigured' | 'unknown') {
  return {
    file: '', script: '', song_playing: false, epoch: 0,
    caption_lines: [], lyrics_source: '', decoder: 'fake',
    ...(source === undefined ? {} : { source })
  } as never
}

describe('SongsPanel 能力矩阵点播门禁', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listMock.mockResolvedValue([{ name: 'tone.wav', size: 1024, mtime: 0, lib: 'default' }])
    playMock.mockResolvedValue(songState('real'))
  })

  it.each([
    { source: 'fake' as const, reason: 'fake 播放器' },
    { source: 'unconfigured' as const, reason: '真实播放服务未配置' },
    { source: 'unknown' as const, reason: '能力未知' },
    { source: undefined, reason: '后端未提供歌曲能力字段 source' }
  ])('source=$source 时禁用点播并显示原因，入口 fail-closed', async ({ source, reason }) => {
    statusMock.mockResolvedValue(songState(source))
    const w = mount(SongsPanel)
    await flushPromises()

    const button = w.findAll('button').find((b) => b.text() === '点播')!
    expect(button.attributes('disabled')).toBeDefined()
    expect(w.text()).toContain(reason)
    await button.trigger('click')
    expect(playMock).not.toHaveBeenCalled()
    w.unmount()
  })

  it('source=real 时允许点播并调用真实播放接口', async () => {
    statusMock.mockResolvedValue(songState('real'))
    const w = mount(SongsPanel)
    await flushPromises()

    const button = w.findAll('button').find((b) => b.text() === '点播')!
    expect(button.attributes('disabled')).toBeUndefined()
    await button.trigger('click')
    await flushPromises()
    expect(playMock).toHaveBeenCalledWith('tone.wav', '')
    w.unmount()
  })

  it('删除歌曲使用危险确认：默认取消、Esc 否定、busy 防重入', async () => {
    statusMock.mockResolvedValue(songState('real'))
    let release: () => void = () => undefined
    deleteMock.mockImplementation(() => new Promise<void>((resolve) => { release = resolve }))
    const w = mount(SongsPanel, { attachTo: document.body })
    await flushPromises()

    await w.findAll('button').find((b) => b.text() === '删除')!.trigger('click')
    const dialog = w.get('[role="alertdialog"]')
    expect(document.activeElement).toBe(dialog.get('button').element)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(deleteMock).not.toHaveBeenCalled()
    expect(w.find('[role="alertdialog"]').exists()).toBe(false)

    await w.findAll('button').find((b) => b.text() === '删除')!.trigger('click')
    const confirm = w.findAll('[role="alertdialog"] button').find((b) => b.text() === '删除')!
    await confirm.trigger('click')
    expect(w.get('[role="alertdialog"]').attributes('aria-busy')).toBe('true')
    await confirm.trigger('click')
    expect(deleteMock).toHaveBeenCalledTimes(1)
    release()
    await flushPromises()
    expect(w.find('[role="alertdialog"]').exists()).toBe(false)
    w.unmount()
  })

  it('删除失败保留危险对话框和错误，不清空待删除项', async () => {
    statusMock.mockResolvedValue(songState('real'))
    deleteMock.mockRejectedValue(new Error('删除失败'))
    const w = mount(SongsPanel)
    await flushPromises()

    await w.findAll('button').find((b) => b.text() === '删除')!.trigger('click')
    const confirm = w.findAll('[role="alertdialog"] button').find((b) => b.text() === '删除')!
    await confirm.trigger('click')
    await flushPromises()

    expect(w.find('[role="alertdialog"]').exists()).toBe(true)
    expect(w.get('[role="alertdialog"]').text()).toContain('删除失败')
    expect(w.get('[role="alertdialog"]').text()).toContain('tone.wav')
    w.unmount()
  })
})
