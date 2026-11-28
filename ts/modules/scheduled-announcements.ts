import {_req, formatApiFailure} from "./common.js";
import {Modal} from "./modal.js";

declare var window: GlobalWindow;
interface ScheduledAnnouncement {
    id: string; users: string[]; subject: string; message: string; send_at: string;
    sent: boolean; sent_at?: string; created_at: string; attempts: number;
    next_attempt?: string; failed: boolean; last_error?: string; delivery_states?: Record<string,string>;
}

export class ScheduledAnnouncements {
    private root: HTMLElement;
    private rows: HTMLElement;
    private status: HTMLElement;
    private refresh: HTMLButtonElement;
    private filter: HTMLSelectElement;
    private jobs: ScheduledAnnouncement[] = [];
    private generation = 0;
    private pending: string | null = null;
    private open = false;
    constructor(private button: HTMLButtonElement, private modal: Modal, private nameFor: (id: string) => string = id => id) {
        this.root = modal.asElement();
        this.rows = this.root.querySelector("tbody");
        this.status = this.root.querySelector("[data-scheduled-status]");
        this.refresh = this.root.querySelector("[data-scheduled-refresh]");
        this.filter = this.root.querySelector("[data-scheduled-filter]");
        const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
        this.root.querySelector("[data-scheduled-timezone]").textContent = "Times shown in " + zone;
        button.onclick = () => { this.open = true; modal.show(); this.load(); };
        this.root.querySelector("[data-scheduled-close]").addEventListener("click", () => modal.close());
        modal.onclose = () => { this.open = false; };
        this.refresh.onclick = () => this.load();
        this.filter.onchange = () => this.render();
        document.addEventListener("announcement-sent", () => { if (this.open) this.load(); });
    }
    enable(): void { this.button.disabled = false; }
    private date(raw?: string): string {
        if (!raw || raw.startsWith("0001-")) return "";
        const value = new Date(raw);
        if (isNaN(value.getTime())) return "Unavailable";
        return value.toLocaleString(window.language || undefined, {year:"numeric",month:"short",day:"numeric",hour:"2-digit",minute:"2-digit",timeZoneName:"short"});
    }
    private message(text: string, error = false): void {
        this.status.textContent = text;
        this.status.setAttribute("role", error ? "alert" : "status");
    }
    load(): void {
        if (this.pending) return;
        const generation = ++this.generation;
        this.refresh.disabled = true;
        this.message("Loading scheduled announcements...");
        _req("GET", "/users/announce/scheduled", null, (req) => {
            if (req.readyState !== 4 || generation !== this.generation) return;
            this.refresh.disabled = false;
            if (req.status !== 200 || !Array.isArray(req.response?.scheduled)) {
                this.message(formatApiFailure(req, "Could not load scheduled announcements. Check the connection and refresh."), true);
                return;
            }
            this.jobs = req.response.scheduled.filter(job => job && typeof job.id === "string");
            this.render();
            this.message(this.jobs.length + " scheduled announcement" + (this.jobs.length === 1 ? "" : "s"));
        }, true, () => {});
    }
    private state(job: ScheduledAnnouncement): string {
        if (job.sent) return "Sent";
        if (job.failed) return Object.values(job.delivery_states || {}).includes("sending") ? "Needs review" : "Failed";
        if (Object.values(job.delivery_states || {}).includes("sending")) return "Delivery in progress";
        return job.attempts > 0 ? "Retry scheduled" : "Queued";
    }
    private render(): void {
        this.rows.replaceChildren();
        const filter = this.filter.value;
        const shown = this.jobs.filter(job => filter === "all" || (filter === "pending" ? !job.sent && !job.failed : job.sent || job.failed));
        shown.sort((a,b) => Number(a.sent || a.failed) - Number(b.sent || b.failed) || (a.sent || a.failed ? Date.parse(b.created_at) - Date.parse(a.created_at) : Date.parse(a.send_at) - Date.parse(b.send_at)));
        for (const job of shown) {
            const row = document.createElement("tr"); row.dataset.jobId = job.id;
            const cell = (text = "") => { const td = document.createElement("td"); td.className = "align-top p-3"; td.textContent = text; row.appendChild(td); return td; };
            const subject = cell(); const title = document.createElement("strong"); title.textContent = job.subject; subject.appendChild(title);
            const preview = document.createElement("details"); const summary = document.createElement("summary"); summary.textContent = "Message preview";
            const body = document.createElement("pre"); body.className = "whitespace-pre-wrap text-sm"; body.textContent = job.message;
            preview.append(summary,body); subject.appendChild(preview);
            cell(this.date(job.send_at));
            const count = Array.isArray(job.users) ? job.users.length : 0;
            cell(count + (count === 1 ? " recipient" : " recipients"));
            const delivery = cell(); const state = document.createElement("strong"); state.textContent = this.state(job); delivery.appendChild(state);
            const attempts = document.createElement("p"); attempts.textContent = (job.attempts || 0) + " delivery attempt" + (job.attempts === 1 ? "" : "s"); delivery.appendChild(attempts);
            if (job.sent && this.date(job.sent_at)) { const time = document.createElement("p"); time.textContent = "Sent " + this.date(job.sent_at); delivery.appendChild(time); }
            if (!job.failed && !job.sent && this.date(job.next_attempt)) { const retry = document.createElement("p"); retry.textContent = "Next retry " + this.date(job.next_attempt); delivery.appendChild(retry); }
            if (job.last_error) { const error = document.createElement("p"); error.className = "text-sm"; error.textContent = job.last_error; delivery.appendChild(error); }
            const outcomes = Object.entries(job.delivery_states || {});
            if (outcomes.length) {
                const detail = document.createElement("details"); const heading = document.createElement("summary"); heading.textContent = "Recipient outcomes"; detail.appendChild(heading);
                const list = document.createElement("ul");
                for (const [key,value] of outcomes) {
                    const split = key.lastIndexOf(":"); const id = split < 0 ? key : key.slice(0,split); const channel = split < 0 ? "message" : key.slice(split+1);
                    const item = document.createElement("li"); item.textContent = `${this.nameFor(id)} (${channel}): ${value === "sent" ? "delivered" : value === "failed" ? "delivery failed" : "outcome not confirmed"}`; list.appendChild(item);
                }
                detail.appendChild(list); delivery.appendChild(detail);
            }
            const actions = cell();
            if (!job.sent && !job.failed) {
                const cancel = this.action(this.pending === job.id ? "Canceling..." : job.attempts > 0 ? "Cancel remaining delivery" : "Cancel announcement", () => this.confirm(job, actions));
                cancel.disabled = Boolean(this.pending); actions.appendChild(cancel);
            }
            this.rows.appendChild(row);
        }
        if (!shown.length) { const row = document.createElement("tr"); const cell = document.createElement("td"); cell.colSpan = 5; cell.className = "p-6 text-center"; cell.textContent = filter === "pending" ? "No announcements are waiting to be sent." : filter === "history" ? "No delivery history yet." : "No scheduled announcements yet. Choose a send time in the announcement composer to add one."; row.appendChild(cell); this.rows.appendChild(row); }
    }
    private action(text: string, click: () => void): HTMLButtonElement {
        const button = document.createElement("button"); button.type = "button"; button.className = "button ~neutral @low"; button.textContent = text; button.onclick = click; return button;
    }
    private confirm(job: ScheduledAnnouncement, cell: HTMLElement): void {
        if (this.pending) return;
        cell.replaceChildren();
        const prompt = document.createElement("p"); prompt.textContent = "Stop remaining delivery? Messages already delivered cannot be recalled.";
        const keep = this.action("Keep scheduled", () => this.render());
        const confirm = this.action("Confirm cancellation", () => this.cancel(job.id)); confirm.className = "button ~critical @low";
        cell.append(prompt, keep, confirm); keep.focus();
    }
    private cancel(id: string): void {
        if (this.pending) return;
        this.pending = id; this.generation++; this.refresh.disabled = true; this.render();
        _req("DELETE", "/users/announce/scheduled/" + encodeURIComponent(id), null, (req) => {
            if (req.readyState !== 4) return;
            this.pending = null; this.refresh.disabled = false;
            if (req.status !== 200 && req.status !== 204) {
                this.render(); this.message(formatApiFailure(req, "Cancellation could not be confirmed. Refresh the queue before trying again."), true); return;
            }
            this.jobs = this.jobs.filter(job => job.id !== id); this.render(); this.load();
        }, true, () => {});
    }
}
