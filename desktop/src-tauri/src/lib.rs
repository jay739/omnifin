use serde::{Deserialize, Serialize};
use std::fs;
use std::net::{TcpStream, ToSocketAddrs};
use std::path::PathBuf;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Mutex;
use std::time::Duration;
use tauri::menu::{MenuBuilder, MenuItemBuilder, SubmenuBuilder};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Manager, State, Url, WebviewUrl, WebviewWindow, WebviewWindowBuilder};
use tauri_plugin_opener::OpenerExt;

// URL of the bundled setup page (the local index.html shipped inside the
// app). Tauri serves bundled assets under a custom scheme that differs by
// platform.
const fn setup_page_url() -> &'static str {
    if cfg!(target_os = "windows") {
        "http://tauri.localhost/index.html"
    } else {
        "tauri://localhost/index.html"
    }
}

// URL of the bundled branded error page, shown when the configured server is
// unreachable instead of the webview engine's default "can't connect" screen.
const fn error_page_url() -> &'static str {
    if cfg!(target_os = "windows") {
        "http://tauri.localhost/error.html"
    } else {
        "tauri://localhost/error.html"
    }
}

// ──────────────────────────────────────────────────────────────────────────
//  Persistent app config
// ──────────────────────────────────────────────────────────────────────────

#[derive(Serialize, Deserialize, Default, Clone)]
struct AppConfig {
    server_url: Option<String>,
    #[serde(default)]
    recent_servers: Vec<String>,
    #[serde(default = "default_zoom")]
    zoom_level: f64,
}

fn default_zoom() -> f64 {
    1.0
}

fn config_file(app: &AppHandle) -> PathBuf {
    let dir = app
        .path()
        .app_config_dir()
        .expect("could not resolve app config dir");
    let _ = fs::create_dir_all(&dir);
    dir.join("config.json")
}

fn load_config(app: &AppHandle) -> AppConfig {
    fs::read_to_string(config_file(app))
        .ok()
        .and_then(|s| serde_json::from_str::<AppConfig>(&s).ok())
        .unwrap_or_default()
}

fn save_config(app: &AppHandle, cfg: &AppConfig) -> Result<(), String> {
    let json = serde_json::to_string_pretty(cfg).map_err(|e| e.to_string())?;
    fs::write(config_file(app), json).map_err(|e| e.to_string())
}

// Push the given URL to the front of recent_servers, dedup, cap to 5.
fn remember_recent(cfg: &mut AppConfig, url: &str) {
    cfg.recent_servers.retain(|u| u != url);
    cfg.recent_servers.insert(0, url.to_string());
    cfg.recent_servers.truncate(5);
}

// ──────────────────────────────────────────────────────────────────────────
//  Server reachability + connect-error state
// ──────────────────────────────────────────────────────────────────────────

// Details of the last failed connection, surfaced to error.html via the
// get_connect_error command. Kept in memory only (transient, per-run).
#[derive(Serialize, Clone)]
struct ConnectErrorInfo {
    url: String,
    reason: String,
}

#[derive(Default)]
struct ConnectErrorState(Mutex<Option<ConnectErrorInfo>>);

fn store_connect_error(app: &AppHandle, url: String, reason: String) {
    if let Some(state) = app.try_state::<ConnectErrorState>() {
        if let Ok(mut guard) = state.0.lock() {
            *guard = Some(ConnectErrorInfo { url, reason });
        }
    }
}

// Pre-flight reachability check: a plain TCP connect to host:port with a short
// timeout. Catches exactly the "can't connect" cases the browser error covers
// (DNS failure, connection refused, host down). std-only, no HTTP client dep.
fn server_reachable(url: &Url) -> Result<(), String> {
    let host = url.host_str().ok_or("URL has no host")?;
    let port = url
        .port_or_known_default()
        .ok_or("URL has no port and no known default")?;
    let addrs = (host, port)
        .to_socket_addrs()
        .map_err(|e| format!("could not resolve {}: {}", host, e))?;
    let timeout = Duration::from_secs(3);
    let mut last_err = String::from("no addresses resolved");
    for addr in addrs {
        match TcpStream::connect_timeout(&addr, timeout) {
            Ok(_) => return Ok(()),
            Err(e) => last_err = e.to_string(),
        }
    }
    Err(last_err)
}

