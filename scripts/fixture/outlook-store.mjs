// Synthetic OWA records only; the fake origin is intercepted in a fresh signed-out browser.
import { readdir } from 'node:fs/promises';
import path from 'node:path';

export const origin = 'https_outlook.office.com_0.indexeddb';

export async function buildOutlookProfile(chromium, profileDir, variant = 'mail') {
  const ctx = await chromium.launchPersistentContext(profileDir, { channel: 'msedge', headless: true });
  let version = '';
  try {
    version = ctx.browser()?.version() ?? '';
    await ctx.route('**/*', route => {
      if (route.request().url().startsWith('https://outlook.office.com/')) return route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><title>Synthetic OWA fixture</title>' });
      return route.abort();
    });
    const page = await ctx.newPage();
    await page.goto('https://outlook.office.com/fixture');
    await page.evaluate(async variant => {
      const id = Id => ({ Id });
      const mailbox = (Name, EmailAddress) => ({ Name, EmailAddress });
      const folders = ['inbox', 'archive', 'drafts', 'sent', 'deleted', 'junk', 'custom'].map((type, i) => ({ id: `Folder/${i}==`, parentFolderId: 'Root/Case==', displayName: `Synthetic ${type}`, distinguishedFolderType: type }));
      const header = (name, extra = {}) => ({ ItemId: id(`Message/${name}==`), ParentFolderId: id('Folder/0=='), Subject: `Synthetic ${name}`, HasAttachments: false, ...extra });
      const messages = [
        header('Normal', { IsRead: true, Flag: { FlagStatus: 'NotFlagged' }, Importance: 'NORMAL', From: { Mailbox: mailbox('Synthetic Sender', 'sender@example.invalid') }, DateTimeReceived: '2026-10-01T10:00:00Z', DateTimeSent: '2026-10-01T09:00:00Z', InternetMessageId: '<normal@example.invalid>', UID: 'SYNTHETIC-UID', ItemClass: 'IPM.Note', ConversationId: id('Synthetic/Conversation=='), ToRecipients: [mailbox('To One', 'one@example.invalid'), mailbox('To Two', 'two@example.invalid')], CcRecipients: [mailbox('Cc', 'cc@example.invalid')], BccRecipients: [mailbox('Bcc', 'bcc@example.invalid')], ReplyTo: [mailbox('Not a recipient', 'reply@example.invalid')], Attachments: [{ AttachmentId: id('Attachment/Inline=='), Name: 'synthetic.png', IsInline: true, ContentType: 'image/png' }] }),
        header('Blob', { IsRead: false, Flag: { FlagStatus: 'Flagged' }, HasAttachments: true }),
        header('Sparse'),
        header('Null', { IsRead: null }),
        header('Undefined', { IsRead: undefined, Flag: { FlagStatus: 'COMPLETE' } }),
      ];
      let state = 19;
      let large = '<p>Synthetic spill ';
      for (let i = 0; i < 800000; i++) {
        state = (Math.imul(state, 1664525) + 1013904223) >>> 0;
        large += String.fromCharCode(65 + (state >>> 24) % 26);
      }
      large += '</p>';
      const bodies = [
        { ItemId: id('Message/Normal=='), NormalizedBody: { Value: '<p>Synthetic normal cached HTML</p>' } },
        { ItemId: id('Message/Blob=='), NormalizedBody: { Value: large }, IsTruncated: true },
        { ItemId: id('Message/Null=='), Body: null },
        { ItemId: id('Message/Undefined=='), Body: undefined, NormalizedBody: { Value: '<p>Synthetic cached truncated HTML</p>' }, IsTruncated: true },
      ];
      async function writeDB(name, withBodies = true) {
        const db = await new Promise((resolve, reject) => {
          const req = indexedDB.open(name, 1);
          req.onupgradeneeded = () => {
            for (const store of ['folders', 'messages', ...(withBodies ? ['messageBodies'] : []), 'credentials', 'events', 'calendar', 'attachmentPayloads']) req.result.createObjectStore(store);
          };
          req.onsuccess = () => resolve(req.result);
          req.onerror = () => reject(new Error('synthetic database open failed'));
        });
        await new Promise((resolve, reject) => {
          const tx = db.transaction([...db.objectStoreNames], 'readwrite');
          folders.forEach((f, i) => tx.objectStore('folders').put(f, `folder-${i}`));
          if (variant !== 'empty') messages.forEach((m, i) => tx.objectStore('messages').put(m, `message-${i}`));
          if (withBodies && variant !== 'empty') bodies.forEach((b, i) => tx.objectStore('messageBodies').put(b, `body-${i}`));
          // Host objects would produce an omission if the mail collector ever decoded these stores.
          tx.objectStore('credentials').put(new DOMException('Synthetic auth decoy only', 'SyntheticDecoy'), 'auth-decoy');
          tx.objectStore('events').put(new DOMException('Synthetic event decoy only'), 'event-decoy');
          tx.objectStore('calendar').put({ subject: 'Synthetic excluded calendar' }, 'calendar-decoy');
          tx.objectStore('attachmentPayloads').put(new Uint8Array([19, 23, 29]), 'payload-decoy');
          tx.oncomplete = resolve;
          tx.onerror = () => reject(new Error('synthetic transaction failed'));
          tx.onabort = () => reject(new Error('synthetic transaction aborted'));
        });
        db.close();
      }
      await writeDB(variant === 'unknown' ? 'not-owa-synthetic' : 'owa-offline-data-synthetic', variant !== 'missing-store');
      if (variant === 'multiple') await writeDB('owa-offline-data-second-synthetic');
    }, variant);
  } finally {
    await ctx.close();
  }
  const found = [];
  async function walk(dir) {
    for (const e of await readdir(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name);
      if (e.isDirectory()) {
        if (e.name.endsWith('.indexeddb.leveldb')) found.push(p);
        else await walk(p);
      }
    }
  }
  await walk(profileDir);
  if (found.length !== 1 || path.basename(found[0]) !== origin + '.leveldb') throw new Error('synthetic OWA origin was not written');
  return { version, leveldb: found[0] };
}
