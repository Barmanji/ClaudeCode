#!/usr/bin/env node
'use strict'

const fs = require('node:fs')
const path = require('node:path')
const { spawn } = require('node:child_process')

const PACKAGE = '@barmanji/clawd-code'
const DISPLAY = 'clawd-code'
const EXE = process.platform === 'win32' ? 'clawd.exe' : 'clawd'

function platformBinary() {
  const name = `${PACKAGE}-${process.platform}-${process.arch}`
  try {
    const manifest = require.resolve(`${name}/package.json`)
    const candidate = path.join(path.dirname(manifest), 'bin', EXE)
    if (fs.existsSync(candidate)) return candidate
  } catch (_) {
    // Optional dependency was skipped; fall through to the downloader.
  }
  return null
}

async function main() {
  const args = process.argv.slice(2)

  // Avoid a multi-megabyte download just to print the version.
  if (args.length === 1 && (args[0] === '--version' || args[0] === '-v')) {
    console.log(`${DISPLAY} ${require('../package.json').version}`)
    return
  }

  let binary = platformBinary()
  if (!binary) {
    const { ensureBinary } = require('../lib/download')
    binary = await ensureBinary()
  }

  const child = spawn(binary, args, { stdio: 'inherit' })
  child.on('error', (err) => {
    console.error(`clawd: failed to start ${binary}: ${err.message}`)
    process.exit(1)
  })
  child.on('exit', (code, signal) => {
    if (signal) {
      process.kill(process.pid, signal)
      return
    }
    process.exit(code == null ? 1 : code)
  })
}

main().catch((err) => {
  console.error(`clawd: ${err.message}`)
  process.exit(1)
})
