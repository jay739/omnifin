import {Modal} from "./modal.js";
import {toggleLoader, _post, unicodeB64Encode, formatApiFailure} from "./common.js";
import {BrowserSession, configureSession, RefreshResult} from "./session.js";

declare var window: GlobalWindow;

export class Login {
    loggedIn = false;
    private _modal: Modal;
    private _form: HTMLFormElement;
    private _url: string;
    private _endpoint: string;
    private _onLogin: (username: string, password: string) => void;
    private _logoutButton: HTMLElement = null;
    private _wall: HTMLElement;
    private _hasOpacityWall = false;
    private _session: BrowserSession;
    private _raised = false;
    private _originalZIndex = "";

    constructor(modal: Modal, endpoint: string, appearance: string) {
        this._endpoint = endpoint;
        this._url = window.pages.Base + endpoint;
        if (!this._url.endsWith("/")) this._url += "/";
        this._modal = modal;
        if (appearance === "opaque") {
            this._hasOpacityWall = true;
            this._wall = document.createElement("div");
            this._wall.classList.add("wall");
            this._modal.asElement().parentElement.appendChild(this._wall);
        }
        this._form = modal.asElement().querySelector(".form-login") as HTMLFormElement;
        this._form.onsubmit = (event: SubmitEvent) => {
            event.preventDefault();
            const button = (event.target as HTMLElement).querySelector(".submit") as HTMLSpanElement;
            const username = (document.getElementById("login-user") as HTMLInputElement).value;
            const password = (document.getElementById("login-password") as HTMLInputElement).value;
            if (!username || !password) { window.notifications.customError("loginError", window.lang.notif("errorLoginBlank")); return; }
            toggleLoader(button);
            this.login(username, password, () => toggleLoader(button));
        };
        const retry = document.createElement("button");
        retry.type = "button";
        retry.className = "button ~neutral @low full-width";
        retry.textContent = "Retry saved session";
        retry.onclick = () => { toggleLoader(retry); this.login("", "", () => toggleLoader(retry)); };
        this._form.appendChild(retry);
        this._session = new BrowserSession({
            token: () => window.token,
            refresh: async () => (await this._requestTokens(true)).result,
            install: (result) => this._install(result),
            expired: () => {
                window.token = "";
                this.loggedIn = false;
                this._removeRefresh();
                this._showLogin();
                document.dispatchEvent(new CustomEvent("session-expired", {detail: {endpoint: this._endpoint}}));
            },
            temporary: () => window.notifications.customError("sessionRenewal", "The session could not be renewed. Check the server connection and retry; your draft is still here."),
        });
        configureSession(this._session);
        this._modal.onclose = () => { if (!this.loggedIn) this._showLogin(); };
    }

    private _showLogin(): void {
        const element = this._modal.asElement();
        if (!this._raised) this._originalZIndex = element.style.zIndex;
        this._raised = true;
        const highest = Math.max(100, ...Array.from(document.querySelectorAll<HTMLElement>(".modal")).map(modal => parseInt(window.getComputedStyle(modal).zIndex, 10) || 0));
        element.style.zIndex = String(highest + 1);
        this._modal.show();
    }
    private _hideLogin(): void {
        this._modal.close();
        if (this._raised) this._modal.asElement().style.zIndex = this._originalZIndex;
        this._raised = false;
    }

    private _refreshKey = () => "omnifin_refresh_" + this._endpoint;
    private _readRefresh(): string | null { try { return localStorage.getItem(this._refreshKey()); } catch { return null; } }
    private _removeRefresh(): void { try { localStorage.removeItem(this._refreshKey()); } catch {} }
    private _install(result: Extract<RefreshResult, {kind: "ok"}>): void {
        window.token = result.token;
        if (result.refresh) { try { localStorage.setItem(this._refreshKey(), result.refresh); } catch {} }
    }
    private _requestTokens(refresh: boolean, username = "", password = ""): Promise<{req: XMLHttpRequest; result: RefreshResult}> {
        return new Promise(resolve => {
            const req = new XMLHttpRequest();
            let settled = false;
            const finish = () => {
                if (req.readyState !== 4 || settled) return;
                settled = true;
                const data = req.response;
                let result: RefreshResult = {kind: "temporary"};
                if (req.status === 200 && typeof data?.token === "string" && data.token) result = {kind: "ok", token: data.token, refresh: data.refresh};
                else if (req.status === 401 || req.status === 403) result = {kind: "expired"};
                resolve({req, result});
            };
            req.open("GET", this._url + (refresh ? "token/refresh" : "token/login"), true);
            req.responseType = "json";
            req.timeout = 15000;
            if (!refresh) req.setRequestHeader("Authorization", "Basic " + unicodeB64Encode(username + ":" + password));
            else { const stored = this._readRefresh(); if (stored) req.setRequestHeader("Authorization", "Bearer " + stored); }
            req.onreadystatechange = finish;
            req.onloadend = finish;
            req.send();
        });
    }
    bindLogout = (button: HTMLElement) => {
        this._logoutButton = button;
        button.classList.add("ui-hidden");
        button.onclick = () => _post(this._endpoint + "logout", null, (req) => {
            if (req.readyState === 4 && req.status === 200) {
                this._session.close(); window.token = ""; this._removeRefresh(); location.reload();
            }
        }, false, () => {});
    };
    get onLogin() { return this._onLogin; }
    set onLogin(callback: (username: string, password: string) => void) { this._onLogin = callback; }
    login = (username: string, password: string, run?: (state?: number) => void): void => {
        const refresh = username === "" && password === "";
        void this._requestTokens(refresh, username, password).then(({req, result}) => {
            if (result.kind === "ok") {
                this._install(result);
                this._session.activate();
                this.loggedIn = true;
                this._onLogin?.(username, password);
                if (this._hasOpacityWall) this._wall.remove();
                this._hideLogin();
                this._logoutButton?.classList.remove("ui-hidden");
            } else if (refresh) {
                if (result.kind === "expired") this._session.expire();
                else this._showLogin();
            } else {
                window.notifications.customError("loginError", formatApiFailure(req, "Sign-in could not be completed. Check the server connection and retry."));
            }
            run?.(req.status);
        }).catch(() => {
            window.notifications.customError("loginError", "Sign-in could not be completed. Check the server connection and retry.");
            run?.(0);
        });
    };
}