// Probe the server, then either navigate the window to it or to the branded
// local error page (storing the failure for error.html to read). Used by the
// recent-server menu pick and the Retry action.
fn connect_or_show_error(app: &AppHandle, win: &WebviewWindow, parsed: Url) {
    match server_reachable(&parsed) {
        Ok(()) => {
            let host = parsed.host_str().unwrap_or("").to_string();
            let _ = win.set_title(&format!("Omnifin — {}", host));
            let _ = win.navigate(parsed);
        }
        Err(reason) => {
            store_connect_error(app, parsed.to_string(), reason);
            let _ = win.set_title("Omnifin — Can't connect");
            if let Ok(err_url) = Url::parse(error_page_url()) {
                let _ = win.navigate(err_url);
            }
        }
    }
}

// ──────────────────────────────────────────────────────────────────────────
//  Window management
// ──────────────────────────────────────────────────────────────────────────

// Counter so File → New Window can create unique window labels.
static WINDOW_COUNTER: AtomicU32 = AtomicU32::new(1);

fn next_window_label() -> String {
    let n = WINDOW_COUNTER.fetch_add(1, Ordering::SeqCst);
    if n == 1 {
        "main".to_string()
    } else {
        format!("main-{}", n)
    }
}

// Injected into every page (local and remote) before it loads. A heartbeat
// that detects the wall-clock jump caused by the machine sleeping, then reloads
// so the server page isn't left stale (expired session, dropped sockets) after
// the system wakes. Cross-platform and dependency-free; Tauri exposes no
// portable sleep/wake event, and a generous 30s gap avoids false reloads from
// ordinary timer throttling.
const WAKE_REFRESH_JS: &str = r#"
(function () {
  if (window.__omnifin_wake_installed) return;
  window.__omnifin_wake_installed = true;
  var INTERVAL = 10000; // tick every 10s
  var GAP = 30000;      // a jump larger than this means the machine slept
  var last = Date.now();
  setInterval(function () {
    var now = Date.now();
    if (now - last > GAP) location.reload();
    last = now;
  }, INTERVAL);
})();
"#;

// Injected before every page loads. Shows a branded "Loading Omnifin…" overlay
// while a remote server page is fetching, removing the white flash between
// Connect/launch/reload and the rendered page. Scoped to remote pages only —
// the local setup and error pages (served from tauri.localhost) load instantly
// and skip it. A 15s safety timeout guarantees the overlay never sticks.
const LOADING_OVERLAY_JS: &str = r#"
(function () {
  if (location.protocol === "tauri:" || location.host === "tauri.localhost") return;
  if (window.__omnifin_loader_installed) return;
  window.__omnifin_loader_installed = true;
  function build() {
    if (document.getElementById("__omnifin_loader")) return;
    var root = document.body || document.documentElement;
    if (!root) return;
    if (!document.getElementById("__omnifin_loader_style")) {
      var style = document.createElement("style");
      style.id = "__omnifin_loader_style";
      style.textContent = "@keyframes __omnifin_spin{to{transform:rotate(360deg)}}";
      (document.head || root).appendChild(style);
    }
    var o = document.createElement("div");
    o.id = "__omnifin_loader";
    o.style.cssText = "position:fixed;inset:0;z-index:2147483647;background:#08080a;color:#f9fafb;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:18px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;transition:opacity .25s ease;";
    o.innerHTML = "<div style=\"width:40px;height:40px;border:3px solid rgba(99,102,241,.25);border-top-color:#6366f1;border-radius:50%;animation:__omnifin_spin .8s linear infinite;\"></div><div style=\"font-size:14px;color:#9ca3af;\">Loading Omnifin…</div>";
    root.appendChild(o);
  }
  function remove() {
    var o = document.getElementById("__omnifin_loader");
    if (!o) return;
    o.style.opacity = "0";
    setTimeout(function () { o.remove(); }, 260);
  }
  build();
  document.addEventListener("readystatechange", build);
  window.addEventListener("DOMContentLoaded", build);
  window.addEventListener("load", remove);
  setTimeout(remove, 15000);
})();
"#;

