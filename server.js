const express = require('express');
const path = require('path');
const { spawn } = require('child_process');

const app = express();
const port = 3000;

app.use(express.json());
app.use(express.static(path.join(__dirname, 'public')));

app.post('/api/download', (req, res) => {
    const { url, output = 'output', depth = 2 } = req.body;

    if (!url) {
        return res.status(400).json({ error: 'URL is required' });
    }

    const python = process.platform === 'win32' ? 'python' : 'python3';
    const child = spawn(python, [path.join(__dirname, 'webdownloader.py'), url, output, String(depth)], {
        cwd: __dirname
    });

    let stdout = '';
    let stderr = '';

    child.stdout.on('data', (data) => {
        stdout += data.toString();
    });

    child.stderr.on('data', (data) => {
        stderr += data.toString();
    });

    child.on('close', (code) => {
        if (code === 0) {
            res.json({ ok: true, output, log: stdout.trim() });
        } else {
            res.status(500).json({ error: stderr || stdout || `Process exited with code ${code}` });
        }
    });
});

app.listen(port, () => {
    console.log(`Web downloader running at http://localhost:${port}`);
});
