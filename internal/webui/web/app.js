'use strict';

// ---- Translations ----------------------------------------------------
const translations = {};
let currentLang = 'en';

async function loadTranslations() {
    try {
        Object.assign(translations, await window.api_i18n() || {});
    } catch (e) {
        console.error('Failed to load translations:', e);
    }
}

// tt looks a key up in the current language, then English, then returns
// the fallback (or the key). {name} placeholders are filled from vars.
function tt(key, vars, fallback) {
    const cur = translations[currentLang] || {};
    const en = translations.en || {};
    let s = cur[key] ?? en[key] ?? fallback ?? key;
    if (vars) for (const [k, v] of Object.entries(vars)) s = s.split('{' + k + '}').join(String(v));
    return s;
}

const pluralRules = {};
// plural('pages', 3) -> "3 strony" using CLDR plural categories.
function plural(kind, n) {
    const rules = pluralRules[currentLang] || (pluralRules[currentLang] = new Intl.PluralRules(currentLang));
    const cat = rules.select(n);
    const cur = translations[currentLang] || {};
    const en = translations.en || {};
    const key = 'count.' + kind + '.' + cat;
    const other = 'count.' + kind + '.other';
    const tpl = cur[key] ?? cur[other] ?? en['count.' + kind + '.' + (n === 1 ? 'one' : 'other')] ?? ('{n} ' + kind);
    return tpl.split('{n}').join(formatNumber(n));
}

function formatNumber(n) {
    try { return new Intl.NumberFormat(currentLang).format(n); } catch (_) { return String(n); }
}

function formatBytes(n) {
    n = Number(n) || 0;
    if (n <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    let num;
    try { num = new Intl.NumberFormat(currentLang, { maximumFractionDigits: i === 0 ? 0 : 1 }).format(n); } catch (_) { num = n.toFixed(i === 0 ? 0 : 1); }
    return num + ' ' + units[i];
}

function formatDuration(ms) {
    const s = Math.max(0, Math.round(ms / 1000));
    const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
    const pad = v => String(v).padStart(2, '0');
    return h > 0 ? h + ':' + pad(m) + ':' + pad(sec) : m + ':' + pad(sec);
}

function applyTranslations() {
    document.documentElement.lang = currentLang;
    document.documentElement.dir = ['ar', 'he'].includes(currentLang) ? 'rtl' : 'ltr';
    document.title = tt('appTitle', null, 'WebDownloader');
    document.querySelectorAll('[data-i18n]').forEach(el => { el.textContent = tt(el.dataset.i18n, null, el.textContent); });
    document.querySelectorAll('[data-i18n-placeholder]').forEach(el => { el.placeholder = tt(el.dataset.i18nPlaceholder, null, el.placeholder); });
    document.querySelectorAll('[data-i18n-title]').forEach(el => { el.title = tt(el.dataset.i18nTitle, null, el.title); });
    document.querySelectorAll('[data-i18n-aria]').forEach(el => { el.setAttribute('aria-label', tt(el.dataset.i18nAria)); });
    updateModeHint();
    updateEngineOptions();
    updatePauseLabel();
    updateDownloadAllHint();
    renderHistory();
    if (lastFinished) renderFinished(lastFinished);
}

// ---- Go -> JS events ---------------------------------------------------
const listeners = { progress: [], complete: [], error: [], asset: [] };
function fire(type, d) { (listeners[type] || []).forEach(cb => { try { cb(d); } catch (e) { console.error(e); } }); }
window._fireProgress = d => fire('progress', d);
window._fireAsset = d => fire('asset', d);
window._fireError = d => fire('error', d);
window._fireComplete = d => fire('complete', d);

// ---- DOM -----------------------------------------------------------------
const $ = id => document.getElementById(id);
const form = $('downloadForm');
const urlInput = $('url');
const urlError = $('urlError');
const outputDirInput = $('outputDir');
const statusPanel = $('status');
const historyList = $('historyList');
const logList = $('log');

let engines = { webview2: true, edge: true, chrome: true };
let downloadHistory = [];
let downloadQueue = [];
let run = null;            // the active download
let lastFinished = null;   // data of the last finished run (for re-render)
let lastHistorySave = 0;
let toastTimer = null;

// ---- Small helpers -------------------------------------------------------
function toast(message) {
    const el = $('toast');
    el.textContent = message;
    el.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { el.hidden = true; }, 5000);
}

function addLog(message, type) {
    const li = document.createElement('li');
    li.className = type || '';
    li.textContent = '[' + new Date().toLocaleTimeString(currentLang) + '] ' + message;
    li.title = message;
    logList.insertBefore(li, logList.firstChild);
    while (logList.children.length > 300) logList.removeChild(logList.lastChild);
}

function joinPath(dir, name) {
    if (!dir) return '';
    const sep = dir.includes('\\') ? '\\' : '/';
    return dir.replace(/[\\/]+$/, '') + sep + name;
}

function parentDir(path) {
    const trimmed = String(path || '').replace(/[\\/]+$/, '');
    const idx = Math.max(trimmed.lastIndexOf('\\'), trimmed.lastIndexOf('/'));
    return idx > 0 ? trimmed.slice(0, idx) : trimmed;
}

function shortURL(u) {
    try { const p = new URL(u); return decodeURI(p.pathname + p.search) || '/'; } catch (_) { return u; }
}

function button(cls, icon, label, onClick) {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = cls;
    const i = document.createElement('i');
    i.className = 'fas fa-' + icon;
    i.setAttribute('aria-hidden', 'true');
    b.append(i, ' ', label);
    b.addEventListener('click', onClick);
    return b;
}