// Build a fresh window pointed at the given target. Used on app launch,
// "Change Server URL…", and "New Window".
fn open_window_for(
    app: &AppHandle,
    label: String,
    target: WebviewUrl,
    title: String,
) -> tauri::Result<WebviewWindow> {
    WebviewWindowBuilder::new(app, &label, target)
        .title(title)
        .inner_size(1280.0, 800.0)
        .min_inner_size(720.0, 480.0)
        .center()
        .initialization_script(WAKE_REFRESH_JS)
        .initialization_script(LOADING_OVERLAY_JS)
        .build()
}

// Target + title for the bundled setup (Connect) page — the default when no
// server is saved or a fresh window is requested.
fn setup_target() -> (WebviewUrl, String) {
    (
        WebviewUrl::App("index.html".into()),
        "Omnifin — Connect".to_string(),
    )
}

// Open the main launch window. If a server is saved, probe it first: navigate
// to it when reachable, else open straight to the branded error page so the
// user never sees the webview's default "can't connect" screen.
fn open_main_window(app: &AppHandle) -> tauri::Result<()> {
    let cfg = load_config(app);
    let (target, title) = match cfg.server_url.as_deref().and_then(|s| Url::parse(s).ok()) {
        Some(url) => match server_reachable(&url) {
            Ok(()) => (
                WebviewUrl::External(url.clone()),
                format!("Omnifin — {}", url.host_str().unwrap_or("")),
            ),
            Err(reason) => {
                store_connect_error(app, url.to_string(), reason);
                (
                    WebviewUrl::App("error.html".into()),
                    "Omnifin — Can't connect".to_string(),
                )
            }
        },
        None => setup_target(),
    };
    open_window_for(app, "main".to_string(), target, title)?;
    Ok(())
}

// Navigate the existing main window to the bundled setup HTML, leaving the
// saved URL untouched so the setup form pre-fills with it. We navigate
// in-place instead of close+recreate to avoid two pitfalls:
//   1. WebviewWindowBuilder::new can't take a label that still exists, so
//      a close-then-recreate races and sometimes errors silently.
//   2. Tauri exits the app when the last window is destroyed, so if the
//      close lands before the new window is built, the app dies.
fn open_setup_window(app: &AppHandle) -> tauri::Result<()> {
    if let Some(win) = app.get_webview_window("main") {
        if let Ok(url) = Url::parse(setup_page_url()) {
            let _ = win.set_title("Omnifin — Change Server");
            return win.navigate(url);
        }
    }
    // Only fall through to creating a fresh window if none currently exists
    // (first launch with no saved URL, or after every window was closed).
    let (target, title) = setup_target();
    open_window_for(app, "main".to_string(), target, title)?;
    Ok(())
}

// Open a brand-new additional window so the user can manage a second server
// alongside the current one.
fn open_new_window(app: &AppHandle) -> tauri::Result<()> {
    let label = next_window_label();
    let (target, title) = setup_target();
    open_window_for(app, label, target, title)?;
    Ok(())
}

// Show + focus an existing main window (for tray click / single-instance).
fn show_main(app: &AppHandle) {
    if let Some(win) = app.get_webview_window("main") {
        let _ = win.show();
        let _ = win.unminimize();
        let _ = win.set_focus();
    } else {
        let _ = open_main_window(app);
    }
}

// ──────────────────────────────────────────────────────────────────────────
//  IPC commands (called from the setup HTML)
// ──────────────────────────────────────────────────────────────────────────

