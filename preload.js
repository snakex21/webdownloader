const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('api', {
    download: (url, depth, downloadAll) => ipcRenderer.invoke('download', { url, depth, downloadAll }),
    getOutputPath: () => ipcRenderer.invoke('get-output-path'),
    openFolder: (path) => ipcRenderer.invoke('open-folder', path),
    onProgress: (callback) => ipcRenderer.on('progress', (event, data) => callback(data)),
    onComplete: (callback) => ipcRenderer.on('complete', (event, data) => callback(data)),
    onError: (callback) => ipcRenderer.on('error', (event, data) => callback(data))
});
