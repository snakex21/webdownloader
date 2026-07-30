const translations = {};
let currentLang = 'en';
let translationsLoaded = false;

const langNames = {
    pl: 'Polski', en: 'English', de: 'Deutsch', fr: 'Français', es: 'Español',
    it: 'Italiano', pt: 'Português', ru: 'Русский', uk: 'Українська',
    cs: 'Čeština', sk: 'Slovenčina', hu: 'Magyar', ro: 'Română', nl: 'Nederlands',
    sv: 'Svenska', da: 'Dansk', fi: 'Suomi', el: 'Ελληνικά', tr: 'Türkçe',
    ar: 'العربية', he: 'עברית', ja: '日本語', ko: '한국어', zh: '中文',
    hi: 'हिन्दी', id: 'Bahasa Indonesia', vi: 'Tiếng Việt', th: 'ไทย',
    no: 'Norsk', bg: 'Български', hr: 'Hrvatski', ca: 'Català'
};

// ---- Event bridge: Go -> JS -----------------------------------------
// Go calls window._fireProgress / _fireAsset / _fireError / _fireComplete
// with a JSON payload of shape { id, payload }. We store user callbacks
// and dispatch based on the event type.
const listeners = {
    progress: [], complete: [], error: [], asset: []
};
function _fireProgress(d)  { (listeners.progress || []).forEach(cb => { try { cb(d); } catch(e) { console.error(e); } }); }
function _fireAsset(d)     { (listeners.asset    || []).forEach(cb => { try { cb(d); } catch(e) { console.error(e); } }); }
function _fireError(d)     { (listeners.error    || []).forEach(cb => { try { cb(d); } catch(e) { console.error(e); } }); }
function _fireComplete(d)  { (listeners.complete || []).forEach(cb => { try { cb(d); } catch(e) { console.error(e); } }); }

// ---- UI helpers -----------------------------------------------------
function updateLanguageSelectFlag() {
    const currentFlag = document.getElementById('currentFlag');
    const languageSelected = document.getElementById('languageSelected');
    const options = document.querySelectorAll('.language-option[data-lang="' + currentLang + '"]');
    if (options.length > 0) {
        if (currentFlag) currentFlag.textContent = (options[0].dataset.flag || 'gb').toUpperCase();
        languageSelected.querySelector('.language-name').textContent = langNames[currentLang] || 'English';
    }
}

document.getElementById('languageSelected')?.addEventListener('click', function() {
    document.querySelector('.language-dropdown').classList.toggle('open');
});

document.querySelectorAll('.language-option').forEach(option => {
    option.addEventListener('click', function() {
        const newLang = this.dataset.lang;
        if (translations[newLang] || newLang === 'en') {
            currentLang = newLang;
            window.api_savePrefs(JSON.stringify({ lang: newLang })).catch(error => {
                console.warn('[prefs] language save failed:', error);
            });
            updateLanguageSelectFlag();
            applyTranslations();
        }
        document.querySelector('.language-dropdown').classList.remove('open');
    });
});

document.addEventListener('click', function(e) {
    if (!e.target.closest('.language-dropdown')) {
        document.querySelector('.language-dropdown')?.classList.remove('open');
    }
});

async function loadTranslations() {
    try {
        const data = await window.api_i18n();
        if (data) {
            Object.assign(translations, data);
            translationsLoaded = true;
        }
    } catch (e) {
        console.error('Failed to load translations:', e);
        translationsLoaded = false;
    }
}

function tt(key, fallback) {
    const t = translations[currentLang] || translations['en'] || {};
    return t[key] !== undefined ? t[key] : (fallback !== undefined ? fallback : undefined);
}