#[tauri::command]
fn set_server_url(app: AppHandle, url: String) -> Result<(), String> {
    let trimmed = url.trim().trim_end_matches('/').to_string();
    if trimmed.is_empty() {
        return Err("URL cannot be empty".into());
    }
    let parsed = Url::parse(&trimmed).map_err(|e| format!("invalid URL: {}", e))?;
    if !matches!(parsed.scheme(), "http" | "https") {
        return Err(format!(
            "only http and https URLs are supported (got {})",
            parsed.scheme()
        ));
    }
    // Probe before saving: returning Err here lets the setup form show its
    // inline error and keep the user's typed URL, and avoids persisting an
    // unreachable server as the default.
    server_reachable(&parsed).map_err(|e| {
        format!(
            "Couldn't reach {}: {}",
            parsed.host_str().unwrap_or(trimmed.as_str()),
            e
        )
    })?;

    let mut cfg = load_config(&app);
    cfg.server_url = Some(trimmed.clone());
    remember_recent(&mut cfg, &trimmed);
    save_config(&app, &cfg)?;

    // Navigate the focused webview to the saved URL and update the window
    // title so the user always sees which server they're on.
    if let Some(win) = app
        .webview_windows()
        .into_iter()
        .find_map(|(_, w)| if w.is_focused().unwrap_or(false) { Some(w) } else { None })
        .or_else(|| app.get_webview_window("main"))
    {
        let host = parsed.host_str().unwrap_or("").to_string();
        let _ = win.set_title(&format!("Omnifin — {}", host));
        win.navigate(parsed).map_err(|e| e.to_string())?;
    }
    Ok(())
}

#[tauri::command]
fn get_server_url(app: AppHandle) -> Option<String> {
    load_config(&app).server_url
}

#[tauri::command]
fn get_recent_servers(app: AppHandle) -> Vec<String> {
    load_config(&app).recent_servers
}

#[tauri::command]
fn clear_server_url(app: AppHandle) -> Result<(), String> {
    let mut cfg = load_config(&app);
    cfg.server_url = None;
    save_config(&app, &cfg)?;
    open_setup_window(&app).map_err(|e| e.to_string())
}

// Read the last connection failure so the error page can show which server
// failed and why. Returns None if there is no recorded error.
#[tauri::command]
fn get_connect_error(state: State<'_, ConnectErrorState>) -> Option<ConnectErrorInfo> {
    state.0.lock().ok().and_then(|guard| guard.clone())
}

// Retry connecting to the saved server (called by the error page's Retry
// button). Re-probes and either loads the server or re-shows the error page.
#[tauri::command]
fn retry_connect(app: AppHandle) -> Result<(), String> {
    let saved = load_config(&app)
        .server_url
        .ok_or("no server configured to retry")?;
    let parsed = Url::parse(&saved).map_err(|e| format!("invalid saved URL: {}", e))?;
    let win = focused_window(&app).ok_or("no window to navigate")?;
    connect_or_show_error(&app, &win, parsed);
    Ok(())
}

// Open the setup form (keeps the saved URL so the form pre-fills) — used by
// the error page's "Change server" action.
#[tauri::command]
fn open_setup(app: AppHandle) -> Result<(), String> {
    open_setup_window(&app).map_err(|e| e.to_string())
}

// Open the current/failed server URL in the user's default browser — used by
// the error page so they can sanity-check the server outside the app.
#[tauri::command]
fn open_current_in_browser(app: AppHandle) {
    open_in_browser(&app);
}

// ──────────────────────────────────────────────────────────────────────────
//  Menu actions
// ──────────────────────────────────────────────────────────────────────────

const ZOOM_STEP: f64 = 0.1;
const ZOOM_MIN: f64 = 0.5;
const ZOOM_MAX: f64 = 2.5;

fn focused_window(app: &AppHandle) -> Option<WebviewWindow> {
    app.webview_windows()
        .into_iter()
        .find_map(|(_, w)| if w.is_focused().unwrap_or(false) { Some(w) } else { None })
        .or_else(|| app.get_webview_window("main"))
}

