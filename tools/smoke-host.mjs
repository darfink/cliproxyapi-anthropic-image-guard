// Offline end-to-end check against the real host and a loopback-only Claude mock.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { copyFile, mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const binary = process.env.CLIPROXYAPI_BINARY ?? '/opt/homebrew/opt/cliproxyapi/bin/cliproxyapi';
const library = resolve(process.argv[2] ?? 'dist/anthropic-image-guard.dylib');
const png = 'iVBORw0KGgoAAAANSUhEUgAAB9EAAAACCAYAAADvqyztAAAACXBIWXMAAAPoAAAD6AG1e1JrAAAAcElEQVRoge3TQQ0AAAgDMeRMJkEiikjQ0ccMXLrKbJsGDDDAAAMMMMAAAwwwwAADDDDAAAMMMMAAAwwwwAADDDDAAAPRoEsER2CAAQYYYIABBhhggAEGGGCAAQYYYIABBhhggAEGGGCAAQYYYGC/wQGYFgdKlUg1AgAAAABJRU5ErkJggg==';
const seen = [];
const mock = createServer(async (req, res) => {
  try {
    assert.equal(req.url.split('?')[0], '/v1/messages');
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = JSON.parse(Buffer.concat(chunks));
    const images = [];
    const walk = value => {
      if (!value || typeof value !== 'object') return;
      if (value.type === 'image' && value.source?.type === 'base64') images.push(value.source);
      for (const child of Object.values(value)) {
        if (Array.isArray(child)) child.forEach(walk);
        else if (child && typeof child === 'object') walk(child);
      }
    };
    walk(body.messages);
    assert.equal(images.length, 21);
    for (const image of images) {
      assert.equal(image.media_type, 'image/png');
      const bytes = Buffer.from(image.data, 'base64');
      assert.equal(bytes.readUInt32BE(16), 2000);
      assert(bytes.readUInt32BE(20) <= 2000);
    }
    if (seen.length) assert.equal(images[0].data, seen[0].image);
    seen.push({ stream: Boolean(body.stream), image: images[0].data });
    const message = { id: 'msg_mock', type: 'message', role: 'assistant', model: 'claude-opus-5-5', content: [{ type: 'text', text: 'ok' }], stop_reason: 'end_turn', stop_sequence: null, usage: { input_tokens: 50, output_tokens: 1 } };
    if (!body.stream) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(message));
      return;
    }
    res.writeHead(200, { 'Content-Type': 'text/event-stream' });
    const event = (type, payload) => res.write(`event: ${type}\ndata: ${JSON.stringify({ type, ...payload })}\n\n`);
    event('message_start', { message: { ...message, content: [], stop_reason: null } });
    event('content_block_start', { index: 0, content_block: { type: 'text', text: '' } });
    event('content_block_delta', { index: 0, delta: { type: 'text_delta', text: 'ok' } });
    event('content_block_stop', { index: 0 });
    event('message_delta', { delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 1 } });
    event('message_stop', {});
    res.end();
  } catch (error) {
    res.writeHead(500, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: { message: error.message } }));
  }
});
mock.listen(0, '127.0.0.1');
await once(mock, 'listening');
const reservation = createServer();
reservation.listen(0, '127.0.0.1');
await once(reservation, 'listening');
const port = reservation.address().port;
await new Promise(resolve => reservation.close(resolve));
const temporary = await mkdtemp(join(tmpdir(), 'anthropic-image-guard-smoke-'));
let host;
let output = '';
try {
  const platform = process.platform === 'win32' ? 'windows' : process.platform;
  const arch = process.arch === 'x64' ? 'amd64' : process.arch;
  const pluginDir = join(temporary, 'plugins', platform, arch);
  await mkdir(pluginDir, { recursive: true });
  await copyFile(library, join(pluginDir, 'anthropic-image-guard' + library.slice(library.lastIndexOf('.'))));
  const config = {
    server: { host: '127.0.0.1', port },
    access: { 'api-keys': ['local-test-only'] },
    management: { 'disable-control-panel': true },
    oauth: { 'auth-dir': join(temporary, 'auth') },
    'api-keys': { claude: [{ name: 'local-mock', 'base-url': `http://127.0.0.1:${mock.address().port}`, models: [{ name: 'claude-opus-5-5', alias: 'guard-test' }], keys: [{ 'api-key': 'local-test-only' }] }] },
    plugins: { enabled: true, dir: join(temporary, 'plugins'), configs: { 'anthropic-image-guard': { enabled: true } } },
  };
  const configPath = join(temporary, 'config.yaml');
  await writeFile(configPath, JSON.stringify(config), { mode: 0o600 });
  host = spawn(binary, ['-config', configPath], { stdio: ['ignore', 'pipe', 'pipe'], cwd: temporary });
  const capture = chunk => { output = (output + chunk).slice(-32000); };
  host.stdout.on('data', capture);
  host.stderr.on('data', capture);
  host.on('error', capture);
  const headers = { Authorization: 'Bearer local-test-only', 'Content-Type': 'application/json' };
  let ready = false;
  for (let i = 0; i < 100; i++) {
    if (host.exitCode !== null || host.signalCode !== null) throw new Error(`Host exited: ${output}`);
    try {
      const response = await fetch(`http://127.0.0.1:${port}/v1/models`, { headers });
      if (response.ok && (await response.text()).includes('guard-test')) { ready = true; break; }
    } catch {}
    await sleep(100);
  }
  assert(ready, `Host did not start: ${output}`);
  for (const route of ['messages', 'responses']) {
    for (const stream of [false, true]) {
      const image = route === 'messages' ? { type: 'image', source: { type: 'base64', media_type: 'image/png', data: png } } : { type: 'input_image', image_url: 'data:image/png;base64,' + png };
      const content = [...Array.from({ length: 21 }, () => image), { type: route === 'messages' ? 'text' : 'input_text', text: 'local test only' }];
      const body = { model: 'guard-test', stream, max_tokens: 32, [route === 'messages' ? 'messages' : 'input']: [{ role: 'user', content }] };
      const response = await fetch(`http://127.0.0.1:${port}/v1/${route}`, { method: 'POST', headers, body: JSON.stringify(body), signal: AbortSignal.timeout(15000) });
      const text = await response.text();
      assert.equal(response.status, 200, `${route} stream=${stream}: ${text}\n${output}`);
      assert(text.includes('ok'), `Missing mock result: ${text}`);
    }
  }
  assert.equal(seen.length, 4);
  // The host can use upstream streaming internally for a non-streaming Responses call.
  assert(seen.filter(x => x.stream).length >= 2);
  for (const stream of [false, true]) {
    const body = { model: 'guard-test', stream, input: [{ role: 'user', content: [{ type: 'input_image', image_url: 'data:image/png;base64,%%%' }] }] };
    const response = await fetch(`http://127.0.0.1:${port}/v1/responses`, { method: 'POST', headers, body: JSON.stringify(body), signal: AbortSignal.timeout(15000) });
    assert.equal(response.status, 400);
    assert((await response.text()).includes('Anthropic image guard'));
  }
  assert.equal(seen.length, 4, 'Rejected requests must never reach the model endpoint.');
  console.log('PASS: stock host loaded the native plugin; Messages/Responses x streaming/non-streaming; 21 images resized; replay bytes stable; unsafe images rejected before execution; no external API calls.');
} finally {
  if (host && host.exitCode === null && host.signalCode === null) {
    host.kill('SIGTERM');
    await Promise.race([once(host, 'exit'), sleep(5000)]);
    if (host.exitCode === null && host.signalCode === null) { host.kill('SIGKILL'); await once(host, 'exit'); }
  }
  mock.closeAllConnections();
  await new Promise(resolve => mock.close(resolve));
  await rm(temporary, { recursive: true, force: true });
}
