// Loopback-only UI fixture. Never packaged in web-admin/dist.
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath } from 'node:url'
const root=fileURLToPath(new URL('.',import.meta.url))
const src=fileURLToPath(new URL('../../src',import.meta.url))
let scenario='active', failedCommit=false, dismissed=false
const status=()=>({enabled:true,status:['near','renewed','perpetual','failure','sync_failure'].includes(scenario)?'active':scenario,management_allowed:['active','near','renewed','perpetual','failure','sync_failure'].includes(scenario),domain:scenario==='unconfigured'?'':'panel.example.test',license_id:'lic-isolated',revision:3,expires_at:scenario==='perpetual'?null:new Date(Date.now()+(['near','failure'].includes(scenario)?2:scenario==='expired'?-2:scenario==='renewed'?60:30)*86400000).toISOString(),checked_at:new Date().toISOString(),reminder_due:!dismissed&&['near','expired','failure'].includes(scenario),renewal_url:'https://ht.hongle.work/',last_error:scenario==='unconfigured'?'请先运行发行包内 setup-panel-license.sh 接入授权配置':undefined})
const server=await createServer({configFile:false,root,plugins:[vue(),{name:'isolated-license-fixture',configureServer(server){ server.middlewares.use(async(req,res,next)=>{
 if(!req.url?.startsWith('/api/')) return next()
 let body='';for await(const chunk of req)body+=chunk
 res.setHeader('Content-Type','application/json');res.setHeader('Cache-Control','no-store')
 const send=(value,code=200)=>{res.statusCode=code;res.end(JSON.stringify(value))}
 if(req.url==='/api/v1/auth/login')return send({access_token:'isolated-preview-session',expires_at:new Date(Date.now()+3600000).toISOString(),user:{id:'preview-admin',username:'preview',display_name:'隔离验收管理员',role:'super_admin'}})
 if(req.headers.authorization!=='Bearer isolated-preview-session')return send({error:{code:'unauthorized',message:'请登录隔离验收账号'}},401)
 if(req.url==='/api/preview/scenario'){scenario=JSON.parse(body).scenario;dismissed=false;failedCommit=false;return send(status())}
 if(req.url==='/api/v1/license/status')return send(status())
 if(req.url==='/api/v1/license/reminders/dismiss'){if(scenario==='failure')return send({error:{code:'fixture_failed',message:'隔离验收：保存失败'}},503);dismissed=true;return send(status())}
 if(req.url==='/api/v1/license/sync'){if(['failure','sync_failure'].includes(scenario))return send({error:{code:'license_sync_busy',message:'授权同步过于频繁，请在 10 秒后重试'}},429);return send(status())}
 if(req.url==='/api/v1/license/activation/prepare')return send({dns_name:'_hl-license.panel.example.test',dns_value:'hl-license-v2=isolated-proof',expires_at:new Date(Date.now()+600000).toISOString()})
 if(req.url==='/api/v1/license/activation/commit'){if(!failedCommit){failedCommit=true;return send({error:{code:'license_sync_failed',message:'隔离验收：DNS TXT 尚未生效，请重试'}},502)}scenario='renewed';dismissed=true;return send(status())}
 send({error:{code:'not_found',message:'隔离验收接口不存在'}},404)
 })}}],resolve:{alias:{'@':src}},server:{host:'127.0.0.1',port:4197,strictPort:true,fs:{allow:[fileURLToPath(new URL('../..',import.meta.url))]}}})
await server.listen();server.printUrls()
