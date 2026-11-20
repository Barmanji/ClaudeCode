'use strict'

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const https = require('node:https')
const http = require('node:http')
const crypto = require('node:crypto')

const PROJECT = 'c-lawd-code'
const REPO = process.env.CLAWD_REPO || 'Barmanji/ClaudeCode'
const VERSION = require('../package.json').version
const RELEASE_BASE = (
  process.env.CLAWD_RELEASE_BASE || `https://github.com/${REPO}/releases/download`
).replace(/\/$/, '')

const GOOS = { win32: 'windows', linux: 'linux', darwin: 'darwin' }[process.platform]
const GOARCH = { x64: 'amd64', arm64: 'arm64' }[process.arch]

function cacheRoot() {
  if (process.env.CLAWD_CACHE_DIR) return process.env.CLAWD_CACHE_DIR
  if (process.platform === 'win32') {
    return path.join(process.env.LOCALAPPDATA || os.tmpdir(), PROJECT)
  }
  if (process.platform === 'darwin') {
    return path.join(os.homedir(), 'Library', 'Caches', PROJECT)
  }
  return path.join(process.env.XDG_CACHE_HOME || path.join(os.homedir(), '.cache'), PROJECT)
}

function fetchBuffer(url, redirects = 5) {
  return new Promise((resolve, reject) => {
    const client = url.startsWith('http://') ? http : https
    client
      .get(url, { headers: { 'User-Agent': `${PROJECT}/${VERSION}` } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          res.resume()
          if (redirects <= 0) {
            reject(new Error('too many redirects'))
            return
          }
          resolve(fetchBuffer(new URL(res.headers.location, url).toString(), redirects - 1))
          return
        }
        if (res.statusCode !== 200) {
          res.resume()
          reject(new Error(`GET ${url} returned HTTP ${res.statusCode}`))
          return
        }
        const chunks = []
        res.on('data', (chunk) => chunks.push(chunk))
        res.on('end', () => resolve(Buffer.concat(chunks)))
        res.on('error', reject)
      })
      .on('error', reject)
  })
}

async function ensureBinary() {
  if (!GOOS || !GOARCH) {
    throw new Error(
      `no prebuilt binary for ${process.platform}/${process.arch}. Use the curl installer or: go install github.com/${REPO}/cmd/clawd@latest`,
    )
  }

  const dir = path.join(cacheRoot(), `v${VERSION}`)
  const dest = path.join(dir, process.platform === 'win32' ? 'clawd.exe' : 'clawd')
  if (fs.existsSync(dest)) return dest

  const asset = `clawd_${GOOS}_${GOARCH}`
  const base = `${RELEASE_BASE}/v${VERSION}`
  process.stderr.write(`downloading ${PROJECT} v${VERSION} (${asset})…\n`)

  const [binary, checksums] = await Promise.all([
    fetchBuffer(`${base}/${asset}`),
    fetchBuffer(`${base}/checksums.txt`),
  ])

  const entry = checksums
    .toString('utf8')
    .split('\n')
    .map((line) => line.trim().split(/\s+/))
    .find((parts) => parts.length === 2 && parts[1] === asset)
  if (!entry) {
    throw new Error(`checksums.txt has no entry for ${asset}; try the curl installer instead`)
  }
  const actual = crypto.createHash('sha256').update(binary).digest('hex')
  if (actual !== entry[0]) {
    throw new Error('checksum mismatch; refusing to run the downloaded binary')
  }

  fs.mkdirSync(dir, { recursive: true })
  const tmp = `${dest}.${process.pid}.tmp`
  fs.writeFileSync(tmp, binary, { mode: 0o755 })
  fs.renameSync(tmp, dest)
  return dest
}

module.exports = { ensureBinary }
