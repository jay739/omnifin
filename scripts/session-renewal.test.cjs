const assert=require("node:assert/strict");
const Module=require("node:module");
const path=require("node:path");
const {buildSync}=require("esbuild");
const {JSDOM}=require("jsdom");
const dom=new JSDOM('<div id="login"><form class="form-login"><input id="login-user"><input id="login-password"><span class="submit">Sign in</span></form></div><div id="composer" class="modal" style="z-index:9000"><textarea id="draft">Keep this draft</textarea></div>',{url:"https://fixture.invalid/"});
Object.assign(global,{window:dom.window,document:dom.window.document,localStorage:dom.window.localStorage,CustomEvent:dom.window.CustomEvent});
const requests=[];
class Request{
 constructor(){this.headers={};requests.push(this);}
 open(method,url){this.method=method;this.url=url;}
 setRequestHeader(key,value){this.headers[key]=value;}
 getResponseHeader(key){return this.responseHeaders?.[key] || null;}
 send(data){this.data=data;}
 finish(status,response,responseHeaders={}){Object.assign(this,{readyState:4,status,response,responseHeaders});if(this.onreadystatechange)this.onreadystatechange();if(this.onloadend)this.onloadend();}
}
global.XMLHttpRequest=Request;
window.pages={Base:""};window.notifications={customError(){},connectionError(){}};window.lang={notif:()=>"",strings:()=>""};
const built=buildSync({stdin:{contents:'export {Login} from "./ts/modules/login.ts"; export {_get,_post,_upload,_download} from "./ts/modules/common.ts";',resolveDir:process.cwd()},bundle:true,format:"cjs",platform:"node",write:false});
const loaded=new Module(path.resolve("session-check.cjs"),module);loaded.paths=module.paths;loaded._compile(built.outputFiles[0].text,loaded.id);
const {Login,_get,_post,_upload,_download}=loaded.exports;
let shown=0,logins=0;
const modal={asElement:()=>document.getElementById("login"),show:()=>shown++,close(){}};
const login=new Login(modal,"/","none");
login.onLogin=()=>logins++;
login.login("fixture-admin","fixture-password");requests.at(-1).finish(200,{token:"expired-access",refresh:"saved-refresh"});
const tick=()=>new Promise(resolve=>setImmediate(resolve));
const denied={error:"Unauthorized",code:"AUTHENTICATION_REQUIRED"};
(async()=>{
 await tick();
 let completed=0;
 _get("/users",null,()=>completed++);const a=requests.at(-1);
 _get("/invites",null,()=>completed++);const b=requests.at(-1);
 a.finish(401,denied);b.finish(401,denied);await tick();
 const refreshes=requests.filter(r=>r.url.endsWith("/token/refresh"));
 assert.equal(refreshes.length,1,"concurrent expired requests should cause one refresh");
 refreshes[0].finish(200,{token:"fresh-access",refresh:"new-refresh"});await tick();
 const retries=requests.filter(r=>r.headers.Authorization==="Bearer fresh-access");assert.equal(retries.length,2);
 retries.forEach(r=>r.finish(200,{}));assert.equal(completed,2);assert.equal(logins,1,"silent refresh must not restart page initialization");
 assert.equal(shown,0);assert.equal(document.getElementById("draft").value,"Keep this draft");

 const refreshCount=()=>requests.filter(r=>r.url.endsWith("/token/refresh")).length;
 const lastRefresh=()=>requests.filter(r=>r.url.endsWith("/token/refresh")).at(-1);
 const originalRefreshes=refreshCount();
 _get("/late-a",null,()=>{});const lateA=requests.at(-1);
 _get("/late-b",null,()=>{});const lateB=requests.at(-1);
 lateA.finish(401,denied);await tick();lastRefresh().finish(200,{token:"late-token",refresh:"late-refresh"});await tick();requests.at(-1).finish(200,{});
 lateB.finish(401,denied);await tick();assert.equal(refreshCount(),originalRefreshes+1,"a late response for an old token must reuse the renewed token");requests.at(-1).finish(200,{});
 let writes=0;
 _post("/invites",{minutes:10},()=>writes++,true);const write=requests.at(-1);const body=write.data;
 write.finish(401,denied);await tick();lastRefresh().finish(200,{token:"write-token",refresh:"write-refresh"});await tick();
 assert.equal(requests.at(-1).url,"/invites");assert.equal(requests.at(-1).data,body);requests.at(-1).finish(200,{});assert.equal(writes,1);
 const beforeBusiness=refreshCount();
 _post("/matrix/login",{},()=>{},true);const business=requests.at(-1);const businessCount=requests.length;
 business.finish(401,{error:"Unauthorized"});await tick();assert.equal(requests.length,businessCount);assert.equal(refreshCount(),beforeBusiness,"a business 401 must not replay a write");
 _post("/uncertain-write",{},()=>{},true);const uncertain=requests.at(-1);const uncertainCount=requests.length;
 uncertain.finish(0,null);await tick();assert.equal(requests.length,uncertainCount,"network errors must not replay writes");
 const form=new window.FormData();let uploads=0;
 _upload("/backups/restore",form,()=>uploads++);const upload=requests.at(-1);
 upload.finish(401,denied);await tick();lastRefresh().finish(200,{token:"upload-token",refresh:"upload-refresh"});await tick();
 assert.equal(requests.at(-1).data,form);assert.equal(requests.at(-1).headers["Content-Type"],undefined);requests.at(-1).finish(200,{});assert.equal(uploads,1);
 let downloaded=0;const createElement=document.createElement.bind(document);const savedURL=global.URL;
 document.createElement=(tag,...args)=>{const element=createElement(tag,...args);if(tag==="a")element.dispatchEvent=()=>{downloaded++;return true};return element};
 global.URL={createObjectURL:()=>"blob:fixture",revokeObjectURL(){}};global.MouseEvent=window.MouseEvent;
 _download("/backups/fixture.bak","fixture.bak");const download=requests.at(-1);
 download.finish(401,{}, {"X-Omnifin-Session":"required"});await tick();lastRefresh().finish(200,{token:"download-token",refresh:"download-refresh"});await tick();
 requests.at(-1).finish(200,{});assert.equal(downloaded,1);document.createElement=createElement;global.URL=savedURL;
 let expiredEvents=0;document.addEventListener("session-expired",()=>expiredEvents++);
 _get("/expire-a",null,()=>{});const expireA=requests.at(-1);_get("/expire-b",null,()=>{});const expireB=requests.at(-1);
 expireA.finish(401,denied);expireB.finish(401,denied);await tick();lastRefresh().finish(401,{error:"Unauthorized"});await tick();
 assert.equal(shown,1);assert.ok(Number(document.getElementById("login").style.zIndex)>9000,"login must appear above an existing composer");assert.equal(expiredEvents,1);assert.equal(login.loggedIn,false);assert.equal(localStorage.getItem("omnifin_refresh_/"),null);
 const pausedCount=requests.length;_get("/paused-poll",null,()=>{});await tick();assert.equal(requests.length,pausedCount,"expired sessions must suppress protected polling");
 assert.equal(document.getElementById("draft").value,"Keep this draft");
 modal.onclose();assert.equal(shown,2,"dismissing mandatory sign-in must not leave an inaccessible locked page");
 login.login("fixture-admin","fixture-password");requests.at(-1).finish(200,{token:"resumed-token",refresh:"retained-refresh"});await tick();
 _get("/temporary",null,()=>{});requests.at(-1).finish(401,denied);await tick();
 assert.equal(lastRefresh().timeout,15000);lastRefresh().finish(503,null);await tick();
 assert.equal(localStorage.getItem("omnifin_refresh_/"),"retained-refresh");assert.equal(shown,2,"a temporary renewal failure must not discard the session");
 _get("/temporary-retry",null,()=>{});requests.at(-1).finish(401,denied);await tick();lastRefresh().finish(200,{token:"recovered-token",refresh:"recovered-refresh"});await tick();requests.at(-1).finish(200,{});
 const limited=refreshCount();_get("/still-rejected",null,()=>{});requests.at(-1).finish(401,denied);await tick();lastRefresh().finish(200,{token:"still-invalid",refresh:"still-invalid-refresh"});await tick();requests.at(-1).finish(401,denied);await tick();
 assert.equal(refreshCount(),limited+1);assert.equal(shown,3,"a request can be retried at most once");
 login.login("fixture-admin","fixture-password");requests.at(-1).finish(200,{token:"logout-access",refresh:"logout-refresh"});await tick();
 _get("/during-logout",null,()=>{});requests.at(-1).finish(401,denied);await tick();const pendingRefresh=lastRefresh();
 let reloads=0;global.location={reload:()=>reloads++};const logoutButton=createElement("button");login.bindLogout(logoutButton);logoutButton.onclick();requests.at(-1).finish(200,{});
 pendingRefresh.finish(200,{token:"obsolete-access",refresh:"obsolete-refresh"});await tick();assert.equal(window.token,"");assert.equal(reloads,1);
 window.pages.Base="/portal";localStorage.setItem("omnifin_refresh_/","admin-realm");localStorage.setItem("omnifin_refresh_/my/","user-realm");
 const userLogin=new Login({asElement:()=>document.getElementById("login"),show:()=>shown++,close(){}},"/my/","none");
 userLogin.login("","");assert.equal(requests.at(-1).url,"/portal/my/token/refresh");assert.equal(requests.at(-1).headers.Authorization,"Bearer user-realm");requests.at(-1).finish(200,{token:"user-access",refresh:"user-refresh"});await tick();
 _get("/my/details",null,()=>{});assert.equal(requests.at(-1).url,"/portal/my/details");requests.at(-1).finish(401,denied);await tick();assert.equal(lastRefresh().url,"/portal/my/token/refresh");assert.equal(lastRefresh().headers.Authorization,"Bearer user-refresh");
 lastRefresh().finish(401,{});await tick();assert.equal(localStorage.getItem("omnifin_refresh_/"),"admin-realm");assert.equal(localStorage.getItem("omnifin_refresh_/my/"),null);
 console.log("Session renewal, replay boundaries, realm, expiry and recovery checks passed.");dom.window.close();
})().catch(error=>{console.error(error);dom.window.close();process.exitCode=1;});