fn apply_zoom(app: &AppHandle, delta: Option<f64>) {
    let mut cfg = load_config(app);
    cfg.zoom_level = match delta {
        Some(d) => (cfg.zoom_level + d).clamp(ZOOM_MIN, ZOOM_MAX),
        None => 1.0,
    };
    let _ = save_config(app, &cfg);
    if let Some(win) = focused_window(app) {
        let _ = win.eval(&format!("document.body.style.zoom = '{}';", cfg.zoom_level));
    }
}

fn reload_focused(app: &AppHandle) {
    if let Some(win) = focused_window(app) {
        let _ = win.eval("location.reload();");
    }
}

fn open_in_browser(app: &AppHandle) {
    // Prefer the URL of the focused window (handles the multi-window case
    // where each window may be on a different server); fall back to the
    // saved default if for any reason that read fails.
    let target = focused_window(app)
        .and_then(|w| w.url().ok())
        .map(|u| u.to_string())
        .or_else(|| load_config(app).server_url);
    if let Some(url) = target {
        let _ = app.opener().open_url(url, None::<&str>);
    }
}

// Cmd+F: inject a small in-page search overlay into the focused window.
// Re-injects each press so it works in both setup HTML and external pages.
fn open_find_in_page(app: &AppHandle) {
    let script = r#"
(function() {
    var existing = document.getElementById('__omnifin_find_bar');
    if (existing) { existing.querySelector('input').focus(); return; }
    var bar = document.createElement('div');
    bar.id = '__omnifin_find_bar';
    bar.style.cssText = 'position:fixed;top:8px;right:8px;z-index:2147483647;background:#12121a;color:#f9fafb;border:1px solid #6366f1;border-radius:8px;padding:6px 8px;display:flex;gap:6px;align-items:center;font-family:-apple-system,sans-serif;font-size:13px;box-shadow:0 8px 24px rgba(0,0,0,0.4);';
    bar.innerHTML = '<input id="__omnifin_find_input" placeholder="Find on page" style="background:#1e1e28;color:#f9fafb;border:1px solid rgba(255,255,255,0.1);border-radius:4px;padding:4px 8px;width:220px;outline:none;font:inherit;"/><span id="__omnifin_find_count" style="opacity:0.6;min-width:40px;">0/0</span><button id="__omnifin_find_close" style="background:transparent;color:#9ca3af;border:0;cursor:pointer;font-size:16px;">×</button>';
    document.body.appendChild(bar);
    var input = bar.querySelector('input');
    var counter = bar.querySelector('#__omnifin_find_count');
    var close = bar.querySelector('#__omnifin_find_close');
    var marks = [];
    var idx = -1;
    function clear() {
        marks.forEach(function(m) { var p = m.parentNode; if (p) { p.replaceChild(document.createTextNode(m.textContent), m); p.normalize(); } });
        marks = []; idx = -1;
    }
    function highlight(q) {
        clear();
        if (!q) { counter.textContent = '0/0'; return; }
        var rx = new RegExp(q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'gi');
        var walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT, {
            acceptNode: function(n) { return (n.parentNode && n.parentNode.id !== '__omnifin_find_bar' && n.parentNode.tagName !== 'SCRIPT' && n.parentNode.tagName !== 'STYLE' && rx.test(n.textContent)) ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT; }
        });
        var nodes = []; var n;
        while ((n = walker.nextNode())) nodes.push(n);
        nodes.forEach(function(node) {
            var text = node.textContent;
            var frag = document.createDocumentFragment();
            var last = 0; var m;
            rx.lastIndex = 0;
            while ((m = rx.exec(text)) !== null) {
                if (m.index > last) frag.appendChild(document.createTextNode(text.slice(last, m.index)));
                var span = document.createElement('mark');
                span.textContent = m[0];
                span.style.background = '#fbbf24';
                span.style.color = '#000';
                frag.appendChild(span);
                marks.push(span);
                last = m.index + m[0].length;
            }
            if (last < text.length) frag.appendChild(document.createTextNode(text.slice(last)));
            node.parentNode.replaceChild(frag, node);
        });
        if (marks.length) { idx = 0; marks[0].scrollIntoView({block: 'center'}); marks[0].style.outline = '2px solid #f59e0b'; }
        counter.textContent = (marks.length ? '1' : '0') + '/' + marks.length;
    }
    input.addEventListener('input', function() { highlight(input.value); });
    input.addEventListener('keydown', function(ev) {
        if (ev.key === 'Escape') { clear(); bar.remove(); }
        else if (ev.key === 'Enter') {
            if (marks.length === 0) return;
            marks[idx].style.outline = '';
            idx = (idx + (ev.shiftKey ? -1 : 1) + marks.length) % marks.length;
            marks[idx].scrollIntoView({block: 'center'});
            marks[idx].style.outline = '2px solid #f59e0b';
            counter.textContent = (idx + 1) + '/' + marks.length;
        }
    });
    close.addEventListener('click', function() { clear(); bar.remove(); });
    input.focus();
})();
"#;
    if let Some(win) = focused_window(app) {
        let _ = win.eval(script);
    }
}

