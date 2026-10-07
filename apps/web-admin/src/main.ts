import { createApp } from 'vue'

import App from './App.vue'
import { router } from './router'
import './styles.css'
import './styles/business.css'
import './styles/nodes.css'
import './styles/probe.css'
import './styles/site.css'

createApp(App).use(router).mount('#app')
