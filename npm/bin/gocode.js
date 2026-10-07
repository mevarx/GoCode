#!/usr/bin/env node
const { spawnSync } = require('child_process');
const fs = require('fs');
const path = require('path');

const targets = {
  'linux-x64': 'linux-amd64/gocode',
  'linux-arm64': 'linux-arm64/gocode',
  'darwin-x64': 'darwin-amd64/gocode',
  'darwin-arm64': 'darwin-arm64/gocode',
  'win32-x64': 'windows-amd64/gocode.exe',
  'win32-arm64': 'windows-arm64/gocode.exe',
};

const key = `${process.platform}-${process.arch}`;
const rel = targets[key];
if (!rel) {
  console.error(`gocode: unsupported platform ${key}`);
  process.exit(1);
}

const bin = path.join(__dirname, '..', 'vendor', rel);
if (!fs.existsSync(bin)) {
  console.error(`gocode: binary not found at ${bin}. Run "npm run fetch-binaries" from the npm/ directory first.`);
  process.exit(1);
}

const res = spawnSync(bin, process.argv.slice(2), { stdio: 'inherit' });
if (res.error) {
  console.error(`gocode: failed to launch: ${res.error.message}`);
  process.exit(1);
}
process.exit(res.status === null ? 1 : res.status);
