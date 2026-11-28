export type RefreshResult = { kind: "ok"; token: string; refresh?: string } | { kind: "expired" } | { kind: "temporary" };
interface SessionHooks {
    token: () => string;
    refresh: () => Promise<RefreshResult>;
    install: (result: Extract<RefreshResult, {kind: "ok"}>) => void;
    expired: () => void;
    temporary: () => void;
}
const handled = new WeakSet<object>();
let current: BrowserSession | null = null;
export const sessionHandled = (req: XMLHttpRequest) => handled.has(req);
export function configureSession(session: BrowserSession): void {
    current?.close();
    current = session;
}

function requiresSession(req: XMLHttpRequest): boolean {
    if (req.status !== 401) return false;
    if (req.getResponseHeader?.("X-Omnifin-Session") === "required") return true;
    let body = req.response;
    if (typeof body === "string") { try { body = JSON.parse(body); } catch { return false; } }
    return body?.code === "AUTHENTICATION_REQUIRED";
}

export class BrowserSession {
    active = false;
    blocked = false;
    private epoch = 0;
    private refreshing: Promise<boolean> | null = null;
    constructor(private hooks: SessionHooks) {}
    activate(): void { this.epoch++; this.active = true; this.blocked = false; this.refreshing = null; }
    close(): void { this.epoch++; this.active = false; this.blocked = false; this.refreshing = null; }
    expire(): void {
        if (this.blocked) return;
        this.blocked = true;
        this.active = true;
        this.hooks.expired();
    }
    private renew(token: string, epoch: number): Promise<boolean> {
        if (epoch !== this.epoch || this.blocked || !this.active) return Promise.resolve(false);
        if (token !== this.hooks.token()) return Promise.resolve(true);
        if (this.refreshing) return this.refreshing;
        const flight = (async () => {
            try {
                const result = await this.hooks.refresh();
                if (epoch !== this.epoch || !this.active || this.blocked) return false;
                if (result.kind === "ok") { this.hooks.install(result); return true; }
                if (result.kind === "expired") this.expire();
                else this.hooks.temporary();
            } catch { if (epoch === this.epoch && this.active && !this.blocked) this.hooks.temporary(); }
            return false;
        })();
        this.refreshing = flight;
        void flight.finally(() => { if (this.refreshing === flight) this.refreshing = null; });
        return flight;
    }
    run(send: (done: (req: XMLHttpRequest) => void) => void, complete: (req: XMLHttpRequest) => void): void {
        if (this.blocked) {
            const req = { readyState: 4, status: 401, response: { error: "Unauthorized", code: "AUTHENTICATION_REQUIRED", hint: "Sign in to continue." } } as unknown as XMLHttpRequest;
            handled.add(req); void Promise.resolve().then(() => complete(req)); return;
        }
        const epoch = this.epoch;
        const attempt = (retried: boolean) => {
            const token = this.hooks.token();
            send((req) => {
                if (!requiresSession(req) || !this.active || epoch !== this.epoch) { complete(req); return; }
                handled.add(req);
                if (retried) { this.expire(); complete(req); return; }
                void this.renew(token, epoch).then(ok => { if (ok) attempt(true); else complete(req); });
            });
        };
        attempt(false);
    }
}

export function runAuthenticatedRequest(url: string, send: (done: (req: XMLHttpRequest) => void) => void, complete: (req: XMLHttpRequest) => void): void {
    // Login, renewal and logout own their credential handling. Ordinary business
    // 401 responses do not carry the middleware marker and must never be replayed.
    if (!current?.active || /\/(?:token\/(?:login|refresh)|logout)(?:[/?]|$)/.test(url)) { send(complete); return; }
    current.run(send, complete);
}
