import { createRouter, createWebHistory } from 'vue-router'
import StagePage from '../pages/StagePage.vue'
import SettingsPage from '../pages/SettingsPage.vue'
import PlatformPage from '../pages/PlatformPage.vue'
import AvatarsPage from '../pages/AvatarsPage.vue'
import AvatarEditPage from '../pages/AvatarEditPage.vue'
import SessionsPage from '../pages/SessionsPage.vue'
import PreparePage from '../pages/PreparePage.vue'
import ModelsPage from '../pages/ModelsPage.vue'

export default createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/prepare', component: PreparePage },
    { path: '/models', component: ModelsPage },
    { path: '/', component: StagePage },
    // 无头公开采集页：仅此路由允许把 RTC 远端音轨送入 PCM bridge。
    { path: '/stage', component: StagePage, meta: { rtcAudioCapture: true } },
    { path: '/settings', component: SettingsPage },
    { path: '/platform', component: PlatformPage },
    { path: '/avatars', component: AvatarsPage },
    { path: '/avatars/new', component: AvatarEditPage },
    { path: '/avatars/:id/edit', component: AvatarEditPage },
    { path: '/sessions', component: SessionsPage },
    { path: '/sessions/:id', component: SessionsPage }
  ]
})