// ---- Modal (focus starts on Cancel; Esc cancels) ------------------------
function confirmDialog({ title, body, confirmLabel, danger = true, confirmDisabled = false }) {
    return new Promise(resolve => {
        const modal = $('modal');
        if (!modal.hidden) { resolve(false); return; } // one dialog at a time
        const cancelBtn = $('modalCancel');
        const confirmBtn = $('modalConfirm');
        const previous = document.activeElement;
        $('modalTitle').textContent = title;
        const bodyEl = $('modalBody');
        bodyEl.replaceChildren(...body);
        cancelBtn.textContent = tt('btnDialogCancel', null, 'Anuluj');
        confirmBtn.textContent = confirmLabel;
        confirmBtn.className = 'btn ' + (danger ? 'btn-danger' : 'btn-primary');
        confirmBtn.hidden = confirmDisabled;
        modal.hidden = false;
        cancelBtn.focus();

        const close = result => {
            modal.hidden = true;
            document.removeEventListener('keydown', onKey, true);
            cancelBtn.onclick = confirmBtn.onclick = modal.onclick = null;
            if (previous && previous.focus) previous.focus();
            resolve(result);
        };
        const onKey = e => {
            if (e.key === 'Escape') { e.preventDefault(); close(false); }
            if (e.key === 'Tab') { // keep focus inside the dialog
                const items = [cancelBtn, confirmBtn].filter(b => !b.hidden);
                const idx = items.indexOf(document.activeElement);
                e.preventDefault();
                items[(idx + (e.shiftKey ? items.length - 1 : 1)) % items.length].focus();
            }
        };
        document.addEventListener('keydown', onKey, true);
        cancelBtn.onclick = () => close(false);
        confirmBtn.onclick = () => close(true);
        modal.onclick = e => { if (e.target === modal) close(false); };
    });
}

function para(text, cls) {
    const p = document.createElement(cls === 'modal-path' ? 'div' : 'p');
    if (cls) p.className = cls;
    p.textContent = text;
    return p;
}

// ---- Options --------------------------------------------------------------
function getMode() {
    return document.querySelector('input[name="mode"]:checked')?.value || 'http';
}

function setMode(mode) {
    const el = document.querySelector('input[name="mode"][value="' + (mode === 'browser' ? 'browser' : 'http') + '"]');
    if (el) el.checked = true;
    updateModeHint();
}

function updateModeHint() {
    const browser = getMode() === 'browser';
    $('modeHint').textContent = tt(browser ? 'modeHintBrowser' : 'modeHintFast');
    $('engine').disabled = !browser;
}

function updateEngineOptions() {
    for (const opt of $('engine').options) {
        if (!opt.value) continue;
        const base = tt(opt.dataset.i18n, null, opt.textContent);
        opt.textContent = engines[opt.value] === false ? base + ' (' + tt('engineMissing') + ')' : base;
    }
}

function limitValue(id) {
    const v = parseInt($(id).value, 10);
    return Number.isFinite(v) && v > 0 ? v : 0;
}

function updateDownloadAllHint() {
    const on = $('downloadAll').checked;
    const unlimited = !limitValue('maxTotalMB') && !limitValue('maxFileMB');
    $('downloadAllHint').hidden = !(on && unlimited);
}

function collectOptions() {
    return {
        url: urlInput.value.trim(),
        depth: parseInt($('depth').value, 10) || 2,
        downloadAll: $('downloadAll').checked,
        outputDir: outputDirInput.value.trim(),
        mode: getMode(),
        engine: $('engine').value,
        existing: $('existing').value,
        includeSubdomains: $('includeSubdomains').checked,
        includeExternal: $('includeExternal').checked,
        maxPages: limitValue('maxPages'),
        maxTotalMB: limitValue('maxTotalMB'),
        maxFileMB: limitValue('maxFileMB'),
    };
}

function applyOptions(o) {
    if (!o) return;
    if (typeof o.url === 'string') urlInput.value = o.url;
    if (o.depth) $('depth').value = String(o.depth);
    if (typeof o.downloadAll === 'boolean') $('downloadAll').checked = o.downloadAll;
    if (o.outputDir) { outputDirInput.value = o.outputDir; showPathEnd(); }
    setMode(o.mode || 'http');
    if (typeof o.engine === 'string') $('engine').value = o.engine;
    if (o.existing) $('existing').value = o.existing;
    $('includeSubdomains').checked = !!o.includeSubdomains;
    $('includeExternal').checked = !!o.includeExternal;
    for (const id of ['maxPages', 'maxTotalMB', 'maxFileMB']) $(id).value = o[id] > 0 ? String(o[id]) : '';
    updateDownloadAllHint();
}

// The cookie is a secret: it is never written to prefs.json or history.
function saveOptions() {
    if (!$('rememberOptions').checked) return;
    window.api_savePrefs(JSON.stringify({ ...collectOptions(), rememberOptions: true, cookie: '', lang: currentLang }))
        .catch(e => console.warn('[prefs] save failed:', e));
}

async function loadPrefs() {
    try { const raw = await window.api_loadPrefs(); return raw ? JSON.parse(raw) : {}; } catch (_) { return {}; }
}

