import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';

const rates = { input: 0.6, output: 1.4, cacheRead: 0.06, cacheWrite: 0.1 };
const cost = { input: rates.input / 1e6 * 50, output: rates.output / 1e6 * 5, cacheRead: rates.cacheRead / 1e6 * 40, cacheWrite: rates.cacheWrite * 10 / 1e6 };
const estimate = cost.input + cost.output + cost.cacheRead + cost.cacheWrite;
const cases = [
  { name: 'reported', provider: 'openrouter', field: '0.123', total: 0.123 },
  { name: 'free', provider: 'openrouter', field: '0', total: 0 },
  { name: 'missing', provider: 'openrouter', total: estimate },
  { name: 'null', provider: 'openrouter', field: 'null', total: estimate },
  { name: 'not-a-number', provider: 'openrouter', field: '"0.123"', total: estimate },
  { name: 'overflow', provider: 'openrouter', field: '1e9999', total: estimate },
  { name: 'other-provider', provider: 'openai', field: '0.123', total: estimate },
  { name: 'custom-openrouter-endpoint', provider: 'custom', baseUrl: 'https://openrouter.ai/api/v1', field: '0.123', total: 0.123 }
].map(({ field, total, ...entry }) => ({
  ...entry,
  usage: `{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":40,"cache_write_tokens":10},"completion_tokens_details":{"reasoning_tokens":2}${field === undefined ? '' : `,"cost":${field}`}}`,
  expectedCost: { ...cost, total }
}));
const destination = process.argv[2] ?? path.join(import.meta.dirname, '../../ai/api/testdata/openrouter-usage.json');
await mkdir(path.dirname(destination), { recursive: true });
await writeFile(destination, JSON.stringify({
  owner: 'Orb',
  generator: 'conformance/extract/orb-openrouter-usage.mjs',
  modelCost: rates,
  cases
}, null, 2) + '\n');
