const { app, BrowserWindow, ipcMain, shell } = require('electron');
const path = require('path');
const cheerio = require('cheerio');
const fs = require('fs');
const https = require('https');
const http = require('http');

if (process.platform === 'win32') {
  require('child_process').execSync('chcp 65001 > nul', { stdio: 'ignore' });
}

console.log('Aplikacja uruchomiona');

let mainWindow;

function sleep(ms) {
    return new Promise(resolve => setTimeout(resolve, ms));
}

function getFilepath(baseDir, url) {
    const parsed = new URL(url);
    let filePath = parsed.pathname.replace(/^\//, '');
    
    if (!filePath || filePath.endsWith('/')) {
        filePath = filePath + 'index.html';
    }
    
    if (!filePath.endsWith('.html') && !filePath.endsWith('.htm')) {
        const ext = path.extname(filePath);
        if (!ext) {
            filePath = filePath + '/index.html';
        }
    }

    if (parsed.search) {
        const safeQuery = parsed.search.slice(1).replace(/[^a-zA-Z0-9._-]+/g, '_');
        const parsedPath = path.parse(filePath);
        filePath = path.join(parsedPath.dir, `${parsedPath.name}__${safeQuery}${parsedPath.ext || '.html'}`);
    }
    
    const fullPath = path.join(baseDir, filePath);
    return fullPath;
}

function getRelativePagePath(baseDir, fromUrl, toUrl) {
    const fromPath = getFilepath(baseDir, fromUrl);
    const toPath = getFilepath(baseDir, toUrl);
    const toParsed = new URL(toUrl);
    let relativePath = path.relative(path.dirname(fromPath), toPath).replace(/\\/g, '/');

    if (toParsed.hash) {
        relativePath += toParsed.hash;
    }

    return relativePath || './index.html';
}

function fetchUrl(url, timeout = 30000) {
    return new Promise((resolve, reject) => {
        const parsed = new URL(url);
        const client = parsed.protocol === 'https:' ? https : http;
        
        const req = client.get(url, {
            headers: {
                'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36'
            },
            timeout
        }, (res) => {
            if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
                const redirectUrl = new URL(res.headers.location, parsed).href;
                fetchUrl(redirectUrl, timeout).then(resolve).catch(reject);
                return;
            }
            
            const chunks = [];
            res.on('data', chunk => chunks.push(chunk));
            res.on('end', () => {
                resolve({
                    ok: res.statusCode >= 200 && res.statusCode < 300,
                    status: res.statusCode,
                    finalUrl: parsed.href,
                    headers: res.headers,
                    buffer: Buffer.concat(chunks),
                    text: () => Buffer.concat(chunks).toString('utf8')
                });
            });
        });
        
        req.on('error', reject);
        req.on('timeout', () => {
            req.destroy();
            reject(new Error('Timeout'));
        });
    });
}

