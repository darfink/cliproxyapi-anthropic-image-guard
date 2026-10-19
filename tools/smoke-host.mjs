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
const args = process.argv.slice(2);
const huge = args.includes('--huge');
const large = huge || args.includes('--large');
assert(args.every(arg => !arg.startsWith('--') || ['--large', '--huge'].includes(arg)), 'Unknown option');
const library = resolve(args.find(arg => !arg.startsWith('--')) ?? 'dist/anthropic-image-guard.dylib');
const testText = 'local test only';
const timeout = huge ? 180000 : large ? 90000 : 15000;
const png = 'iVBORw0KGgoAAAANSUhEUgAAB9EAAAACCAYAAADvqyztAAAACXBIWXMAAAPoAAAD6AG1e1JrAAAAcElEQVRoge3TQQ0AAAgDMeRMJkEiikjQ0ccMXLrKbJsGDDDAAAMMMMAAAwwwwAADDDDAAAMMMMAAAwwwwAADDDDAAAPRoEsER2CAAQYYYIABBhhggAEGGGCAAQYYYIABBhhggAEGGGCAAQYYYGC/wQGYFgdKlUg1AgAAAABJRU5ErkJggg==';
// Already-small lossless WebP, also covered by the Go format tests. Padding
// exercises a large wire payload without large pixel allocations or resizing.
const webp = Buffer.from('UklGRiQAAABXRUJQVlA4TBcAAAAvf8EFEAdQwaJUuoBhABLC//1SRP9TPwA=', 'base64');
const encoded = large ? Buffer.concat([webp, Buffer.alloc((1 << 20) / 4 * 3 - webp.length)]).toString('base64') : png;
const media = large ? 'image/webp' : 'image/png';
const count = huge ? 200 : large ? 65 : 21;
const retainedCount = huge ? 13 : large ? 10 : 21;
const expectedCalls = huge ? 1 : 4;
const seen = [];
const mock = createServer(async (req, res) => {
  try {
    assert.equal(req.url.split('?')[0], '/v1/messages');
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const raw = Buffer.concat(chunks);
    assert(raw.length <= 32_000_000, `Provider's 32 MB request cap exceeded: ${raw.length}`);
    const body = JSON.parse(raw);
    assert.equal(body.messages[0].content.at(-1).text, testText, 'Text changed during interception or translation');
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
    assert.equal(images.length, retainedCount);
    if (large) assert(raw.includes(Buffer.from('[Earlier image omitted to keep this request within image limits.]')));
    for (const image of images) {
      assert.equal(image.media_type, media);
      if (large) {
        assert.equal(image.data, encoded, 'Retained image bytes changed');
        continue;
      }
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
  for (const route of huge ? ['responses'] : ['messages', 'responses']) {
    for (const stream of huge ? [true] : [false, true]) {
      const image = route === 'messages' ? { type: 'image', source: { type: 'base64', media_type: media, data: encoded } } : { type: 'input_image', image_url: 'data:' + media + ';base64,' + encoded };
      const content = [...Array.from({ length: count }, () => image), { type: route === 'messages' ? 'text' : 'input_text', text: testText }];
      const body = { model: 'guard-test', stream, max_tokens: 32, [route === 'messages' ? 'messages' : 'input']: [{ role: 'user', content }] };
      const payload = JSON.stringify(body);
      if (large) assert(Buffer.byteLength(payload) > 64 << 20);
      if (huge) assert(Buffer.byteLength(payload) * 4 / 3 > 256 << 20, 'Must exceed the old RPC-envelope cap');
      const response = await fetch(`http://127.0.0.1:${port}/v1/${route}`, { method: 'POST', headers, body: payload, signal: AbortSignal.timeout(timeout) });
      const text = await response.text();
      assert.equal(response.status, 200, `${route} stream=${stream}: ${text}\n${output}`);
      assert(text.includes('ok'), `Missing mock result: ${text}`);
    }
  }
  assert.equal(seen.length, expectedCalls);
  // The host can use upstream streaming internally for a non-streaming Responses call.
  assert(seen.filter(x => x.stream).length >= (huge ? 1 : 2));
  for (const stream of [false, true]) {
    const body = { model: 'guard-test', stream, input: [{ role: 'user', content: [{ type: 'input_image', image_url: 'data:image/png;base64,%%%' }] }] };
    const response = await fetch(`http://127.0.0.1:${port}/v1/responses`, { method: 'POST', headers, body: JSON.stringify(body), signal: AbortSignal.timeout(timeout) });
    assert.equal(response.status, 400);
    assert((await response.text()).includes('Anthropic image guard: inline image has invalid base64 data'));
  }
  assert.equal(seen.length, expectedCalls, 'Rejected requests must never reach the model endpoint.');
  console.log(`PASS: ${large ? `>${huge ? 199 : 64} MiB image history reduced from ${count} to ${retainedCount} images; ` : '21 images resized; '}stock native host; ${huge ? 'Responses streaming, >256 MiB RPC' : 'Messages/Responses x streaming/non-streaming'}; provider 32 MB cap enforced; replay bytes stable; text unchanged; unsafe images rejected; no external API calls.`);
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
