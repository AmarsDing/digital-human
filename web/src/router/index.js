import { createRouter, createWebHistory } from 'vue-router'
import StagePage from '../pages/StagePage.vue'
import SettingsPage from '../pages/SettingsPage.vue'

export default createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: StagePage },
    { path: '/settings', component: SettingsPage }
  ]
})