async function downloadAsset(assetUrl, baseDir, baseUrl, win, downloadId) {
    try {
        const parsedAsset = new URL(assetUrl, baseUrl);
        const response = await fetchUrl(parsedAsset.href);
        
        if (!response.ok) return null;
        
        let assetPath;
        if (assetUrl.startsWith('http://') || assetUrl.startsWith('https://')) {
            const parsed = new URL(assetUrl);
            assetPath = parsed.pathname.replace(/^\//, '');
        } else {
            assetPath = assetUrl.replace(/^\//, '');
        }
        
        assetPath = assetPath.split('?')[0].split('#')[0];
        
        const fullPath = path.join(baseDir, assetPath);
        fs.mkdirSync(path.dirname(fullPath), { recursive: true });
        fs.writeFileSync(fullPath, response.buffer);
        
        return fullPath;
    } catch (error) {
        return null;
    }
}

function rewriteAssetPaths(html, baseDir, baseUrl) {
    const $ = cheerio.load(html);
    
    const assets = [];
    
    $('img[src]').each((i, el) => {
        const src = $(el).attr('src');
        if (src && !src.startsWith('data:') && !src.startsWith('http') && !src.startsWith('//')) {
            assets.push({ tag: 'img', attr: 'src', url: src });
        }
    });
    
    $('link[href]').each((i, el) => {
        const href = $(el).attr('href');
        if (href && !href.startsWith('http') && !href.startsWith('//') && href.endsWith('.css')) {
            assets.push({ tag: 'link', attr: 'href', url: href });
        }
    });
    
    $('script[src]').each((i, el) => {
        const src = $(el).attr('src');
        if (src && !src.startsWith('http') && !src.startsWith('//')) {
            assets.push({ tag: 'script', attr: 'src', url: src });
        }
    });
    
    $('source[src]').each((i, el) => {
        const src = $(el).attr('src');
        if (src && !src.startsWith('data:') && !src.startsWith('http') && !src.startsWith('//')) {
            assets.push({ tag: 'source', attr: 'src', url: src });
        }
    });
    
    $('video[src]').each((i, el) => {
        const src = $(el).attr('src');
        if (src && !src.startsWith('data:') && !src.startsWith('http') && !src.startsWith('//')) {
            assets.push({ tag: 'video', attr: 'src', url: src });
        }
    });
    
    $('video[poster]').each((i, el) => {
        const poster = $(el).attr('poster');
        if (poster && !poster.startsWith('data:') && !poster.startsWith('http') && !poster.startsWith('//')) {
            assets.push({ tag: 'video', attr: 'poster', url: poster });
        }
    });
    
    const uniqueAssets = [...new Set(assets.map(a => JSON.stringify(a)))].map(a => JSON.parse(a));
    
    return { $, uniqueAssets };
}

async function downloadPage(url, baseDir, baseUrl, depth, maxDepth, win, downloadId, downloadAll) {
    const parsedUrl = new URL(url);
    const domain = parsedUrl.hostname;
    
    if (depth > maxDepth) return { links: [], attachments: [], pageAssets: 0 };
    
    try {
        const response = await fetchUrl(url);
        
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }
        
        const html = response.text();
        
        const filepath = getFilepath(baseDir, url);
        fs.mkdirSync(path.dirname(filepath), { recursive: true });
        
        const { $, uniqueAssets } = rewriteAssetPaths(html, baseDir, baseUrl);
        
        for (const asset of uniqueAssets) {
            const assetUrl = new URL(asset.url, baseUrl).href;
            const downloadedPath = await downloadAsset(assetUrl, baseDir, baseUrl, win, downloadId);
            if (downloadedPath) {
                const relativePath = path.relative(path.dirname(filepath), downloadedPath);
                $(asset.tag).each((i, el) => {
                    if ($(el).attr(asset.attr) === asset.url) {
                        $(el).attr(asset.attr, relativePath.replace(/\\/g, '/'));
                    }
                });
                
                win.webContents.send('progress', {
                    id: downloadId,
                    asset: assetUrl,
                    status: 'asset'
                });
            }
        }
        
        const links = [];
        const attachments = [];
        const fileExtensions = ['.pdf', '.doc', '.docx', '.xls', '.xlsx', '.ppt', '.pptx', '.zip', '.rar', '.7z', '.tar', '.gz', '.mp3', '.mp4', '.avi', '.mov', '.jpg', '.jpeg', '.png', '.gif', '.svg', '.webp', '.ico'];
        
        $('a[href]').each((i, el) => {
            const href = $(el).attr('href');
            try {
                const fullUrl = new URL(href, baseUrl).href;
                const linkParsed = new URL(fullUrl);
                const ext = path.extname(linkParsed.pathname).toLowerCase();
                
                if (linkParsed.hostname === domain && !href.startsWith('mailto:') && !href.startsWith('tel:') && !href.startsWith('javascript:')) {
                    if (ext === '' || ext === '.html' || ext === '.htm' || linkParsed.search) {
                        $(el).attr('href', getRelativePagePath(baseDir, url, fullUrl));
                    }

                    if (linkParsed.hash && !linkParsed.search && (ext === '' || ext === '.html' || ext === '.htm')) {
                        return;
                    }

                    if (downloadAll && fileExtensions.includes(ext)) {
                        attachments.push(fullUrl);
                    } else if (!downloadAll || (ext === '' || ext === '.html' || ext === '.htm' || linkParsed.search)) {
                        links.push(fullUrl);
                    }
                }
            } catch (e) {}
        });

        fs.writeFileSync(filepath, $.html());
        
        const uniqueLinks = [...new Set(links)];
        const uniqueAttachments = [...new Set(attachments)];
        
        win.webContents.send('progress', {
            id: downloadId,
            url: url,
            status: 'completed',
            pages: uniqueLinks.length + 1,
            assets: uniqueAssets.length,
            attachments: uniqueAttachments.length
        });
        
        await sleep(500);
        
        return { links: uniqueLinks, attachments: uniqueAttachments, pageAssets: uniqueAssets.length };
        
    } catch (error) {
        win.webContents.send('progress', {
            id: downloadId,
            url: url,
            status: 'error',
            error: error.message
        });
        return { links: [], attachments: [], pageAssets: 0 };
    }
}

async function downloadRecursive(url, baseDir, depth, maxDepth, win, downloadId, baseUrl, downloadAll) {
    const toVisit = [[url, depth]];
    const visited = new Set();
    const downloadedAttachments = new Set();
    let pageCount = 0;
    let assetCount = 0;
    let attachmentCount = 0;
    
    while (toVisit.length > 0) {
        const [currentUrl, currentDepth] = toVisit.shift();
        
        if (visited.has(currentUrl) || currentDepth > maxDepth) continue;
        visited.add(currentUrl);
        
        win.webContents.send('progress', {
            id: downloadId,
            url: currentUrl,
            status: 'downloading',
            depth: currentDepth
        });
        
        const result = await downloadPage(currentUrl, baseDir, currentUrl, currentDepth, maxDepth, win, downloadId, downloadAll);
        
        const links = result.links;
        const attachments = result.attachments;
        const pageAssets = result.pageAssets || 0;
        pageCount++;
        assetCount += pageAssets;
        
        if (downloadAll && attachments.length > 0) {
            for (const attachUrl of attachments) {
                if (!downloadedAttachments.has(attachUrl)) {
                    downloadedAttachments.add(attachUrl);
                    const downloaded = await downloadAsset(attachUrl, baseDir, baseUrl, win, downloadId);
                    if (downloaded) {
                        attachmentCount++;
                        win.webContents.send('progress', {
                            id: downloadId,
                            asset: attachUrl,
                            status: 'attachment'
                        });
                    }
                }
            }
        }
        
        win.webContents.send('progress', {
            id: downloadId,
            url: currentUrl,
            status: 'done',
            count: pageCount,
            assets: assetCount,
            attachments: attachmentCount
        });
        
        console.log(`[PROGRESS] Strony: ${pageCount}, Assety: ${assetCount}, Zalaczniki: ${attachmentCount}`);
        
        if (currentDepth < maxDepth) {
            for (const link of links) {
                if (!visited.has(link)) {
                    toVisit.push([link, currentDepth + 1]);
                }
            }
        }
    }
    
    return { pageCount, assetCount, attachmentCount };
}

ipcMain.handle('download', async (event, { url, depth, downloadAll = true }) => {
    let parsedUrl;
    try {
        parsedUrl = new URL(url);
    } catch (e) {
        return { error: 'Invalid URL' };
    }
    
    const downloadId = Date.now().toString();
    const siteName = parsedUrl.hostname.replace(/^www\./, '');
    const outputDir = path.join(__dirname, 'output', siteName);
    
    console.log(`Rozpoczęto pobieranie: ${url}`);
    console.log(`Pobierz wszystkie pliki: ${downloadAll}`);
    
    downloadRecursive(url, outputDir, 0, depth, mainWindow, downloadId, url, downloadAll)
        .then((result) => {
            console.log(`Zakończono pobieranie: ${result.pageCount} stron, ${result.attachmentCount} załączników`);

            mainWindow.webContents.send('complete', {
                id: downloadId,
                count: result.pageCount,
                assets: result.assetCount,
                attachments: result.attachmentCount,
                output: outputDir
            });
        })
        .catch((error) => {
            console.error(`Błąd pobierania: ${error.message}`);
            mainWindow.webContents.send('error', { id: downloadId, error: error.message });
        });

    return { id: downloadId, output: outputDir };
});

ipcMain.handle('get-output-path', () => {
    return path.join(__dirname, 'output');
});

ipcMain.handle('open-folder', async (event, folderPath) => {
    await shell.openPath(folderPath);
});

function createWindow() {
    mainWindow = new BrowserWindow({
        width: 800,
        height: 700,
        minWidth: 600,
        minHeight: 500,
        webPreferences: {
            preload: path.join(__dirname, 'preload.js'),
            contextIsolation: true,
            nodeIntegration: false
        },
        backgroundColor: '#0f0f23',
        show: false
    });

    mainWindow.loadFile(path.join(__dirname, 'renderer', 'index.html'));

    mainWindow.once('ready-to-show', () => {
        mainWindow.show();
    });

    mainWindow.on('closed', () => {
        mainWindow = null;
    });
}

app.whenReady().then(() => {
    createWindow();

    app.on('activate', () => {
        if (BrowserWindow.getAllWindows().length === 0) {
            createWindow();
        }
    });
});

app.on('window-all-closed', () => {
    if (process.platform !== 'darwin') {
        app.quit();
    }
});
