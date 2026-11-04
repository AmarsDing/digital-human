import { createRouter, createWebHistory } from 'vue-router'
import StagePage from '../pages/StagePage.vue'
import SettingsPage from '../pages/SettingsPage.vue'
import PlatformPage from '../pages/PlatformPage.vue'
import AvatarsPage from '../pages/AvatarsPage.vue'
import AvatarEditPage from '../pages/AvatarEditPage.vue'
import PreparePage from '../pages/PreparePage.vue'

export default createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/prepare', component: PreparePage },
    { path: '/', component: StagePage },
    { path: '/settings', component: SettingsPage },
    { path: '/platform', component: PlatformPage },
    { path: '/avatars', component: AvatarsPage },
    { path: '/avatars/new', component: AvatarEditPage },
    { path: '/avatars/:id/edit', component: AvatarEditPage }
  ]
})
