/* 数字人 RTC factory shim（F49 自托管）。
 * 把 vendor 下的厂商 SDK 包装成统一 adapter，暴露唯一出口 window.__DH_RTC_FACTORY__；
 * 业务层只经 web/src/components/rtc/adapter.ts 消费本工厂，不直接触碰厂商全局。
 *
 * 固化版本：
 *   - artc：AliRTC Web SDK 7.1.9（aliyun-rtc-sdk-7.1.9.js，蓝本 stage/index.html:8 同源）
 *   - trtc：TRTC Web SDK 5.00.1600（trtc-sdk-5.00.1600.js，蓝本 :1165 同源）
 *
 * 观看侧约定（对齐蓝本 joinArtc/joinTrtc）：只订阅不推流，上行音频不经 RTC。
 */
(function () {
  'use strict'

  function unavailable(provider) {
    return {
      join: function () {
        return Promise.reject(
          new Error('RTC SDK 未自托管（' + provider + '），vendor 目录缺少对应 SDK 文件')
        )
      },
      leave: function () { return Promise.resolve() },
      mute: function () {},
      destroy: function () {},
      onRemoteAudio: function () {}
    }
  }

  /* ── AliRTC（artc）────────────────────────────────────────────── */
  function createArtc() {
    var AliRtcEngine = window.AliRtcEngine
    if (!AliRtcEngine) return unavailable('artc')
    var engine = null
    var videoEl = null
    var audioHandler = function () {}
    var exposedStream = null

    function bindView(uid) {
      if (!engine || !videoEl) return
      // 1=大流 2=小流（蓝本两档都绑，由 SDK 按可用流渲染）。
      try { engine.setRemoteViewConfig(videoEl, uid, 1) } catch (e) { /* 忽略档位差异 */ }
      try { engine.setRemoteViewConfig(videoEl, uid, 2) } catch (e) { /* 忽略档位差异 */ }
      try {
        var p = videoEl.play()
        if (p && typeof p.catch === 'function') p.catch(function () { /* 自动播放被拦由上层提示 */ })
      } catch (e) { /* jsdom 等环境无 play */ }
      // 只暴露 SDK 已绑定到远端 video 的真实 MediaStream；绝不请求麦克风或系统回录。
      var stream = videoEl.srcObject
      if (stream && stream !== exposedStream && typeof stream.getAudioTracks === 'function' && stream.getAudioTracks().length) {
        exposedStream = stream
        audioHandler({ kind: 'stream', stream: stream })
      }
    }

    return {
      onRemoteAudio: function (handler) { audioHandler = handler },
      join: async function (creds, video) {
        videoEl = video
        var supported = { support: true }
        try { supported = await AliRtcEngine.isSupported() } catch (e) { /* 老版本无此接口 */ }
        if (supported && supported.support === false) {
          throw new Error('当前浏览器不支持 AliRTC: ' + (supported.reason || ''))
        }
        try { AliRtcEngine.setLogLevel(0) } catch (e) { /* 忽略 */ }
        engine = AliRtcEngine.getInstance()
        engine.on('remoteUserOnLineNotify', function (uid) {
          // 推流端上线后 SDP/首帧有延迟，蓝本双时间点兜底绑定。
          setTimeout(function () { bindView(uid) }, 400)
          setTimeout(function () { bindView(uid) }, 2200)
        })
        engine.on('videoSubscribeStateChanged', function (uid) { bindView(uid) })
        var call = async function (m) {
          var args = Array.prototype.slice.call(arguments, 1)
          if (engine && typeof engine[m] === 'function') await engine[m].apply(engine, args)
        }
        try { await call('setChannelProfile', 'communication') } catch (e) { /* 忽略 */ }
        try { await call('setDefaultPublishLocalAudioStream', false) } catch (e) { /* 忽略 */ }
        try { await call('setDefaultPublishLocalVideoStream', false) } catch (e) { /* 忽略 */ }
        try { await call('setDefaultSubscribeAllRemoteAudioStreams', true) } catch (e) { /* 忽略 */ }
        try { await call('setDefaultSubscribeAllRemoteVideoStreams', true) } catch (e) { /* 忽略 */ }
        // 重连兜底（蓝本 2026-10-20）：上一场频道成员未掉线时 join 会 reject，重试一次。
        var lastErr = null
        for (var attempt = 1; attempt <= 2; attempt++) {
          try {
            await engine.joinChannel(creds.token, creds.userId)
            return
          } catch (err) {
            lastErr = err
            if (attempt === 1) await new Promise(function (r) { setTimeout(r, 1500) })
          }
        }
        throw (lastErr instanceof Error ? lastErr : new Error(String(lastErr || 'RTC 入会失败')))
      },
      leave: async function () {
        var e = engine
        engine = null
        videoEl = null
        exposedStream = null
        if (e) {
          try { await e.leaveChannel() } catch (err) { /* 离会失败不阻断本地清理 */ }
        }
      },
      mute: function () { /* 观看侧无声卡上行；播放音量由 video 元素 muted 控制 */ },
      destroy: function () {
        engine = null
        videoEl = null
      }
    }
  }

  /* ── TRTC（trtc）─────────────────────────────────────────────── */
  function createTrtc() {
    var TRTC = window.TRTC
    if (!TRTC) return unavailable('trtc')
    var client = null
    var videoEl = null
    var audioHandler = function () {}

    return {
      onRemoteAudio: function (handler) { audioHandler = handler },
      join: async function (creds, video) {
        videoEl = video
        client = TRTC.create()
        client.on(TRTC.EVENT.REMOTE_VIDEO_AVAILABLE, async function (e) {
          try {
            await client.startRemoteVideo({
              userId: e.userId,
              streamType: e.streamType,
              view: videoEl,
              option: { fillMode: 'contain' }
            })
            var p = videoEl.play()
            if (p && typeof p.catch === 'function') p.catch(function () { /* 上层提示 */ })
          } catch (err) { /* 订阅失败由状态轮询兜底 */ }
        })
        client.on(TRTC.EVENT.REMOTE_AUDIO_AVAILABLE, async function (e) {
          try {
            await client.startRemoteAudio({ userId: e.userId })
            await client.callExperimentalAPI('enableAudioFrameEvent', {
              enable: true, userId: e.userId, sampleRate: 48000, channelCount: 1
            })
          } catch (err) { /* 远端音频未实证；不得回退到麦克风或系统回录 */ }
        })
        client.on(TRTC.EVENT.AUDIO_FRAME, function (e) {
          if (e && e.data) audioHandler({ kind: 'samples', samples: e.data })
        })
        await client.enterRoom({
          strRoomId: creds.roomId,
          scene: 'rtc',
          sdkAppId: Number(creds.appId),
          userId: creds.userId,
          userSig: creds.token
        })
      },
      leave: async function () {
        var c = client
        client = null
        videoEl = null
        if (c) {
          try { await c.exitRoom() } catch (err) { /* 不阻断 */ }
          try { if (typeof c.destroy === 'function') c.destroy() } catch (err) { /* 不阻断 */ }
        }
      },
      mute: function () { /* 同 artc：由 video 元素 muted 控制 */ },
      destroy: function () {
        var c = client
        client = null
        videoEl = null
        if (c && typeof c.destroy === 'function') {
          try { c.destroy() } catch (err) { /* 不阻断 */ }
        }
      }
    }
  }

  window.__DH_RTC_FACTORY__ = {
    create: function (provider) {
      if (provider === 'trtc') return createTrtc()
      return createArtc()
    }
  }
})()