function applyTranslations() {
    if (!translationsLoaded) return;

    const t = translations[currentLang] || translations['en'] || {};

    if (t.appTitle) document.title = t.appTitle;
    if (t.appDescription) document.querySelector('.header p').textContent = t.appDescription;
    const firstLabel = document.querySelector('.form-group label');
    if (t.labelUrl && firstLabel) firstLabel.textContent = t.labelUrl;
    if (t.placeholderUrl) document.getElementById('url').placeholder = t.placeholderUrl;
    const depthGroup = document.getElementById('depth')?.closest('.form-group');
    if (t.labelDepth && depthGroup) depthGroup.querySelector('label').textContent = t.labelDepth;

    const outputLabel = document.getElementById('labelOutput');
    if (t.labelOutput && outputLabel) outputLabel.textContent = t.labelOutput;

    const depthSelect = document.getElementById('depth');
    if (t.depth1) depthSelect.options[0].textContent = t.depth1;
    if (t.depth2) depthSelect.options[1].textContent = t.depth2;
    if (t.depth3) depthSelect.options[2].textContent = t.depth3;
    if (t.depth4) depthSelect.options[3].textContent = t.depth4;
    if (t.depth5) depthSelect.options[4].textContent = t.depth5;

    const checkboxText = document.querySelector('.checkbox-text');
    if (t.checkboxDownloadAll && checkboxText) checkboxText.innerHTML = '<i class="fas fa-file-archive"></i> ' + t.checkboxDownloadAll;

    const rememberText = document.getElementById('rememberOptions')?.closest('.checkbox-label')?.querySelector('.checkbox-text');
    if (t.checkboxRemember && rememberText) rememberText.innerHTML = '<i class="fas fa-save"></i> ' + t.checkboxRemember;

    // Advanced options
    const advLabel = document.getElementById('advancedLabel');
    if (t.advancedLabel && advLabel) advLabel.textContent = t.advancedLabel;
    if (t.modeLabel) {
        const ml = document.getElementById('modeLabel');
        if (ml) ml.textContent = t.modeLabel;
    }
    const modePills = document.querySelectorAll('.mode-pill span');
    if (modePills[0] && t.modeFast) modePills[0].textContent = t.modeFast;
    if (modePills[1] && t.modeBrowser) modePills[1].textContent = t.modeBrowser;
    if (t.modeHint) {
        const mh = document.getElementById('modeHint');
        if (mh) mh.textContent = t.modeHint;
    }
    if (t.includeSubdomains) {
        const el = document.querySelector('[data-i18n="includeSubdomains"]');
        if (el) el.textContent = t.includeSubdomains;
    }
    if (t.includeExternal) {
        const el = document.querySelector('[data-i18n="includeExternal"]');
        if (el) el.textContent = t.includeExternal;
    }
    if (t.maxPagesLabel) {
        const el = document.getElementById('maxPagesLabel');
        if (el) el.textContent = t.maxPagesLabel;
    }
    if (t.maxTotalLabel) {
        const el = document.getElementById('maxTotalLabel');
        if (el) el.textContent = t.maxTotalLabel;
    }
    if (t.maxFileLabel) {
        const el = document.getElementById('maxFileLabel');
        if (el) el.textContent = t.maxFileLabel;
    }

    // Buttons
    if (t.btnDownload) {
        const span = document.getElementById('downloadBtnLabel');
        if (span) span.textContent = t.btnDownload;
    }
    if (t.btnCancel) {
        const span = document.getElementById('cancelBtnLabel');
        if (span) span.textContent = t.btnCancel;
    }

    const savedTo = tt('output.savedTo');
    if (savedTo) {
        const el = document.querySelector('[data-i18n="savedTo"]');
        if (el) el.textContent = savedTo;
    }

    const openFolder = tt('output.openFolder');
    if (openFolder) {
        const el = document.querySelector('[data-i18n="openFolder"]');
        if (el) el.textContent = openFolder;
    }

    const statLabels = document.querySelectorAll('.stat-label');
    const sp = (k) => tt('stat.' + k);
    if (sp('pages') && statLabels[0]) statLabels[0].textContent = sp('pages');
    if (sp('assets') && statLabels[1]) statLabels[1].textContent = sp('assets');
    if (sp('files') && statLabels[2]) statLabels[2].textContent = sp('files');
    if (sp('bytes') && statLabels[3]) statLabels[3].textContent = sp('bytes');

    // Queue / pause / resume buttons
    const queueLabel = document.getElementById('queueBtnLabel');
    if (t.btnQueue && queueLabel) queueLabel.textContent = t.btnQueue;
    const pauseLabel = document.getElementById('pauseBtnLabel');
    if (t.btnPause && pauseLabel && !isPaused) pauseLabel.textContent = t.btnPause;
    if (t.btnResume && pauseLabel && isPaused) pauseLabel.textContent = t.btnResume;

    // Cookie label and hint
    const cookieLabelEl = document.getElementById('cookieLabel');
    if (t.cookieLabel && cookieLabelEl) cookieLabelEl.textContent = t.cookieLabel;
    const cookieHintEl = document.getElementById('cookieHint');
    if (t.cookieHint && cookieHintEl) cookieHintEl.textContent = t.cookieHint;

    // History panel
    const histTitle = document.getElementById('historyTitle');
    if (t.historyTitle && histTitle) histTitle.textContent = t.historyTitle;
    const clearLabel = document.getElementById('clearHistoryLabel');
    if (t.btnClearHistory && clearLabel) clearLabel.textContent = t.btnClearHistory;
    const histEmpty = document.getElementById('historyEmpty');
    if (t.historyEmpty && histEmpty) histEmpty.textContent = t.historyEmpty;

    // Current action buttons
    const openPageLabel = document.getElementById('openPageLabel');
    if (t.btnOpenPage && openPageLabel) openPageLabel.textContent = t.btnOpenPage;
    const revealPageLabel = document.getElementById('revealPageLabel');
    if (t.btnRevealPage && revealPageLabel) revealPageLabel.textContent = t.btnRevealPage;
    const reportLabel = document.getElementById('reportLabel');
    if (t.btnReport && reportLabel) reportLabel.textContent = t.btnReport;

    // Re-render history to update dynamic button labels
    if (downloadHistory.length) renderHistory();
}

function applyDeleteTranslations() {
    // Called from renderHistory which reads tt() directly
}

async function initI18n() {
    await loadTranslations();

    // Detect in this priority order:
    //   1. Go-side prefs file.
    //   2. Go-side WinAPI call (kernel32.GetUserDefaultLocaleName).
    //   3. Browser navigator.language / navigator.languages.
    //   4. 'en' fallback.
    let source = 'fallback';
    let systemLang = '';

    // 1. Go-side prefs file.
    try {
        const raw = await window.api_loadPrefs();
        if (raw) {
            const opts = JSON.parse(raw);
            if (opts.lang && translations[opts.lang]) {
                systemLang = opts.lang;
                source = 'prefs.json';
            }
        }
    } catch (_) {}

    // 2. WinAPI system locale
    if (!systemLang) {
        try {
            const fromGo = await Promise.race([
                window.api_getLocale(),
                new Promise((_, reject) => setTimeout(() => reject(new Error('timeout')), 2000)),
            ]);
            if (fromGo && translations[fromGo]) {
                systemLang = fromGo;
                source = 'api_getLocale';
            } else {
                console.log('[i18n] api_getLocale returned', JSON.stringify(fromGo), '— not in translations');
            }
        } catch (e) {
            console.log('[i18n] api_getLocale failed:', e.message);
        }
    }

    if (!systemLang) {
        const langs = (navigator.languages && navigator.languages.length)
            ? navigator.languages
            : (navigator.language ? [navigator.language] : []);
        for (const l of langs) {
            const short = l.split('-')[0].toLowerCase();
            if (translations[short]) { systemLang = short; source = 'navigator'; break; }
        }
    }

    if (!systemLang) {
        systemLang = 'en';
        source = 'en-fallback';
    }

    currentLang = systemLang;
    console.log('[i18n] selected', currentLang, 'from', source,
        '| navigator=', navigator.language);

    updateLanguageSelectFlag();
    applyTranslations();
}