// ──────────────────────────────────────────────────────────────────────────
//  Menu construction
// ──────────────────────────────────────────────────────────────────────────

fn build_menu(app: &AppHandle) -> tauri::Result<()> {
    let cfg = load_config(app);

    let change_server = MenuItemBuilder::with_id("change_server", "Change Server URL…")
        .accelerator("CmdOrCtrl+,")
        .build(app)?;
    let new_window = MenuItemBuilder::with_id("new_window", "New Window")
        .accelerator("CmdOrCtrl+N")
        .build(app)?;
    let reload = MenuItemBuilder::with_id("reload", "Reload")
        .accelerator("CmdOrCtrl+R")
        .build(app)?;
    let find = MenuItemBuilder::with_id("find", "Find in Page…")
        .accelerator("CmdOrCtrl+F")
        .build(app)?;
    let open_browser = MenuItemBuilder::with_id("open_browser", "Open in Default Browser")
        .accelerator("CmdOrCtrl+Shift+O")
        .build(app)?;
    let zoom_in = MenuItemBuilder::with_id("zoom_in", "Zoom In")
        .accelerator("CmdOrCtrl+Plus")
        .build(app)?;
    let zoom_out = MenuItemBuilder::with_id("zoom_out", "Zoom Out")
        .accelerator("CmdOrCtrl+-")
        .build(app)?;
    let zoom_reset = MenuItemBuilder::with_id("zoom_reset", "Actual Size")
        .accelerator("CmdOrCtrl+0")
        .build(app)?;
    let quit = MenuItemBuilder::with_id("quit", "Quit Omnifin")
        .accelerator("CmdOrCtrl+Q")
        .build(app)?;

    // Build a Recent submenu from saved history (top 5).
    let mut recent_builder = SubmenuBuilder::new(app, "Recent Servers");
    if cfg.recent_servers.is_empty() {
        let placeholder =
            MenuItemBuilder::with_id("recent_empty", "(no recent servers yet)")
                .enabled(false)
                .build(app)?;
        recent_builder = recent_builder.item(&placeholder);
    } else {
        for (i, url) in cfg.recent_servers.iter().enumerate() {
            let label = if url.len() > 60 {
                format!("{}…", &url[..58])
            } else {
                url.clone()
            };
            let item = MenuItemBuilder::with_id(format!("recent_{}", i), label).build(app)?;
            recent_builder = recent_builder.item(&item);
        }
    }
    let recent_submenu = recent_builder.build()?;

    let app_menu = SubmenuBuilder::new(app, "Omnifin")
        .item(&change_server)
        .item(&recent_submenu)
        .separator()
        .item(&new_window)
        .separator()
        .item(&quit)
        .build()?;

    let view_menu = SubmenuBuilder::new(app, "View")
        .item(&reload)
        .item(&find)
        .item(&open_browser)
        .separator()
        .item(&zoom_in)
        .item(&zoom_out)
        .item(&zoom_reset)
        .build()?;

    let edit_menu = SubmenuBuilder::new(app, "Edit")
        .undo()
        .redo()
        .separator()
        .cut()
        .copy()
        .paste()
        .select_all()
        .build()?;

    let menu = MenuBuilder::new(app)
        .items(&[&app_menu, &edit_menu, &view_menu])
        .build()?;
    app.set_menu(menu)?;
    Ok(())
}