// ---- URL validation -------------------------------------------------------
function normalizeURL(raw) {
    let v = raw.trim();
    if (!v) return { error: tt('urlEmpty') };
    if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(v)) {
        // Local addresses usually have no TLS; everything else gets https.
        const host = v.replace(/^\/+/, '').split(/[/:?#]/)[0].toLowerCase();
        const local = host === 'localhost' || /^\d{1,3}(\.\d{1,3}){3}$/.test(host) || /\.(localhost|local|test|lan)$/.test(host);
        v = (local ? 'http://' : 'https://') + v.replace(/^\/+/, '');
    }
    let u;
    try { u = new URL(v); } catch (_) { return { error: tt('urlInvalid') }; }
    const hostOK = u.hostname && (u.hostname.includes('.') || u.hostname === 'localhost' || u.hostname.startsWith('['));
    if (!/^https?:$/.test(u.protocol) || !hostOK || /\s/.test(raw.trim())) return { error: tt('urlInvalid') };
    return { url: u.href };
}

function showURLError(message) {
    urlError.textContent = message || '';
    urlError.hidden = !message;
    urlInput.setAttribute('aria-invalid', message ? 'true' : 'false');
}

urlInput.addEventListener('input', () => { if (!urlError.hidden) showURLError(''); });

// ---- History --------------------------------------------------------------
function historyKey(item) {
    const url = (item?.request?.url || item?.url || '').trim().toLowerCase();
    return url + '|' + (item?.output || '').trim().toLowerCase();
}

function dedupeHistory(items) {
    const seen = new Set();
    return (items || []).filter(item => {
        if (!item) return false;
        const key = historyKey(item);
        if (key === '|') return true;
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
    });
}

// What gets stored for "resume": never the cookie nor one-shot fields.
function historyRequest(request) {
    const { cookie, resumeOutput, ...rest } = request || {};
    return rest;
}

async function persistHistory() {
    downloadHistory = dedupeHistory(downloadHistory).slice(0, 30);
    try { await window.api_savePrefs(JSON.stringify({ history: downloadHistory })); } catch (_) {}
}

async function upsertHistory(entry) {
    let idx = downloadHistory.findIndex(x => x.id === entry.id);
    if (idx < 0 && historyKey(entry) !== '|') idx = downloadHistory.findIndex(x => historyKey(x) === historyKey(entry));
    if (idx >= 0) downloadHistory[idx] = { ...downloadHistory[idx], ...entry };
    else downloadHistory.unshift(entry);
    await persistHistory();
    renderHistory();
}

async function initHistory() {
    const prefs = await loadPrefs();
    let changed = false;
    downloadHistory = (Array.isArray(prefs.history) ? prefs.history : []).map(item => {
        if (item && (item.status === 'downloading' || item.status === 'paused')) {
            changed = true;
            item = { ...item, status: 'interrupted' };
        }
        if (item && item.request && item.request.cookie) {
            changed = true;
            item = { ...item, request: historyRequest(item.request) };
        }
        return item;
    });
    const before = downloadHistory.length;
    downloadHistory = dedupeHistory(downloadHistory);
    if (changed || before !== downloadHistory.length) await persistHistory();
    renderHistory();
}

const statusIcons = { completed: 'check', warn: 'exclamation-triangle', error: 'times', cancelled: 'ban', interrupted: 'pause', paused: 'pause', downloading: 'spinner' };

function renderHistory() {
    historyList.replaceChildren();
    if (!downloadHistory.length) {
        historyList.appendChild(emptyHistory());
        $('clearHistoryBtn').disabled = true;
        return;
    }
    $('clearHistoryBtn').disabled = isRunning();
    for (const item of downloadHistory) historyList.appendChild(historyRow(item));
}

function historyRow(item) {
    const li = document.createElement('li');
    li.className = 'history-item';
    const status = item.status || 'unknown';
    const isRunning = run && (run.historyId === item.id) && !run.finished;

    const top = document.createElement('div');
    top.className = 'history-top';
    const title = document.createElement('div');
    title.className = 'history-title';
    title.textContent = item.url || item.output || '–';
    title.title = item.url || '';
    const badge = document.createElement('span');
    badge.className = 'badge ' + status;
    const icon = document.createElement('i');
    icon.className = 'fas fa-' + (statusIcons[status] || 'question');
    icon.setAttribute('aria-hidden', 'true');
    badge.append(icon, ' ', tt('statusLabel.' + status, null, status));
    top.append(title, badge);

    const meta = document.createElement('div');
    meta.className = 'history-meta';
    const when = item.finishedAt || item.startedAt;
    const parts = [];
    if (when) parts.push(new Date(when).toLocaleString(currentLang));
    parts.push(plural('pages', item.pages || 0), plural('assets', item.assets || 0));
    if (item.attachments) parts.push(plural('files', item.attachments));
    parts.push(formatBytes(item.bytes || 0));
    if (item.elapsedMs) parts.push(formatDuration(item.elapsedMs));
    meta.textContent = parts.join(' · ');

    const path = document.createElement('div');
    path.className = 'history-path';
    path.textContent = item.output || '';

    const actions = document.createElement('div');
    actions.className = 'mini-actions';
    if (['interrupted', 'cancelled', 'error', 'paused'].includes(status) && !isRunning) {
        const label = status === 'error' ? tt('btnRetry') : tt('btnResume');
        actions.appendChild(button('mini-btn', status === 'error' ? 'redo' : 'play', label, () => resumeHistoryItem(item)));
    }
    const hasFiles = status !== 'error' || (item.pages || 0) > 0;
    if (hasFiles) {
        actions.appendChild(button('mini-btn', 'play-circle', tt('btnPreview'), () => startPreview(item.output, item.startFile)));
        actions.appendChild(button('mini-btn', 'external-link-alt', tt('btnOpenPage'), () => openFile(item.startFile || joinPath(item.output, 'index.html'))));
        actions.appendChild(button('mini-btn', 'folder-open', tt('btnFolder'), () => openFolder(item.output)));
        actions.appendChild(button('mini-btn', 'file-alt', tt('btnReport'), () => openFile(item.reportPath || joinPath(item.output, 'download-report.json'))));
    }
    const del = button('mini-btn danger', 'trash', tt('btnDelete'), () => deleteHistoryItem(item));
    del.disabled = !!isRunning;
    actions.appendChild(del);

    li.append(top, meta, path, actions);
    return li;
}

async function deleteHistoryItem(item) {
    const info = item.output ? await window.api_folderInfo(item.output).catch(() => null) : null;
    const body = [para(item.url || '')];
    if (item.output) body.push(para(item.output, 'modal-path'));
    let blocked = false;
    if (info && info.busy) {
        body.push(para(tt('delete.busy')));
        blocked = true;
    } else if (info && info.exists) {
        const size = para(tt('delete.size', { size: formatBytes(info.bytes), files: plural('errors', info.files) }), 'modal-size');
        body.push(size, para(tt('delete.body')));
    } else {
        body.push(para(tt('delete.missing')));
    }
    const ok = await confirmDialog({ title: tt('delete.title'), body, confirmLabel: info && info.exists ? tt('btnConfirmDelete') : tt('btnDelete'), confirmDisabled: blocked });
    if (!ok) return;
    if (item.output && info && info.exists) {
        const result = await window.api_deleteFolder(item.output).catch(e => 'error: ' + e);
        if (result !== 'ok') {
            toast(result === 'error: busy' ? tt('delete.busy') : tt('log.error') + ' ' + result);
            return;
        }
    }
    await window.api_deleteHistoryItem(item.id || '').catch(() => {});
    downloadHistory = downloadHistory.filter(x => x.id !== item.id);
    await persistHistory();
    renderHistory();
    toast(tt('log.deleted') + ' ' + (item.output || item.url || ''));
}

$('clearHistoryBtn').addEventListener('click', async () => {
    const ok = await confirmDialog({ title: tt('clear.title'), body: [para(tt('clear.body'))], confirmLabel: tt('btnConfirmClear') });
    if (!ok) return;
    downloadHistory = downloadHistory.filter(x => run && x.id === run.historyId && !run.finished);
    await persistHistory();
    renderHistory();
});

// ---- Opening things ---------------------------------------------------------
async function openFile(path) {
    try { await window.api_openFile(path); } catch (_) { toast(tt('openFailed')); }
}
async function openFolder(path) {
    try { await window.api_openFolder(path); } catch (_) { toast(tt('openFailed')); }
}
async function startPreview(output, startFile) {
    try {
        const res = await window.api_startPreview(output || '', startFile || '');
        if (res.error) toast(tt('previewFailed', { error: res.error }));
        else toast(tt('previewOpened', { url: res.url }));
    } catch (e) {
        toast(tt('previewFailed', { error: String(e) }));
    }
}

// ---- Running a download -------------------------------------------------------
function setBusy(busy) {
    $('downloadBtn').disabled = busy;
    $('runControls').hidden = !busy;
    $('cancelBtn').disabled = false;
    $('pauseBtn').disabled = false;
    $('clearHistoryBtn').disabled = busy || !downloadHistory.length;
}

function isRunning() { return !!(run && !run.finished); }

function setStatus(state, title) {
    const icons = { downloading: 'spinner fa-spin', starting: 'spinner fa-spin', success: 'check', warn: 'exclamation-triangle', error: 'times', cancelled: 'ban', paused: 'pause' };
    const iconEl = $('statusIcon');
    iconEl.className = 'status-icon ' + state;
    iconEl.innerHTML = '<i class="fas fa-' + (icons[state] || 'spinner') + '"></i>';
    $('statusTitle').textContent = title;
    statusPanel.classList.remove('is-error', 'is-warn', 'is-success', 'is-paused');
    if (state === 'error') statusPanel.classList.add('is-error');
    if (state === 'warn') statusPanel.classList.add('is-warn');
    if (state === 'success') statusPanel.classList.add('is-success');
    if (state === 'paused') statusPanel.classList.add('is-paused');
}

function updatePauseLabel() {
    const paused = !!(run && run.paused);
    $('pauseLabel').textContent = tt(paused ? 'btnResume' : 'btnPause');
    $('pauseBtn').querySelector('i').className = 'fas fa-' + (paused ? 'play' : 'pause');
}

function resetStatusCard(url, baseline) {
    statusPanel.hidden = false;
    $('progressBlock').hidden = false;
    $('errorBlock').hidden = true;
    $('notes').hidden = true;
    $('notes').replaceChildren();
    $('finishActions').hidden = true;
    $('statusUrl').textContent = url;
    $('statusUrl').title = url;
    $('metrics').hidden = false;
    $('currentRow').hidden = false;
    $('statsRow').hidden = false;
    $('outputRow').hidden = false;
    $('outputPath').textContent = '–';
    $('outputLabel').textContent = tt('output.saving');
    $('pageCount').textContent = formatNumber(baseline.pages || 0);
    $('assetCount').textContent = formatNumber(baseline.assets || 0);
    $('attachmentCount').textContent = formatNumber(baseline.attachments || 0);
    $('bytesStat').textContent = formatBytes(baseline.bytes || 0);
    $('currentFile').textContent = '–';
    $('metricSpeed').textContent = '–';
    $('metricElapsed').textContent = '0:00';
    $('metricEta').textContent = '–';
    $('metricQueue').textContent = '–';
    setProgress(null);
    logList.replaceChildren();
}

function setProgress(pct, label) {
    const bar = $('progressBar');
    if (pct === null || pct === undefined) {
        bar.classList.add('indeterminate');
        bar.removeAttribute('aria-valuenow');
        $('progressFill').style.width = '';
        $('progressLabel').textContent = label || tt('progress.unknown');
    } else {
        bar.classList.remove('indeterminate');
        bar.setAttribute('aria-valuemin', '0');
        bar.setAttribute('aria-valuemax', '100');
        bar.setAttribute('aria-valuenow', String(pct));
        $('progressFill').style.width = pct + '%';
        $('progressLabel').textContent = label || (pct + '%');
    }
}

form.addEventListener('submit', e => {
    e.preventDefault();
    startDownload(null);
});

$('queueBtn').addEventListener('click', () => {
    const check = normalizeURL(urlInput.value);
    if (check.error) { showURLError(check.error); urlInput.focus(); return; }
    urlInput.value = check.url;
    const req = { ...collectOptions(), url: check.url, cookie: $('cookieHeader').value };
    if (!run || run.finished) { startDownload(null, req); return; }
    downloadQueue.push(req);
    addLog(tt('log.queued', { url: req.url, n: downloadQueue.length }), 'info');
    toast(tt('log.queued', { url: req.url, n: downloadQueue.length }));
});

// startDownload runs the form (or a queued request). resume = history item.
async function startDownload(resume, queuedRequest) {
    if (run && !run.finished) {
        toast(tt('log.firstCancel'));
        return;
    }
    let request;
    if (queuedRequest) {
        request = queuedRequest;
    } else {
        const check = normalizeURL(urlInput.value);
        if (check.error) { showURLError(check.error); urlInput.focus(); return; }
        showURLError('');
        urlInput.value = check.url;
        request = { ...collectOptions(), url: check.url, cookie: $('cookieHeader').value };
    }
    if (resume) request.resumeOutput = resume.output;
    saveOptions();

    const baseline = resume ? { pages: resume.pages || 0, assets: resume.assets || 0, attachments: resume.attachments || 0, bytes: resume.bytes || 0 } : { pages: 0, assets: 0, attachments: 0, bytes: 0 };
    run = {
        id: null, historyId: resume ? resume.id : null, url: request.url, request, baseline,
        startedAt: Date.now(), paused: false, pausedMs: 0, pausedAt: 0, finished: false,
        samples: [], pages: baseline.pages, assets: baseline.assets, attachments: baseline.attachments, bytes: baseline.bytes,
    };
    lastFinished = null;
    resetStatusCard(request.url, baseline);
    setStatus('starting', tt(request.mode === 'browser' ? 'statusTitle.starting' : 'statusTitle.downloading'));
    setBusy(true);
    updatePauseLabel();
    statusPanel.scrollIntoView({ block: 'start', behavior: 'smooth' });

    let result;
    try {
        result = await window.api_download(JSON.stringify(request));
    } catch (e) {
        result = { error: String(e && e.message || e) };
    }
    if (result.error) {
        finishRun({ Failed: true, FailKind: classifyStartError(result.error), FailDetail: result.error }, true);
        return;
    }
    run.id = result.id;
    run.output = result.output;
    if (run.cancelling) { run.cancelling = false; requestCancel(); }
    run.startFile = result.startFile || joinPath(result.output, 'index.html');
    run.updating = !!result.updating;
    run.historyId = run.historyId || result.id;
    $('outputPath').textContent = result.output;
    run.timer = setInterval(updateMetrics, 1000);
    addLog(tt('log.started'), 'info');
    await upsertHistory({
        id: run.historyId, runId: result.id, url: request.url, output: result.output,
        outputBase: request.outputDir, startFile: run.startFile, request: historyRequest(request),
        status: 'downloading', startedAt: new Date().toISOString(), ...baseline,
    });
}

function classifyStartError(msg) {
    if (/^busy/.test(msg)) return 'busy';
    if (/^cannot resume/.test(msg)) return 'resume';
    if (/invalid url|empty url/.test(msg)) return 'invalid';
    return 'other';
}

function activeElapsed() {
    if (!run) return 0;
    const pausedNow = run.paused ? Date.now() - run.pausedAt : 0;
    return Date.now() - run.startedAt - run.pausedMs - pausedNow;
}

function updateMetrics() {
    if (!run || run.finished) return;
    const elapsed = activeElapsed();
    $('metricElapsed').textContent = formatDuration(elapsed);
    // Speed over the last ~5 s of samples.
    const now = Date.now();
    // Network bytes (also of files still downloading), not only saved ones.
    run.samples.push({ t: now, b: Math.max(run.transferred || 0, run.bytes) });
    while (run.samples.length > 1 && now - run.samples[0].t > 5000) run.samples.shift();
    const first = run.samples[0];
    if (!run.paused && run.samples.length > 1 && now > first.t) {
        const last = run.samples[run.samples.length - 1];
        const bps = (last.b - first.b) / ((now - first.t) / 1000);
        $('metricSpeed').textContent = bps > 0 ? formatBytes(bps) + '/s' : '–';
    } else if (run.paused) {
        $('metricSpeed').textContent = '–';
    }
    // ETA from the share of pages done (or of the size limit), once there
    // is a rate: elapsed time scaled by what is left.
    const frac = run.frac || 0;
    if (frac > 0 && frac < 1 && run.queued > 0 && elapsed > 5000 && !run.paused) {
        $('metricEta').textContent = '~' + formatDuration(elapsed * (1 - frac) / frac);
    } else {
        $('metricEta').textContent = '–';
    }
}

function updateCounters() {
    $('pageCount').textContent = formatNumber(run.pages);
    $('assetCount').textContent = formatNumber(run.assets);
    $('attachmentCount').textContent = formatNumber(run.attachments);
    $('bytesStat').textContent = formatBytes(run.bytes);
}

listeners.progress.push(evt => {
    if (!run || evt.id !== run.id) return;
    const d = evt.payload || {};
    if (!run.paused && $('statusIcon').classList.contains('starting')) setStatus('downloading', tt('statusTitle.downloading'));
    const b = run.baseline;
    run.pages = Math.max(run.pages, b.pages + (d.Pages || 0));
    run.assets = Math.max(run.assets, b.assets + (d.Assets || 0));
    run.attachments = Math.max(run.attachments, b.attachments + (d.Attachments || 0));
    run.bytes = Math.max(run.bytes, b.bytes + (d.Bytes || 0));
    run.transferred = Math.max(run.transferred || 0, d.Transferred || 0);
    updateCounters();
    $('metricQueue').textContent = formatNumber(d.Queued || 0);
    if (d.Current) setCurrentFile(d.Current);

    // Progress: pages done vs. done + queued (or the page/size limit).
    const done = d.Pages || 0;
    const total = done + (d.Queued || 0) + (d.Status === 'downloading' ? 1 : 0);
    let pct = null;
    const maxPages = run.request.maxPages || 0;
    const maxBytes = (run.request.maxTotalMB || 0) * 1048576;
    if (run.request.depth <= 1) pct = d.Status === 'done' ? 100 : null;
    else if (total > 0 && (done > 0 || d.Queued > 0)) pct = Math.floor(done / total * 100);
    if (maxPages > 0) pct = Math.max(pct || 0, Math.floor(done / maxPages * 100));
    if (maxBytes > 0) pct = Math.max(pct || 0, Math.floor((d.Bytes || 0) / maxBytes * 100));
    run.queued = d.Queued || 0;
    if (total > 0 && done > 0) run.frac = Math.min(0.99, done / total);
    if (done > 0 && !d.Queued && d.Status !== 'downloading' && !maxBytes) {
        // All pages are saved; files (attachments) are still coming.
        run.pct = 99;
        run.frac = undefined;
        setProgress(99, tt('progress.finishing'));
    } else if (pct !== null) {
        pct = Math.min(99, pct);
        run.pct = pct;
        setProgress(pct, tt('progress.label', { pct, done: formatNumber(done), total: formatNumber(Math.max(total, done)) }));
    }

    if (d.URL) {
        if (d.Status === 'downloading') {
            setCurrentFile(d.URL);
            addLog(tt('log.downloading') + ' ' + d.URL, '');
        } else if (d.Status === 'done') {
            addLog(tt('log.downloaded') + ' ' + d.URL, 'success');
        }
    }

    const idx = downloadHistory.findIndex(x => x.id === run.historyId);
    if (idx >= 0) {
        Object.assign(downloadHistory[idx], { pages: run.pages, assets: run.assets, attachments: run.attachments, bytes: run.bytes });
        if (Date.now() - lastHistorySave > 2500) {
            lastHistorySave = Date.now();
            persistHistory().then(renderHistory);
        }
    }
});

listeners.asset.push(evt => {
    if (!run || evt.id !== run.id) return;
    const d = evt.payload || {};
    const attachment = d.kind === 'attachment';
    setCurrentFile(d.url);
    addLog(tt(attachment ? 'log.attachment' : 'log.asset') + ' ' + d.url, '');
});

listeners.error.push(evt => {
    if (!run || evt.id !== run.id) return;
    const d = evt.payload || {};
    addLog(tt('log.error') + ' ' + (d.url || '') + ' – ' + (d.error || ''), 'error');
});

listeners.complete.push(evt => {
    if (!run || evt.id !== run.id) return;
    finishRun(evt.payload || {}, false);
});

function errorMessage(kind, status) {
    if (kind === 'http') {
        if (status === 404 || status === 410) return tt('err.http404');
        if (status === 401 || status === 403) return tt('err.http401', { status });
        if (status >= 500) return tt('err.http5xx', { status });
        return tt('err.http', { status });
    }
    return tt('err.' + (kind || 'other'), null, tt('err.other'));
}

async function finishRun(summary, immediate) {
    if (!run || run.finished) return;
    run.finished = true;
    clearInterval(run.timer);
    setBusy(false);
    const elapsed = activeElapsed();
    const data = {
        url: run.url,
        output: run.output || '',
        startFile: summary.StartFile || '',
        failed: !!summary.Failed,
        cancelled: !!summary.Cancelled,
        failKind: summary.FailKind || '',
        failStatus: summary.FailStatus || 0,
        failDetail: summary.FailDetail || '',
        errors: summary.Errors || 0,
        skipped: summary.SkippedExternal || 0,
        skippedHosts: summary.SkippedExternalHosts || [],
        needsServer: !!summary.NeedsServer,
        engine: summary.Engine || '',
        updating: !!run.updating,
        pages: Math.max(run.pages, run.baseline.pages + (summary.Pages || 0)),
        assets: Math.max(run.assets, run.baseline.assets + (summary.Assets || 0)),
        attachments: Math.max(run.attachments, run.baseline.attachments + (summary.Attachments || 0)),
        bytes: Math.max(run.bytes, run.baseline.bytes + (summary.Bytes || 0)),
        elapsedMs: elapsed,
        reportPath: summary.ReportPath || '',
        request: run.request,
    };
    run.pages = data.pages; run.assets = data.assets; run.attachments = data.attachments; run.bytes = data.bytes;
    lastFinished = data;
    renderFinished(data);
    statusPanel.scrollIntoView({ block: 'start', behavior: 'smooth' });

    let status = 'completed';
    if (data.failed) status = 'error';
    else if (data.cancelled) status = 'cancelled';
    else if (data.errors > 0 || data.skipped > 0) status = 'warn';
    if (!immediate || run.historyId) {
        await upsertHistory({
            // A failed start leaves no folder (or one that belongs to an older
            // copy): never point the history entry, and its Delete, at it.
            id: run.historyId || ('failed-' + Date.now()), url: run.url, output: data.failed && !data.pages ? '' : data.output,
            startFile: data.startFile || run.startFile || '', request: historyRequest(run.request),
            status, finishedAt: new Date().toISOString(), reportPath: data.reportPath,
            pages: data.pages, assets: data.assets, attachments: data.attachments, bytes: data.bytes, elapsedMs: elapsed,
        });
    }
    if (!data.failed) {
        addLog(tt('log.complete', { pages: plural('pages', data.pages), assets: plural('assets', data.assets), files: plural('files', data.attachments) }), data.cancelled ? 'error' : 'success');
    }
    if (downloadQueue.length && !data.cancelled) setTimeout(runNextQueued, 400);
}

function renderFinished(d) {
    statusPanel.hidden = false;
    $('runControls').hidden = true;
    $('statusUrl').textContent = d.url;
    const notes = $('notes');
    notes.replaceChildren();
    const note = (text, cls) => { const li = document.createElement('li'); li.className = cls; li.textContent = text; notes.appendChild(li); };

    if (d.failed) {
        setStatus('error', tt('statusTitle.error'));
        $('progressBlock').hidden = true;
        $('errorBlock').hidden = false;
        $('errorReason').textContent = errorMessage(d.failKind, d.failStatus);
        $('errorTech').textContent = d.failDetail || d.failKind;
        $('finishActions').hidden = false;
        $('retryBtn').hidden = false;
        $('editUrlBtn').hidden = false;
        for (const id of ['previewBtn', 'openPageBtn', 'openFolderBtn', 'openReportBtn']) $(id).hidden = true;
        $('outputRow').hidden = true;
        $('statsRow').hidden = true;
        notes.hidden = true;
        return;
    }
    $('errorBlock').hidden = true;
    $('statsRow').hidden = false;
    $('retryBtn').hidden = true;
    $('editUrlBtn').hidden = true;
    for (const id of ['previewBtn', 'openPageBtn', 'openFolderBtn', 'openReportBtn']) $(id).hidden = false;
    $('openPageBtn').disabled = !d.startFile;
    $('previewBtn').disabled = !d.startFile;

    if (d.cancelled) setStatus('cancelled', tt('statusTitle.cancelled'));
    else if (d.errors > 0 || d.skipped > 0) setStatus('warn', tt('statusTitle.warn'));
    else setStatus('success', tt('statusTitle.complete'));

    $('progressBlock').hidden = false;
    setProgress(d.cancelled ? (lastPct() ?? 0) : 100, d.cancelled ? '' : '100%');
    // Live metrics mean nothing after the run; the summary note has the time.
    $('metrics').hidden = true;
    $('currentRow').hidden = true;
    $('outputRow').hidden = false;
    $('pageCount').textContent = formatNumber(d.pages);
    $('assetCount').textContent = formatNumber(d.assets);
    $('attachmentCount').textContent = formatNumber(d.attachments);
    $('bytesStat').textContent = formatBytes(d.bytes);
    $('outputLabel').textContent = tt('output.savedTo');
    $('outputPath').textContent = d.output;

    note(tt('summary.done', {
        pages: plural('pages', d.pages), assets: plural('assets', d.assets), files: plural('files', d.attachments),
        size: formatBytes(d.bytes), time: formatDuration(d.elapsedMs),
    }) + (d.engine ? ' ' + tt('summary.engine', { engine: d.engine }) : ''), d.cancelled ? 'info' : 'ok');
    if (d.updating) note(tt('warn.updated'), 'info');
    if (d.needsServer) note(tt('warn.server'), 'info');
    if (d.errors > 0) note(tt('warn.errors', { errors: plural('errors', d.errors) }), 'warn');
    if (d.skipped > 0) {
        const hosts = d.skippedHosts.slice(0, 3).join(', ') + (d.skippedHosts.length > 3 ? ', …' : '');
        note(tt('warn.cdn', { n: formatNumber(d.skipped), hosts }), 'warn');
    }
    notes.hidden = false;
    $('finishActions').hidden = false;
}

function setCurrentFile(u) {
    $('currentFile').textContent = shortURL(u);
    $('currentFile').title = u;
}

// emptyHistory is the first-run panel: what to do, in three steps.
function emptyHistory() {
    const li = document.createElement('li');
    li.className = 'history-empty';
    const lead = document.createElement('p');
    lead.className = 'empty-lead';
    lead.textContent = tt('historyEmpty');
    const title = document.createElement('h3');
    title.textContent = tt('howto.title');
    const steps = document.createElement('ol');
    steps.className = 'howto';
    for (const [icon, key] of [['link', 'howto.1'], ['layer-group', 'howto.2'], ['play-circle', 'howto.3']]) {
        const step = document.createElement('li');
        const i = document.createElement('i');
        i.className = 'fas fa-' + icon;
        i.setAttribute('aria-hidden', 'true');
        step.append(i, document.createTextNode(tt(key)));
        steps.appendChild(step);
    }
    const tip = document.createElement('p');
    tip.className = 'empty-tip';
    const bulb = document.createElement('i');
    bulb.className = 'fas fa-lightbulb';
    bulb.setAttribute('aria-hidden', 'true');
    tip.append(bulb, document.createTextNode(tt('howto.tip')));
    li.append(lead, title, steps, tip);
    return li;
}

// showPathEnd keeps the end of a long folder path visible (the folder name
// matters more than the drive) and puts the full path in the tooltip.
function showPathEnd() {
    const input = outputDirInput;
    input.title = input.value;
    if (document.activeElement !== input) requestAnimationFrame(() => { input.scrollLeft = input.scrollWidth; });
}

function lastPct() { return run && run.pct !== undefined ? run.pct : null; }

$('retryBtn').addEventListener('click', () => {
    if (!lastFinished) return;
    applyOptions(lastFinished.request);
    startDownload(null);
});
$('editUrlBtn').addEventListener('click', () => { urlInput.focus(); urlInput.select(); });
$('previewBtn').addEventListener('click', () => lastFinished && startPreview(lastFinished.output, lastFinished.startFile));
$('openPageBtn').addEventListener('click', () => lastFinished && openFile(lastFinished.startFile));
$('openFolderBtn').addEventListener('click', () => lastFinished && openFolder(lastFinished.output));
$('openReportBtn').addEventListener('click', () => lastFinished && openFile(lastFinished.reportPath || joinPath(lastFinished.output, 'download-report.json')));

$('cancelBtn').addEventListener('click', () => requestCancel());

// requestCancel stops the run. The UI switches to "Cancelling..." at once;
// the backend confirms with a "complete" event (it forces one itself after
// 2 s) and, as a last resort, the UI finishes the run on its own.
async function requestCancel() {
    if (!run || run.finished || run.cancelling) return;
    run.cancelling = true;
    downloadQueue = [];
    $('cancelBtn').disabled = true;
    $('pauseBtn').disabled = true;
    setStatus('cancelled', tt('statusTitle.cancelling'));
    addLog(tt('log.cancelling'), 'error');
    if (!run.id) return; // still starting: cancelled as soon as the id arrives
    const id = run.id;
    let accepted = '';
    try { accepted = await window.api_cancel(id); } catch (e) { console.error(e); }
    setTimeout(() => {
        if (run && run.id === id && !run.finished) finishRun({ Cancelled: true }, false);
    }, accepted ? 4000 : 0);
}

$('pauseBtn').addEventListener('click', async () => {
    if (!run || !run.id || run.finished) return;
    try {
        if (!run.paused) {
            await window.api_pause(run.id);
            run.paused = true;
            run.pausedAt = Date.now();
            setStatus('paused', tt('statusTitle.paused'));
            addLog(tt('log.paused'), 'info');
            await upsertHistory({ id: run.historyId, status: 'paused' });
        } else {
            await window.api_resume(run.id);
            run.pausedMs += Date.now() - run.pausedAt;
            run.paused = false;
            setStatus('downloading', tt('statusTitle.downloading'));
            addLog(tt('log.resumed'), 'info');
            await upsertHistory({ id: run.historyId, status: 'downloading' });
        }
        updatePauseLabel();
    } catch (e) { console.error(e); }
});

function runNextQueued() {
    if (!downloadQueue.length || (run && !run.finished)) return;
    const next = downloadQueue.shift();
    addLog(tt('log.queueStart', { url: next.url, n: downloadQueue.length }), 'info');
    applyOptions(next);
    startDownload(null, next);
}

function resumeHistoryItem(item) {
    if (run && !run.finished) { toast(tt('log.firstCancel')); return; }
    const req = { ...(item.request || {}), url: (item.request || {}).url || item.url };
    applyOptions(req);
    if (item.status === 'error' && !(item.pages > 0)) {
        startDownload(null); // nothing to resume: a fresh try
        return;
    }
    addLog(tt('log.resuming', { url: req.url }), 'info');
    startDownload(item);
}

// ---- Wiring -------------------------------------------------------------------
document.querySelectorAll('input[name="mode"]').forEach(r => r.addEventListener('change', () => { updateModeHint(); saveOptions(); }));
['depth', 'downloadAll', 'outputDir', 'engine', 'existing', 'includeSubdomains', 'includeExternal', 'maxPages', 'maxTotalMB', 'maxFileMB'].forEach(id => {
    $(id).addEventListener('change', () => { updateDownloadAllHint(); saveOptions(); });
});
['maxTotalMB', 'maxFileMB'].forEach(id => $(id).addEventListener('input', updateDownloadAllHint));
$('rememberOptions').addEventListener('change', e => {
    if (e.target.checked) saveOptions();
    else window.api_savePrefs(JSON.stringify({ rememberOptions: false })).catch(() => {});
});
outputDirInput.addEventListener('blur', showPathEnd);
outputDirInput.addEventListener('input', () => { outputDirInput.title = outputDirInput.value; });
window.addEventListener('resize', showPathEnd);

$('language').addEventListener('change', e => {
    currentLang = e.target.value;
    window.api_savePrefs(JSON.stringify({ lang: currentLang })).catch(() => {});
    applyTranslations();
});
$('pickFolderBtn').addEventListener('click', async () => {
    try {
        const picked = await window.api_pickFolder();
        if (picked) { outputDirInput.value = picked; showPathEnd(); saveOptions(); }
    } catch (e) { console.error('pick folder failed', e); }
});

// ---- Start-up: translations first, then everything that renders text ------
async function init() {
    await loadTranslations();
    const prefs = await loadPrefs();
    let lang = prefs.lang && translations[prefs.lang] ? prefs.lang : '';
    if (!lang) {
        try {
            const fromGo = await Promise.race([window.api_getLocale(), new Promise((_, r) => setTimeout(() => r(new Error('timeout')), 2000))]);
            if (translations[fromGo]) lang = fromGo;
        } catch (_) {}
    }
    if (!lang) {
        for (const l of (navigator.languages || [navigator.language || ''])) {
            const short = String(l).split('-')[0].toLowerCase();
            if (translations[short]) { lang = short; break; }
        }
    }
    currentLang = lang || 'en';
    $('language').value = currentLang;

    try { engines = await window.api_engines() || engines; } catch (_) {}

    if (prefs.rememberOptions) {
        $('rememberOptions').checked = true;
        applyOptions(prefs);
    } else {
        setMode('http');
    }
    if (!outputDirInput.value) {
        try { outputDirInput.value = await window.api_defaultOutputPath(''); } catch (_) {}
    }
    showPathEnd();
    applyTranslations();
    await initHistory();
    urlInput.focus();
}

init();
