// The browser half of package wallet: it carries decisions the server already
// made to the person's own wallet (EIP-6963 discovery, EIP-1193 requests). It
// parses no amount, formats no money and writes no prose: every sentence is a
// data-say-* attribute of the [data-wallet] block (requirements §6.5).

const fail = (kind) => Object.assign(new Error(kind), { kind });
const rethrow = (e) => { throw e?.kind ? e : fail(e?.code === 4001 ? 'declined' : 'failed'); };
const same = (a, b) => String(a).toLowerCase() === String(b).toLowerCase();

export const hexUTF8 = (s) => '0x' + [...new TextEncoder().encode(s)].map((b) => b.toString(16).padStart(2, '0')).join('');

// discover collects the wallets that answer an EIP-6963 request within ms.
export function discover(win, ms = 300) {
  const found = [];
  const on = (e) => found.push(e.detail);
  win.addEventListener('eip6963:announceProvider', on);
  win.dispatchEvent(new Event('eip6963:requestProvider'));
  return new Promise((ok) => setTimeout(() => { win.removeEventListener('eip6963:announceProvider', on); ok(found); }, ms));
}

// holding returns the first wallet among found whose connected accounts hold
// address. eth_accounts asks nothing of the person: a wallet not yet connected
// to this page answers none, and the caller falls back to asking.
export async function holding(found, address) {
  for (const f of found) {
    const accounts = await f.provider.request({ method: 'eth_accounts' }).catch(() => []);
    if (accounts.some((a) => same(a, address))) return f.provider;
  }
}

// register signs the kernel's registration message, prefix + the address as
// the wallet returns it, which is also the address the form then sends.
export async function register(p, prefix) {
  const [address] = await p.request({ method: 'eth_requestAccounts' }).catch(rethrow);
  const signature = await p.request({ method: 'personal_sign', params: [hexUTF8(prefix + address), address] }).catch(rethrow);
  return { address, signature };
}

// fetchTx asks the server for the transfer, built from a fresh read of the
// registered address; 409 means nothing fundable is on file any more.
export async function fetchTx(fetchFn, amount, csrf) {
  const res = await fetchFn('/wallet/fund', {
    method: 'POST', credentials: 'same-origin', body: new URLSearchParams({ amount }),
    headers: { 'X-CSRF-Token': csrf, 'Content-Type': 'application/x-www-form-urlencoded' },
  });
  if (!res.ok) throw fail(res.status === 409 ? 'stale' : 'failed');
  return res.json();
}

// send puts the wallet on tx's chain and account, re-reading both immediately
// before the request, and returns the transaction hash: submission, not credit.
export async function send(p, tx) {
  const chain = () => p.request({ method: 'eth_chainId' });
  if (!same(await chain(), tx.chainId)) {
    await p.request({ method: 'wallet_switchEthereumChain', params: [{ chainId: tx.chainId }] })
      .catch((e) => { throw fail(e?.code === 4001 ? 'declined' : 'chain'); });
  }
  const [account] = await p.request({ method: 'eth_requestAccounts' }).catch(rethrow);
  if (!same(await chain(), tx.chainId)) throw fail('chain');
  if (!same(account, tx.from)) throw fail('account');
  return p.request({ method: 'eth_sendTransaction', params: [{ from: account, to: tx.to, data: tx.data, chainId: tx.chainId }] }).catch(rethrow);
}

async function bind(doc) {
  const root = doc.querySelector('[data-wallet]');
  if (!root) return;
  const { announce, openDialog } = await import('/static/app.js');
  for (const el of root.querySelectorAll('[data-wallet-js]')) el.hidden = false;
  // A review is rendered by a POST; replacing its URL makes a reload GET the
  // wallet, never the review again, so a reload can offer no second send.
  if (location.pathname !== '/wallet') history.replaceState(null, '', '/wallet');
  const near = (el) => el.closest('dialog') || root;
  const say = (kind, el) => {
    const box = near(el).querySelector('[data-wallet-say]');
    box.textContent = root.getAttribute('data-say-' + kind);
    box.hidden = false;
  };
  const pick = async (el, address) => {
    const found = await discover(window);
    const held = address && await holding(found, address);
    if (held || found.length < 2) return held || found[0]?.provider;
    const box = near(el).querySelector('[data-wallet-choose]');
    return new Promise((ok) => {
      box.replaceChildren(...found.map((f) => {
        const b = doc.createElement('button');
        Object.assign(b, { type: 'button', className: 'btn', textContent: f.info.name, onclick: () => { box.hidden = true; ok(f.provider); } });
        return b;
      }));
      box.hidden = false;
    });
  };
  root.addEventListener('click', async (e) => {
    const el = e.target.closest('[data-wallet-register], [data-wallet-send]');
    if (!el) return;
    e.preventDefault();
    for (const box of root.querySelectorAll('[data-wallet-say]')) box.hidden = true;
    el.disabled = true; // one flow per click: a second click cannot send twice
    try {
      if (el.hasAttribute('data-wallet-send')) {
        const csrf = doc.querySelector('meta[name="csrf-token"]').content;
        const tx = await fetchTx(fetch, el.dataset.amount, csrf);
        const p = await pick(el, tx.from);
        if (!p) throw fail('none');
        await send(p, tx);
        el.closest('dialog').close(); // stays disabled: this review is spent
        announce('Transaction submitted');
        return;
      }
      const p = await pick(el);
      const dlg = doc.getElementById('form-address');
      if (p) {
        const { address, signature } = await register(p, root.dataset.prefix);
        const form = dlg.querySelector('form[method="post"]');
        form.address.value = address;
        form.signature.value = signature;
        dlg.querySelector('[data-wallet-step]')?.remove();
      } else say('none', dlg);
      openDialog(dlg);
      dlg.querySelector('[name="current_password"]').focus();
      el.disabled = false;
    } catch (err) {
      el.disabled = false;
      say(err.kind || 'failed', el);
    }
  });
}

if (typeof document !== 'undefined') bind(document);
