// Run from the repository root: node scripts/backup-restore.test.cjs
const assert = require("node:assert/strict");
const path = require("node:path");
const Module = require("node:module");
const { buildSync } = require("esbuild");
const { JSDOM } = require("jsdom");

function loadTypescript(relativePath) {
    const filename = path.resolve(relativePath);
    const result = buildSync({ entryPoints: [filename], bundle: true, platform: "node", format: "cjs", write: false });
    const loaded = new Module(filename, module);
    loaded.filename = filename;
    loaded.paths = module.paths;
    loaded._compile(result.outputFiles[0].text, filename);
    return loaded.exports;
}

const dom = new JSDOM('<button id="restore"><i class="icon"></i></button><span class="heading"></span>');
global.window = dom.window;
global.document = dom.window.document;
const errors = [];
let closed = 0;
let shown = 0;
window.pages = { Base: "/omnifin" };
window.token = "fixture-token";
window.lang = { strings: (key) => key === "settingsRestarting" ? "Restarting" : "", notif: () => "" };
window.notifications = { customError: (key, message) => errors.push(message) };
window.modals = {
    backups: { close: () => closed++ },
    settingsRefresh: { modal: document, show: () => shown++ },
};
const { settingsList } = loadTypescript("ts/modules/settings.ts");
const settings = Object.create(settingsList.prototype);
const button = document.getElementById("restore");
let completion;
settings._restoreBackup((done) => { completion = done; }, button);
assert.equal(button.classList.contains("loader"), true);
assert.equal(closed, 0, "dialog must stay open while the server validates the backup");
completion({ readyState: 2, status: 200 });
assert.equal(shown, 0, "response headers alone must not announce a restart");
settings._restoreBackup(() => { throw new Error("duplicate request was sent"); }, button);
assert.equal(errors.length, 0);
completion({ readyState: 4, status: 400, response: { error: "Choose another backup." } });
assert.equal(closed, 0);
assert.equal(shown, 0);
assert.equal(button.classList.contains("loader"), false);
assert.match(errors.at(-1), /Choose another backup/);

for (const status of [0, 404, 409, 500]) {
    settings._restoreBackup((done) => done({ readyState: 4, status, response: null }), button);
    assert.equal(closed, 0);
    assert.equal(shown, 0);
    assert.equal(button.classList.contains("loader"), false, "failure must allow retry");
}
settings._restoreBackup(() => { throw new Error("request setup failed"); }, button);
assert.equal(button.classList.contains("loader"), false);
settings._restoreBackup((done) => { completion = done; }, button);
completion({ readyState: 4, status: 200 });
completion({ readyState: 4, status: 200 });
assert.equal(closed, 1);
assert.equal(shown, 1);
assert.equal(document.querySelector(".heading").textContent, "Restarting");
assert.equal(button.classList.contains("loader"), false);
assert.ok(button.querySelector("i.icon"), "loading must preserve the button icon");

const requests = [];
class FakeRequest {
    constructor() { this.headers = {}; requests.push(this); }
    open(method, url, async) { Object.assign(this, { method, url, async }); }
    setRequestHeader(key, value) { this.headers[key] = value; }
    send(data) { this.data = data; }
}
global.XMLHttpRequest = FakeRequest;
const { _upload, _post } = loadTypescript("ts/modules/common.ts");
for (const status of [0, 400, 500, 200]) {
    let callbacks = 0;
    const form = new window.FormData();
    _upload("/backups/restore", form, (req) => { callbacks++; assert.equal(req.status, status); });
    const req = requests.at(-1);
    assert.equal(callbacks, 0);
    assert.equal(req.url, "/omnifin/backups/restore");
    assert.equal(req.method, "POST");
    assert.equal(req.responseType, "json");
    assert.equal(req.headers.Authorization, "Bearer fixture-token");
    assert.equal(req.headers["Content-Type"], undefined, "the browser must add the multipart boundary");
    assert.equal(req.data, form);
    Object.assign(req, { readyState: 4, status });
    req.onloadend();
    assert.equal(callbacks, 1, "both failed and successful uploads need a completion callback");
}
let localCallback = 0;
_post("/backups/restore/fixture.bak", null, () => localCallback++, true, () => {});
const local = requests.at(-1);
Object.assign(local, { readyState: 4, status: 0 });
local.onreadystatechange();
assert.equal(localCallback, 1, "a disconnected local restore must reach its completion handler");
dom.window.close();
console.log("Backup restore UI and upload checks passed.");