initI18n();

// ---- DOM refs -------------------------------------------------------
const form = document.getElementById('downloadForm');
const downloadBtn = document.getElementById('downloadBtn');
const queueBtn = document.getElementById('queueBtn');
const pauseBtn = document.getElementById('pauseBtn');
const cancelBtn = document.getElementById('cancelBtn');
const status = document.getElementById('status');
const statusIcon = document.getElementById('statusIcon');
const statusTitle = document.getElementById('statusTitle');
const statusUrl = document.getElementById('statusUrl');
const progressFill = document.getElementById('progressFill');
const progressText = document.getElementById('progressText');
const pageCount = document.getElementById('pageCount');
const assetCount = document.getElementById('assetCount');
const attachmentCount = document.getElementById('attachmentCount');
const bytesStat = document.getElementById('bytesStat');
const outputPath = document.getElementById('outputPath');
const openFolderBtn = document.getElementById('openFolderBtn');
const openPageBtn = document.getElementById('openPageBtn');
const revealPageBtn = document.getElementById('revealPageBtn');
const openReportBtn = document.getElementById('openReportBtn');
const currentActions = document.getElementById('currentActions');
const historyList = document.getElementById('historyList');
const clearHistoryBtn = document.getElementById('clearHistoryBtn');
const log = document.getElementById('log');
const outputDirInput = document.getElementById('outputDir');
const pickFolderBtn = document.getElementById('pickFolderBtn');

let currentOutputPath = null;
let currentDownloadId = null;
let currentHistoryId = null;
let resumeHistoryId = null;
let resumeBaselinePending = null;
let currentBaseline = { pages: 0, assets: 0, attachments: 0, bytes: 0 };
let isPaused = false;
let downloadQueue = [];
let currentMode = 'http';
let downloadHistory = [];
let lastHistoryProgressSave = 0;

// ---- Mode pill toggle --------------------------------------------
document.querySelectorAll('.mode-pill').forEach(pill => {
    pill.addEventListener('click', () => {
        document.querySelectorAll('.mode-pill').forEach(p => p.classList.remove('active'));
        pill.classList.add('active');
        currentMode = pill.dataset.mode;
        const radio = pill.querySelector('input');
        if (radio) radio.checked = true;
        saveOptions();
    });
});

function getMode() {
    const active = document.querySelector('.mode-pill.active');
    return active ? (active.dataset.mode || 'http') : 'http';
}

// ---- Remember options ----------------------------------------------

function collectOptions() {
    return {
        rememberOptions: document.getElementById('rememberOptions')?.checked ?? false,
        url: document.getElementById('url')?.value || '',
        depth: document.getElementById('depth')?.value || '2',
        downloadAll: document.getElementById('downloadAll')?.checked ?? true,
        outputDir: outputDirInput?.value || '',
        mode: getMode(),
        includeSubdomains: document.getElementById('includeSubdomains')?.checked ?? false,
        includeExternal: document.getElementById('includeExternal')?.checked ?? false,
        maxPages: parseInt(document.getElementById('maxPages')?.value || '0', 10) || 0,
        maxTotalMB: parseInt(document.getElementById('maxTotalMB')?.value || '0', 10) || 0,
        maxFileMB: parseInt(document.getElementById('maxFileMB')?.value || '0', 10) || 0,
        cookie: document.getElementById('cookieHeader')?.value || '',
        lang: currentLang || '',
    };
}

function saveOptions() {
    if (!document.getElementById('rememberOptions')?.checked) return;
    const opts = collectOptions();
    window.api_savePrefs(JSON.stringify(opts)).catch(error => {
        console.warn('[prefs] save failed:', error);
    });
}

function saveRememberDisabled() {
    const opts = { rememberOptions: false, lang: currentLang || '' };
    window.api_savePrefs(JSON.stringify(opts)).catch(error => {
        console.warn('[prefs] save failed:', error);
    });
}

async function loadPrefsObject() {
    try {
        const raw = await window.api_loadPrefs();
        return raw ? JSON.parse(raw) : {};
    } catch (_) {
        return {};
    }
}

function localIndexPath(outputDir) {
    if (!outputDir) return '';
    const sep = outputDir.includes('\\') ? '\\' : '/';
    return outputDir.replace(/[\\/]+$/, '') + sep + 'index.html';
}

function parentDir(path) {
    if (!path) return '';
    const trimmed = String(path).replace(/[\\/]+$/, '');
    const idx = Math.max(trimmed.lastIndexOf('\\'), trimmed.lastIndexOf('/'));
    return idx > 0 ? trimmed.slice(0, idx) : trimmed;
}

function setMode(mode) {
    mode = mode || 'http';
    document.querySelectorAll('.mode-pill').forEach(pill => {
        const active = pill.dataset.mode === mode;
        pill.classList.toggle('active', active);
        const radio = pill.querySelector('input');
        if (radio) radio.checked = active;
    });
    currentMode = mode;
}

async function initHistory() {
    const prefs = await loadPrefsObject();
    const loaded = Array.isArray(prefs.history) ? prefs.history : [];
    downloadHistory = Array.isArray(loaded) ? loaded : [];
    let changed = false;
    downloadHistory = downloadHistory.map(item => {
        if (item && item.status === 'downloading') {
            changed = true;
            return { ...item, status: 'interrupted', finishedAt: new Date().toISOString() };
        }
        return item;
    });
    const beforeDedup = downloadHistory.length;
    downloadHistory = dedupeHistory(downloadHistory).slice(0, 20);
    if (downloadHistory.length !== beforeDedup) changed = true;
    if (changed) await persistHistory();
    renderHistory();
}

