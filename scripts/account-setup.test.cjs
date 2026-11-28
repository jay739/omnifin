const assert = require("node:assert/strict");
const path = require("node:path");
const Module = require("node:module");
const {buildSync} = require("esbuild");
const {JSDOM} = require("jsdom");
const dom = new JSDOM('<form><input id="name"><input id="email"><input id="password"><select><option value="setup">Setup</option></select><span class="submit">Create</span></form>');
global.window=dom.window;global.document=dom.window.document;global.navigator=dom.window.navigator;
const requests=[];
class Request {
 constructor(){requests.push(this);this.headers={};}
 open(method,url){this.method=method;this.url=url;}
 setRequestHeader(key,value){this.headers[key]=value;}
 send(data){this.data=JSON.parse(data);}
 finish(status,response){Object.assign(this,{readyState:4,status,response});this.onreadystatechange();}
}
global.XMLHttpRequest=Request;
let successes=0,closed=0,reloads=0;
const errors=[];
window.pages={Base:""};window.token="fixture-token";
window.notifications={customSuccess:()=>successes++,customError:(_,message)=>errors.push(message)};
window.lang={notif:()=>"",strings:()=>"",var:()=>"Account created."};
window.modals={addUser:{close:()=>closed++}};
const filename=path.resolve("ts/modules/accounts.ts");
const compiled=buildSync({entryPoints:[filename],bundle:true,platform:"node",format:"cjs",write:false});
const loaded=new Module(filename,module);loaded.filename=filename;loaded.paths=module.paths;loaded._compile(compiled.outputFiles[0].text,filename);
const accounts=Object.create(loaded.exports.accountsList.prototype);
Object.assign(accounts,{_addUserForm:document.querySelector("form"),_addUserName:document.getElementById("name"),_addUserEmail:document.getElementById("email"),_addUserPassword:document.getElementById("password"),_addUserProfile:document.querySelector("select"),reload:()=>reloads++});
accounts._addUserName.value="new-account";accounts._addUserEmail.value="new@example.invalid";accounts._addUserPassword.value="fixture-password";
const submit=()=>accounts._addUser({preventDefault(){}});
submit();submit();assert.equal(requests.length,1,"a pending creation must not be submitted twice");
assert.equal(requests[0].url,"/user");
requests[0].finish(502,{user:true,user_id:"existing-id",setup_complete:false,error:"Account created, but setup failed at Jellyfin policy."});
assert.equal(successes,0);assert.equal(closed,0);assert.match(errors.at(-1),/setup failed/);
assert.equal(document.querySelector(".submit").textContent,"Retry setup");
assert.equal(accounts._addUserName.readOnly,true);
submit();assert.equal(requests[1].url,"/users/existing-id/setup","retry must use the existing account endpoint");
requests[1].finish(200,{user:true,user_id:"existing-id",setup_complete:true,email:false,error:""});
assert.equal(successes,1);assert.equal(closed,1);assert.equal(accounts._addUserPassword.value,"");
assert.match(errors.at(-1),/not sent again/);assert.equal(reloads,2);
accounts._addUserPassword.value="fixture-password";
submit();requests.at(-1).finish(0,null);
assert.equal(accounts._addUserPending,false);assert.equal(closed,1);assert.match(errors.at(-1),/could not be confirmed/);
dom.window.close();
console.log("Account creation and setup retry UI checks passed.");
