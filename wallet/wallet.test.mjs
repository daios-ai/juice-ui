// node --test wallet/wallet.test.mjs — wallet.mjs against a fake EIP-1193 provider.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { hexUTF8, discover, holding, register, fetchTx, send } from './wallet.mjs';

// provider answers each method from a table: a value, or a function of params;
// an Error-like {code} is thrown. calls records every request.
function provider(table) {
  const calls = [];
  return {
    calls,
    async request({ method, params }) {
      calls.push({ method, params });
      const v = typeof table[method] === 'function' ? table[method](params) : table[method];
      if (v && v.code) throw v;
      return v;
    },
  };
}

const tx = { from: '0xabc', to: '0xtoken', data: '0xa9059cbb', chainId: '0x89' };

test('hexUTF8 encodes UTF-8 bytes', () => {
  assert.equal(hexUTF8('a\nb'), '0x610a62');
});

test('discover collects announced providers', async () => {
  const win = new EventTarget();
  win.addEventListener('eip6963:requestProvider', () => {
    const e = new Event('eip6963:announceProvider');
    e.detail = { info: { name: 'W' }, provider: 'P' };
    win.dispatchEvent(e);
  });
  assert.deepEqual(await discover(win, 5), [{ info: { name: 'W' }, provider: 'P' }]);
});

test('register signs the prefix plus the address the wallet returned', async () => {
  const p = provider({ eth_requestAccounts: ['0xabc'], personal_sign: '0xsig' });
  assert.deepEqual(await register(p, 'pre\naddress: '), { address: '0xabc', signature: '0xsig' });
  assert.deepEqual(p.calls[1].params, [hexUTF8('pre\naddress: 0xabc'), '0xabc']);
});

test('register maps a refusal to declined', async () => {
  const p = provider({ eth_requestAccounts: { code: 4001 } });
  await assert.rejects(register(p, ''), { kind: 'declined' });
});

test('send switches the chain, re-checks it and returns the hash', async () => {
  let chain = '0x1';
  const p = provider({
    eth_chainId: () => chain,
    wallet_switchEthereumChain: () => { chain = '0x89'; return null; },
    eth_requestAccounts: ['0xABC'],
    eth_sendTransaction: '0xhash',
  });
  assert.equal(await send(p, tx), '0xhash');
  assert.deepEqual(p.calls.at(-1).params, [{ from: '0xABC', to: '0xtoken', data: '0xa9059cbb', chainId: '0x89' }]);
});

test('send refuses what it cannot vouch for', async () => {
  const base = { eth_chainId: '0x89', eth_requestAccounts: ['0xabc'], eth_sendTransaction: '0xhash' };
  const cases = [
    [{ eth_chainId: '0x1', wallet_switchEthereumChain: { code: 4902 } }, 'chain'],
    [{ eth_chainId: '0x1', wallet_switchEthereumChain: null }, 'chain'], // switched, still elsewhere
    [{ eth_requestAccounts: ['0xother'] }, 'account'],
    [{ eth_sendTransaction: { code: 4001 } }, 'declined'],
    [{ eth_sendTransaction: { code: -32000 } }, 'failed'],
  ];
  for (const [over, kind] of cases) {
    const p = provider({ ...base, ...over });
    await assert.rejects(send(p, tx), { kind });
    if (kind === 'chain' || kind === 'account') assert.ok(!p.calls.some((c) => c.method === 'eth_sendTransaction'));
  }
});

test('fetchTx posts the amount with the CSRF header and maps 409 to stale', async () => {
  let seen;
  const ok = async (url, init) => { seen = { url, init }; return { ok: true, json: async () => tx }; };
  assert.deepEqual(await fetchTx(ok, '10.00', 'T'), tx);
  assert.equal(seen.url, '/wallet/fund');
  assert.equal(seen.init.headers['X-CSRF-Token'], 'T');
  assert.equal(seen.init.body.get('amount'), '10.00');
  await assert.rejects(fetchTx(async () => ({ ok: false, status: 409 }), '1', 'T'), { kind: 'stale' });
  await assert.rejects(fetchTx(async () => ({ ok: false, status: 500 }), '1', 'T'), { kind: 'failed' });
});

test('holding picks the wallet already connected to the address, asking nothing', async () => {
  const a = provider({ eth_accounts: ['0xother'] });
  const b = provider({ eth_accounts: ['0xABC'] });
  const c = provider({ eth_accounts: { code: 4100 } });
  assert.equal(await holding([{ provider: c }, { provider: a }, { provider: b }], '0xabc'), b);
  assert.equal(await holding([{ provider: a }], '0xabc'), undefined);
  assert.ok([a, b, c].every((p) => p.calls.every((x) => x.method === 'eth_accounts')));
});