function historyKey(item) {
    const request = item?.request || {};
    const url = (request.url || item?.url || '').trim().toLowerCase();
    const output = (item?.output || '').trim().toLowerCase();
    return url + '|' + output;
}

function dedupeHistory(items) {
    const seen = new Set();
    const out = [];
    for (const item of (items || [])) {
        if (!item) continue;
        const key = historyKey(item);
        if (key !== '|' && seen.has(key)) continue;
        if (key !== '|') seen.add(key);
        out.push(item);
    }
    return out;
}

async function persistHistory() {
    downloadHistory = dedupeHistory(downloadHistory);
    downloadHistory = downloadHistory.slice(0, 20);
    try { await window.api_savePrefs(JSON.stringify({ history: downloadHistory })); } catch (_) {}
}

async function upsertHistory(entry) {
    const entryKey = historyKey(entry);
    let idx = downloadHistory.findIndex(x => x.id === entry.id);
    if (idx < 0 && entryKey !== '|') idx = downloadHistory.findIndex(x => historyKey(x) === entryKey);
    if (idx >= 0) downloadHistory[idx] = { ...downloadHistory[idx], ...entry };
    else downloadHistory.unshift(entry);
    await persistHistory();
    renderHistory();
}

function statusLabel(status) {
    const t = translations[currentLang] || translations['en'] || {};
    const labels = t.statusLabel || {};
    switch (status) {
        case 'completed': return labels.completed || 'Ukończone';
        case 'cancelled': return labels.cancelled || 'Anulowane';
        case 'interrupted': return labels.interrupted || 'Przerwane';
        case 'downloading': return labels.downloading || 'Pobieranie';
        case 'paused': return labels.paused || 'Pauza';
        case 'error': return labels.error || 'Błąd';
        default: return status || labels.unknown || 'Nieznany';
    }
}

function renderHistory() {
    if (!historyList) return;
    historyList.innerHTML = '';
    if (!downloadHistory.length) {
        const empty = document.createElement('div');
        empty.className = 'history-empty';
        empty.textContent = tt('historyEmpty', 'Brak historii.');
        historyList.appendChild(empty);
        return;
    }
    downloadHistory.forEach(item => {
        const row = document.createElement('div');
        row.className = 'history-item';

        const title = document.createElement('div');
        title.className = 'history-title';
        title.textContent = item.url || item.output || '-';
        row.appendChild(title);

        const meta = document.createElement('div');
        meta.className = 'history-meta';
        const when = item.finishedAt || item.startedAt || '';
        meta.innerHTML = '<span class="history-status">' + statusLabel(item.status) + '</span>' +
            (when ? new Date(when).toLocaleString() + ' · ' : '') +
            (item.pages ?? 0) + ' ' + tt('stat.pages', 'stron') + ' · ' + (item.assets ?? 0) + ' ' + tt('stat.assets', 'assetów') + ' · ' +
            formatBytes(item.bytes || 0) + '<br>' + (item.output || '');
        row.appendChild(meta);

        const actions = document.createElement('div');
        actions.className = 'mini-actions';

        if (['interrupted', 'cancelled', 'error', 'paused'].includes(item.status)) {
            const resume = document.createElement('button');
            resume.type = 'button';
            resume.className = 'mini-btn';
            resume.innerHTML = '<i class="fas fa-play"></i> ' + tt('btnResume', 'Wznów');
            resume.addEventListener('click', () => resumeHistoryItem(item));
            actions.appendChild(resume);
        }

        const openPage = document.createElement('button');
        openPage.type = 'button';
        openPage.className = 'mini-btn';
        openPage.innerHTML = '<i class="fas fa-external-link-alt"></i> ' + tt('btnOpenPage', 'Otwórz stronę');
        openPage.addEventListener('click', () => openLocalPage(item.indexPath || localIndexPath(item.output)));
        actions.appendChild(openPage);

        const reveal = document.createElement('button');
        reveal.type = 'button';
        reveal.className = 'mini-btn';
        reveal.innerHTML = '<i class="fas fa-search"></i> ' + tt('btnRevealFile', 'Pokaż plik');
        reveal.addEventListener('click', () => revealLocalPage(item.indexPath || localIndexPath(item.output)));
        actions.appendChild(reveal);

        const folder = document.createElement('button');
        folder.type = 'button';
        folder.className = 'mini-btn';
        folder.innerHTML = '<i class="fas fa-folder-open"></i> ' + tt('btnFolder', 'Folder');
        folder.addEventListener('click', async () => {
            try { if (item.output) await window.api_openFolder(item.output); } catch (e) { console.error(e); }
        });
        actions.appendChild(folder);

        const report = document.createElement('button');
        report.type = 'button';
        report.className = 'mini-btn';
        report.innerHTML = '<i class="fas fa-file-alt"></i> ' + tt('btnReport', 'Raport');
        report.addEventListener('click', () => openLocalPage(item.reportPath || ((item.output || '').replace(/[\\/]+$/, '') + ((item.output || '').includes('\\') ? '\\' : '/') + 'download-report.json')));
        actions.appendChild(report);

        const delBtn = document.createElement('button');
        delBtn.type = 'button';
        delBtn.className = 'mini-btn';
        delBtn.style.color = '#ff5252';
    delBtn.innerHTML = '<i class="fas fa-trash"></i> ' + tt('btnDelete', 'Usuń');
    delBtn.addEventListener('click', async () => {
        if (!confirm(tt('confirmDelete', 'Na pewno usunąć tę stronę wraz z plikami?'))) return;
        if (item.output) {
            const result = await window.api_deleteFolder(item.output).catch(error => 'error: ' + error);
            if (result !== 'ok') {
                alert(tt('error', 'Błąd:') + ' ' + result);
                addLog(result, 'error');
                return;
            }
        }
        await window.api_deleteHistoryItem(item.id || '');
            downloadHistory = downloadHistory.filter(x => x.id !== item.id);
            await persistHistory();
            renderHistory();
            addLog(tt('log.deleted', 'Usunięto:') + ' ' + (item.url || item.output || ''), 'error');
        });
        actions.appendChild(delBtn);

        row.appendChild(actions);
        historyList.appendChild(row);
    });
}