fn handle_menu_event(app: &AppHandle, id: &str) {
    match id {
        "change_server" => {
            let _ = open_setup_window(app);
        }
        "new_window" => {
            let _ = open_new_window(app);
        }
        "reload" => reload_focused(app),
        "find" => open_find_in_page(app),
        "open_browser" => open_in_browser(app),
        "zoom_in" => apply_zoom(app, Some(ZOOM_STEP)),
        "zoom_out" => apply_zoom(app, Some(-ZOOM_STEP)),
        "zoom_reset" => apply_zoom(app, None),
        "quit" => app.exit(0),
        other if other.starts_with("recent_") => {
            if let Some(idx) = other.strip_prefix("recent_").and_then(|s| s.parse::<usize>().ok())
            {
                let mut cfg = load_config(app);
                if let Some(url) = cfg.recent_servers.get(idx).cloned() {
                    if let Ok(parsed) = Url::parse(&url) {
                        // Make this the active server and move it to the top of
                        // history, then connect — showing the branded error
                        // page (not silence) if the server is unreachable.
                        cfg.server_url = Some(url.clone());
                        remember_recent(&mut cfg, &url);
                        let _ = save_config(app, &cfg);
                        if let Some(win) = focused_window(app) {
                            connect_or_show_error(app, &win, parsed);
                        }
                        // Rebuild the menu so the picked server moves to the top.
                        let _ = build_menu(app);
                    }
                }
            }
        }
        _ => {}
    }
}

// ──────────────────────────────────────────────────────────────────────────
//  App entry point
// ──────────────────────────────────────────────────────────────────────────

pub fn run() {
    tauri::Builder::default()
        // Single-instance: if Omnifin is already running, focus that window
        // instead of opening a second copy.
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| {
            show_main(app);
        }))
        // Persist window size/position between launches.
        .plugin(tauri_plugin_window_state::Builder::default().build())
        .plugin(tauri_plugin_opener::init())
        .invoke_handler(tauri::generate_handler![
            set_server_url,
            get_server_url,
            get_recent_servers,
            clear_server_url,
            get_connect_error,
            retry_connect,
            open_setup,
            open_current_in_browser
        ])
        .setup(|app| {
            app.manage(ConnectErrorState::default());
            let handle = app.handle().clone();
            build_menu(&handle)?;
            app.on_menu_event(move |app, event| handle_menu_event(app, event.id().as_ref()));

            // System tray icon — click to show, right-click for quick menu.
            let tray_open = MenuItemBuilder::with_id("tray_open", "Open Omnifin").build(app)?;
            let tray_change = MenuItemBuilder::with_id("tray_change", "Change Server URL…").build(app)?;
            let tray_quit = MenuItemBuilder::with_id("tray_quit", "Quit Omnifin").build(app)?;
            let tray_menu = MenuBuilder::new(app)
                .items(&[&tray_open, &tray_change, &tray_quit])
                .build()?;

            let _tray = TrayIconBuilder::with_id("omnifin-tray")
                .tooltip("Omnifin")
                .icon(app.default_window_icon().cloned().unwrap())
                .menu(&tray_menu)
                .show_menu_on_left_click(false)
                .on_menu_event(|app, event| match event.id().as_ref() {
                    "tray_open" => show_main(app),
                    "tray_change" => {
                        let _ = open_setup_window(app);
                    }
                    "tray_quit" => app.exit(0),
                    _ => {}
                })
                .on_tray_icon_event(|tray, event| {
                    if let TrayIconEvent::Click {
                        button: MouseButton::Left,
                        button_state: MouseButtonState::Up,
                        ..
                    } = event
                    {
                        show_main(tray.app_handle());
                    }
                })
                .build(app)?;

            open_main_window(&handle)?;
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