async function openLocalPage(path) {
    try { if (path) await window.api_openFile(path); } catch (e) { console.error(e); }
}

async function revealLocalPage(path) {
    try { if (path) await window.api_revealPath(path); } catch (e) { console.error(e); }
}

function applyHistoryItemToForm(item) {
    const request = item.request || {};
    const urlInput = document.getElementById('url');
    if (urlInput) urlInput.value = request.url || item.url || '';

    const depthInput = document.getElementById('depth');
    if (depthInput) depthInput.value = String(request.depth || item.depth || 2);

    const dlAll = document.getElementById('downloadAll');
    if (dlAll) dlAll.checked = typeof request.downloadAll === 'boolean' ? request.downloadAll : true;

    if (outputDirInput) outputDirInput.value = request.outputDir || item.outputBase || parentDir(item.output || '');

    setMode(request.mode || item.mode || 'http');

    const sub = document.getElementById('includeSubdomains');
    if (sub) sub.checked = !!request.includeSubdomains;
    const ext = document.getElementById('includeExternal');
    if (ext) ext.checked = !!request.includeExternal;
    const maxPages = document.getElementById('maxPages');
    if (maxPages) maxPages.value = String(request.maxPages || 0);
    const maxTotal = document.getElementById('maxTotalMB');
    if (maxTotal) maxTotal.value = String(request.maxTotalMB || 0);
    const maxFile = document.getElementById('maxFileMB');
    if (maxFile) maxFile.value = String(request.maxFileMB || 0);
    const cookie = document.getElementById('cookieHeader');
    if (cookie) cookie.value = request.cookie || '';
}

function resumeHistoryItem(item) {
    if (currentDownloadId && cancelBtn.style.display !== 'none') {
        addLog(tt('log.firstCancel', 'Najpierw anuluj aktualne pobieranie.'), 'error');
        return;
    }
    resumeHistoryId = item.id;
    resumeBaselinePending = {
        pages: Number(item.pages || 0),
        assets: Number(item.assets || 0),
        attachments: Number(item.attachments || 0),
        bytes: Number(item.bytes || 0),
    };
    applyHistoryItemToForm(item);
    addLog(tt('log.resuming', 'Wznawianie: {url}').replace('{url}', item.url || item.output || ''), 'downloading');
    if (form.requestSubmit) form.requestSubmit();
    else form.dispatchEvent(new Event('submit', { cancelable: true }));
}

async function restoreOptions() {
    try {
        const raw = await window.api_loadPrefs();
        if (!raw) return;
        const opts = JSON.parse(raw);
        const remCb = document.getElementById('rememberOptions');
        if (opts.rememberOptions === false) {
            if (remCb) remCb.checked = false;
            return;
        }
        if (opts.url) {
            const urlInput = document.getElementById('url');
            if (urlInput) urlInput.value = opts.url;
        }
        if (opts.depth) {
            const depthInput = document.getElementById('depth');
            if (depthInput) depthInput.value = opts.depth;
        }
        if (typeof opts.downloadAll === 'boolean') {
            const dlAll = document.getElementById('downloadAll');
            if (dlAll) dlAll.checked = opts.downloadAll;
        }
        if (opts.outputDir && outputDirInput) {
            outputDirInput.value = opts.outputDir;
        }
        if (opts.mode) {
            document.querySelectorAll('.mode-pill').forEach(pill => {
                pill.classList.toggle('active', pill.dataset.mode === opts.mode);
                const radio = pill.querySelector('input');
                if (radio) radio.checked = (pill.dataset.mode === opts.mode);
            });
            currentMode = opts.mode;
        }
        if (typeof opts.includeSubdomains === 'boolean') {
            const el = document.getElementById('includeSubdomains');
            if (el) el.checked = opts.includeSubdomains;
        }
        if (typeof opts.includeExternal === 'boolean') {
            const el = document.getElementById('includeExternal');
            if (el) el.checked = opts.includeExternal;
        }
        if (typeof opts.maxPages === 'number') {
            const el = document.getElementById('maxPages');
            if (el) el.value = String(opts.maxPages);
        }
        if (typeof opts.maxTotalMB === 'number') {
            const el = document.getElementById('maxTotalMB');
            if (el) el.value = String(opts.maxTotalMB);
        }
        if (typeof opts.maxFileMB === 'number') {
            const el = document.getElementById('maxFileMB');
            if (el) el.value = String(opts.maxFileMB);
        }
        if (typeof opts.cookie === 'string') {
            const el = document.getElementById('cookieHeader');
            if (el) el.value = opts.cookie;
        }
        if (remCb) remCb.checked = true;
        // Restore saved language
        if (opts.lang && translations[opts.lang]) {
            currentLang = opts.lang;
            updateLanguageSelectFlag();
            applyTranslations();
        }
    } catch (e) {
        console.warn('[prefs] restore failed:', e);
    }
}

async function initPreferences() {
    // Restore saved options before anything else populates defaults.
    await restoreOptions();

    // Default the output dir field to a sensible value once the Go side is ready.
    try {
        const p = await window.api_defaultOutputPath('');
        if (outputDirInput && !outputDirInput.value) outputDirInput.value = p;
    } catch (_) {}
}

initPreferences();
initHistory();

document.getElementById('rememberOptions')?.addEventListener('change', (e) => {
    if (e.target.checked) saveOptions();
    else saveRememberDisabled();
});

['url', 'depth', 'downloadAll', 'outputDir', 'maxPages', 'maxTotalMB', 'maxFileMB', 'cookieHeader'].forEach(id => {
    const el = document.getElementById(id);
    el?.addEventListener('change', saveOptions);
    el?.addEventListener('input', () => {
        if (id === 'url' || id === 'outputDir') saveOptions();
    });
});
['includeSubdomains', 'includeExternal'].forEach(id => {
    document.getElementById(id)?.addEventListener('change', saveOptions);
});

// Folder picker
pickFolderBtn?.addEventListener('click', async () => {
    try {
        const picked = await window.api_pickFolder();
        if (picked && outputDirInput) {
            outputDirInput.value = picked;
            saveOptions();
        }
    } catch (e) {
        console.error('pick folder failed', e);
    }
});

function buildCurrentRequest() {
    const opts = collectOptions();
    return {
        url: document.getElementById('url').value,
        outputDir: outputDirInput ? outputDirInput.value.trim() : '',
        depth: parseInt(document.getElementById('depth').value),
        downloadAll: !!opts.downloadAll,
        mode: getMode(),
        includeSubdomains: !!opts.includeSubdomains,
        includeExternal: !!opts.includeExternal,
        maxPages: opts.maxPages,
        maxTotalMB: opts.maxTotalMB,
        maxFileMB: opts.maxFileMB,
        cookie: opts.cookie || '',
    };
}

queueBtn.addEventListener('click', () => {
    const req = buildCurrentRequest();
    if (!req.url) return;
    downloadQueue.push(req);
    addLog(tt('log.queued', 'Dodano do kolejki: {url} (kolejka: {n})').replace('{url}', req.url).replace('{n}', downloadQueue.length), 'downloading');
    if (!currentDownloadId || cancelBtn.style.display === 'none') runNextQueued();
});

function applyRequestToForm(req) {
    document.getElementById('url').value = req.url || '';
    document.getElementById('depth').value = String(req.depth || 2);
    document.getElementById('downloadAll').checked = !!req.downloadAll;
    if (outputDirInput) outputDirInput.value = req.outputDir || '';
    setMode(req.mode || 'http');
    document.getElementById('includeSubdomains').checked = !!req.includeSubdomains;
    document.getElementById('includeExternal').checked = !!req.includeExternal;
    document.getElementById('maxPages').value = String(req.maxPages || 0);
    document.getElementById('maxTotalMB').value = String(req.maxTotalMB || 0);
    document.getElementById('maxFileMB').value = String(req.maxFileMB || 0);
    document.getElementById('cookieHeader').value = req.cookie || '';
}

function runNextQueued() {
    if (!downloadQueue.length) return;
    const next = downloadQueue.shift();
    applyRequestToForm(next);
    addLog(tt('log.queueStart', 'Start z kolejki: {url} (pozostało: {n})').replace('{url}', next.url).replace('{n}', downloadQueue.length), 'downloading');
    if (form.requestSubmit) form.requestSubmit();
    else form.dispatchEvent(new Event('submit', { cancelable: true }));
}

function setBusy(busy) {
    downloadBtn.disabled = busy;
    queueBtn.disabled = busy;
    downloadBtn.style.display = busy ? 'none' : 'inline-flex';
    queueBtn.style.display = busy ? 'none' : 'inline-flex';
    cancelBtn.style.display = busy ? 'inline-flex' : 'none';
    pauseBtn.style.display = busy ? 'inline-flex' : 'none';
    // Disable form inputs while a run is in flight.
    document.querySelectorAll('#downloadForm input, #downloadForm select').forEach(el => {
        if (el !== cancelBtn) el.disabled = busy;
    });
}

function setStatus(state, title) {
    const cls = 'status-icon ' + state;
    statusIcon.className = cls;
    const icon = state === 'downloading' ? 'spinner' :
                 state === 'success' ? 'check' :
                 state === 'error' ? 'times' :
                 state === 'cancelled' ? 'ban' :
                 state === 'paused' ? 'pause-circle' : 'spinner';
    statusIcon.innerHTML = '<i class="fas fa-' + icon + (state === 'downloading' ? ' fa-spin' : '') + '"></i>';
    statusTitle.textContent = title;
}

form.addEventListener('submit', async (e) => {
    e.preventDefault();

    const t = translations[currentLang] || translations['en'] || {};

    const url = document.getElementById('url').value;

    saveOptions(); // persist user preferences if "remember" is checked

    setBusy(true);
    setStatus('downloading', tt('statusTitle.downloading') || 'Downloading...');

    status.classList.add('active');
    statusUrl.textContent = url;

    outputPath.textContent = '-';
    currentBaseline = resumeBaselinePending || { pages: 0, assets: 0, attachments: 0, bytes: 0 };
    pageCount.textContent = String(currentBaseline.pages || 0);
    assetCount.textContent = String(currentBaseline.assets || 0);
    attachmentCount.textContent = String(currentBaseline.attachments || 0);
    bytesStat.textContent = formatBytes(currentBaseline.bytes || 0);
    log.innerHTML = '';
    openFolderBtn.style.display = 'none';
    currentActions.style.display = 'none';
    // reset progress-bar animation
    progressFill.style.animation = '';
    progressFill.style.width = '0%';
    progressText.textContent = '';

    const request = buildCurrentRequest();

    try {
        const result = await window.api_download(JSON.stringify(request));

        if (result.error) {
            addLog((t.error || 'Error:') + ' ' + result.error, 'error');
            setBusy(false);
            setStatus('error', tt('statusTitle.error') || 'Error!');
        return;
    }

        currentDownloadId = result.id;
        currentHistoryId = resumeHistoryId || result.id;
        resumeHistoryId = null;
        resumeBaselinePending = null;
        currentOutputPath = result.output;
        outputPath.textContent = result.output;
        openFolderBtn.style.display = 'inline-block';
        currentActions.style.display = 'flex';

        await upsertHistory({
            id: currentHistoryId,
            runId: result.id,
            url: url,
            output: result.output,
            outputBase: request.outputDir,
            indexPath: localIndexPath(result.output),
            request: request,
            status: 'downloading',
            startedAt: new Date().toISOString(),
            pages: currentBaseline.pages || 0,
            assets: currentBaseline.assets || 0,
            attachments: currentBaseline.attachments || 0,
            bytes: currentBaseline.bytes || 0,
        });

        addLog(tt('log.started') || 'Starting download...', 'downloading');

    } catch (error) {
        resumeHistoryId = null;
        resumeBaselinePending = null;
        addLog('Błąd: ' + error.message, 'error');
        setBusy(false);
        setStatus('error', tt('statusTitle.error') || 'Error!');
    }
});

// ---- Cancel button --------------------------------------------------
cancelBtn.addEventListener('click', async () => {
    if (!currentDownloadId) return;
    const t = translations[currentLang] || translations['en'] || {};
    cancelBtn.disabled = true;
    try {
        await window.api_cancel(currentDownloadId);
        await upsertHistory({ id: currentHistoryId || currentDownloadId, status: 'cancelled', finishedAt: new Date().toISOString() });
        addLog(tt('log.cancelled') || 'Cancelled by user', 'error');
        setBusy(false);
    } catch (e) {
        console.error('cancel failed', e);
    } finally {
        cancelBtn.disabled = false;
    }
});

pauseBtn.addEventListener('click', async () => {
    if (!currentDownloadId) return;
    try {
        if (!isPaused) {
            await window.api_pause(currentDownloadId);
            isPaused = true;
            pauseBtn.innerHTML = '<i class="fas fa-play"></i> <span>' + tt('btnResume', 'Wznów') + '</span>';
            setStatus('paused', tt('statusLabel.paused', 'Pauza'));
            await upsertHistory({ id: currentHistoryId || currentDownloadId, status: 'paused' });
            addLog(tt('log.paused', 'Pauza...'), 'downloading');
        } else {
            await window.api_resume(currentDownloadId);
            isPaused = false;
            pauseBtn.innerHTML = '<i class="fas fa-pause"></i> <span>' + tt('btnPause', 'Pauza') + '</span>';
            setStatus('downloading', tt('statusTitle.downloading', 'Downloading...'));
            await upsertHistory({ id: currentHistoryId || currentDownloadId, status: 'downloading' });
            addLog(tt('log.resumed', 'Wznowiono.'), 'downloading');
        }
    } catch (e) { console.error(e); }
});

// ---- Subscriptions --------------------------------------------------
listeners.progress.push((evt) => {
    const data = evt.payload || {};
    if (evt.id !== currentDownloadId) return;

    // Older/in-flight "downloading" events may contain zero-valued
    // counters. Never let those reset the totals already accumulated
    // by asset/progress events.
    const isDownloadingEvent = data.Status === 'downloading';
    const displayPages = Math.max(Number(pageCount.textContent || '0'), Number(currentBaseline.pages || 0), Number(data.Pages || 0));
    const displayAssets = Math.max(Number(assetCount.textContent || '0'), Number(currentBaseline.assets || 0), Number(data.Assets || 0));
    const displayAttachments = Math.max(Number(attachmentCount.textContent || '0'), Number(currentBaseline.attachments || 0), Number(data.Attachments || 0));
    const displayBytes = Math.max(Number(currentBaseline.bytes || 0), Number(data.Bytes || 0));
    if (!isDownloadingEvent || displayPages > 0) pageCount.textContent = String(displayPages);
    if (!isDownloadingEvent || displayAssets > 0) assetCount.textContent = String(displayAssets);
    if (!isDownloadingEvent || displayAttachments > 0) attachmentCount.textContent = String(displayAttachments);
    if (!isDownloadingEvent || displayBytes > 0) bytesStat.textContent = formatBytes(displayBytes);

    // Update the progress bar percentage when we know a MaxPages
    // limit was set.
    const maxPages = parseInt(document.getElementById('maxPages')?.value || '0', 10) || 0;
    const maxBytes = (parseInt(document.getElementById('maxTotalMB')?.value || '0', 10) || 0) * 1024 * 1024;
    if (maxPages > 0 && data.Pages !== undefined) {
        const pct = Math.min(100, Math.round((displayPages / maxPages) * 100));
        progressFill.style.animation = 'none';
        progressFill.style.width = pct + '%';
        progressText.textContent = pct + '%';
    } else if (maxBytes > 0 && data.Bytes !== undefined) {
        const pct = Math.min(100, Math.round((displayBytes / maxBytes) * 100));
        progressFill.style.animation = 'none';
        progressFill.style.width = pct + '%';
        progressText.textContent = pct + '%';
    }

    const ta = translations[currentLang] || translations['en'] || {};

    if (data.URL) {
        const isDownloading = data.Status === 'downloading';
        if (data.Status !== 'error') {
            const prefix = isDownloading ? (ta.log?.downloading || 'downloading:') : (ta.log?.downloaded || 'Downloaded:');
            const msg = prefix + ' ' + String(data.URL).substring(0, 60) + '...';
            addLog(msg, isDownloading ? 'downloading' : 'success');
        }
    }

    const idx = downloadHistory.findIndex(x => x.id === (currentHistoryId || currentDownloadId));
    if (idx >= 0) {
        downloadHistory[idx] = {
            ...downloadHistory[idx],
            pages: Number(pageCount.textContent || '0'),
            assets: Number(assetCount.textContent || '0'),
            attachments: Number(attachmentCount.textContent || '0'),
            bytes: data.Bytes !== undefined ? displayBytes : (downloadHistory[idx].bytes || 0),
        };
        renderHistory();
        const now = Date.now();
        if (now - lastHistoryProgressSave > 2500) {
            lastHistoryProgressSave = now;
            persistHistory();
        }
    }
});

listeners.asset.push((evt) => {
    if (evt.id !== currentDownloadId) return;
    const data = evt.payload || {};
    const ta = translations[currentLang] || translations['en'] || {};
    const isAttachment = data.kind === 'attachment';
    const msgKey = isAttachment ? 'log.attachment' : 'log.asset';
    const [grp, key] = msgKey.split('.');
    const prefix = (ta[grp] && ta[grp][key]) || (isAttachment ? 'Downloaded file:' : 'downloaded asset:');
    const msg = prefix + ' ' + String(data.url).substring(0, 60) + '...';
    addLog(msg, isAttachment ? 'attachment' : 'asset');
    if (isAttachment) {
        attachmentCount.textContent = String(Number(attachmentCount.textContent || '0') + 1);
    } else {
        assetCount.textContent = String(Number(assetCount.textContent || '0') + 1);
    }
});

listeners.complete.push((evt) => {
    if (evt.id !== currentDownloadId) return;
    const data = evt.payload || {};
    const t = translations[currentLang] || translations['en'] || {};

    if (data.Cancelled) {
        setStatus('cancelled', tt('statusTitle.cancelled') || 'Cancelled');
        addLog(tt('log.cancelled') || 'Cancelled by user', 'error');
    } else {
        setStatus('success', tt('statusTitle.complete') || 'Download complete!');
    }

    upsertHistory({
        id: currentHistoryId || currentDownloadId,
        status: data.Cancelled ? 'cancelled' : 'completed',
        finishedAt: new Date().toISOString(),
        reportPath: data.ReportPath || '',
        pages: Math.max(Number(pageCount.textContent || '0'), Number(currentBaseline.pages || 0), Number(data.Pages || 0)),
        assets: Math.max(Number(assetCount.textContent || '0'), Number(currentBaseline.assets || 0), Number(data.Assets || 0)),
        attachments: Math.max(Number(attachmentCount.textContent || '0'), Number(currentBaseline.attachments || 0), Number(data.Attachments || 0)),
        bytes: Math.max(Number(currentBaseline.bytes || 0), Number(data.Bytes || 0)),
    });

    setBusy(false);
    progressFill.style.animation = 'none';
    progressFill.style.width = '100%';
    progressText.textContent = '100%';
    pageCount.textContent = String(Math.max(Number(pageCount.textContent || '0'), Number(currentBaseline.pages || 0), Number(data.Pages || 0)));
    assetCount.textContent = String(Math.max(Number(assetCount.textContent || '0'), Number(currentBaseline.assets || 0), Number(data.Assets || 0)));
    attachmentCount.textContent = String(Math.max(Number(attachmentCount.textContent || '0'), Number(currentBaseline.attachments || 0), Number(data.Attachments || 0)));
    if (data.Bytes !== undefined) bytesStat.textContent = formatBytes(Math.max(Number(currentBaseline.bytes || 0), Number(data.Bytes || 0)));

    const completeMsg = (tt('log.complete') || 'Complete! Downloaded: {pages} pages, {assets} assets, {attachments} files.')
        .replace('{pages}', data.Pages || 0)
        .replace('{assets}', data.Assets || 0)
        .replace('{attachments}', data.Attachments || 0);
    addLog(completeMsg, data.Cancelled ? 'error' : 'success');
    isPaused = false;
    pauseBtn.innerHTML = '<i class="fas fa-pause"></i> <span>Pauza</span>';
    setTimeout(runNextQueued, 300);
});

listeners.error.push((evt) => {
    if (evt.id !== currentDownloadId) return;
    const data = evt.payload || {};
    const t = translations[currentLang] || translations['en'] || {};

    addLog((t.error || 'Error:') + ' ' + (data.error || ''), 'error');
});

openFolderBtn.addEventListener('click', async () => {
    if (currentOutputPath) {
        try { await window.api_openFolder(currentOutputPath); } catch (e) { console.error(e); }
    }
});

openPageBtn.addEventListener('click', () => openLocalPage(localIndexPath(currentOutputPath)));
revealPageBtn.addEventListener('click', () => revealLocalPage(localIndexPath(currentOutputPath)));
openReportBtn.addEventListener('click', () => openLocalPage((currentOutputPath || '').replace(/[\\/]+$/, '') + ((currentOutputPath || '').includes('\\') ? '\\' : '/') + 'download-report.json'));
clearHistoryBtn.addEventListener('click', async () => {
    downloadHistory = [];
    await persistHistory();
    renderHistory();
});

function addLog(message, type) {
    const item = document.createElement('div');
    item.className = 'log-item ' + type;
    item.textContent = '[' + new Date().toLocaleTimeString() + '] ' + message;
    log.insertBefore(item, log.firstChild);
    // Cap at 200 log entries so the page doesn't grow forever.
    while (log.children.length > 200) {
        log.removeChild(log.lastChild);
    }
}

function formatBytes(n) {
    if (!n || n < 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    let v = n;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return (i === 0 ? v.toFixed(0) : v.toFixed(1)) + ' ' + units[i];
}
