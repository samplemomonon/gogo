// The publication gate is what keeps unapproved legal text out of a
// production build, so its behavior is pinned the same way the runtime gates
// are: an approved fixture pair passes, and each rule blocks on its own. Every
// case builds a throwaway copy of the script plus both documents under a
// temporary directory, so the checked-in drafts are never touched and no test
// depends on the repository's current publication state.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, copyFile, writeFile, rm } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptPath = fileURLToPath(new URL('./check-publication.mjs', import.meta.url));

const approvedFrontmatter = `---
publicationStatus: approved
documentOwner: "Pemilik Layanan (contoh uji)"
version: "1.0"
effectiveDate: "2026-10-19"
approvalReference: "REF-TEST-001"
---`;

const validBody = '\n\n# Dokumen uji\n\nIsi dokumen untuk pengujian gerbang publikasi.\n';
const approvedDocument = `${approvedFrontmatter}${validBody}`;

// runGate copies the real script and both documents into a temporary site
// tree, then runs the copy. Only the documents differ per case.
async function runGate({ terms = approvedDocument, privacy = approvedDocument } = {}) {
  const root = await mkdtemp(join(tmpdir(), 'legal-gate-'));
  try {
    await mkdir(join(root, 'scripts'), { recursive: true });
    await mkdir(join(root, 'docs'), { recursive: true });
    await copyFile(scriptPath, join(root, 'scripts', 'check-publication.mjs'));
    await writeFile(join(root, 'docs', 'syarat-ketentuan.md'), terms);
    await writeFile(join(root, 'docs', 'kebijakan-privasi.md'), privacy);
    const result = spawnSync(process.execPath, [join(root, 'scripts', 'check-publication.mjs')], { encoding: 'utf8' });
    return { status: result.status, stdout: result.stdout, stderr: result.stderr };
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

test('approved documents pass the gate', async () => {
  const { status, stdout, stderr } = await runGate();
  assert.equal(status, 0, stderr);
  assert.match(stdout, /Legal publication metadata is approved\./);
});

test('a draft document blocks the build', async () => {
  const { status, stderr } = await runGate({
    terms: `---
publicationStatus: draft
documentOwner: ""
version: ""
effectiveDate: ""
approvalReference: ""
---

# Syarat dan Ketentuan

**Draf internal — belum disetujui untuk publikasi.**
`,
  });
  assert.equal(status, 1);
  assert.match(stderr, /syarat-ketentuan\.md: publicationStatus must be approved/);
});

test('failures in both documents are reported together', async () => {
  const draft = `---\npublicationStatus: draft\n---\n\nBelum diisi.\n`;
  const { status, stderr } = await runGate({ terms: draft, privacy: draft });
  assert.equal(status, 1);
  assert.match(stderr, /syarat-ketentuan\.md: /);
  assert.match(stderr, /kebijakan-privasi\.md: /);
});

test('each required metadata key blocks on its own', async () => {
  for (const key of ['documentOwner', 'version', 'effectiveDate', 'approvalReference']) {
    const source = approvedFrontmatter.replace(new RegExp(`^${key}:.*$`, 'm'), `${key}: ""`) + validBody;
    const { status, stderr } = await runGate({ privacy: source });
    assert.equal(status, 1, `${key}: expected the build to be blocked`);
    assert.match(stderr, new RegExp(`kebijakan-privasi\\.md: ${key} is required`));
  }
});

test('effectiveDate must be a real calendar date', async () => {
  for (const date of ['2026-13-01', '01-02-2026', '2026-2-3', '2023-02-29']) {
    const source = approvedFrontmatter.replace(/^effectiveDate:.*$/m, `effectiveDate: "${date}"`) + validBody;
    const { status, stderr } = await runGate({ terms: source });
    assert.equal(status, 1, `${date}: expected the build to be blocked`);
    assert.match(stderr, /syarat-ketentuan\.md: effectiveDate must be a real date in YYYY-MM-DD format/);
  }
  for (const date of ['2026-10-19', '2024-02-29']) {
    const source = approvedFrontmatter.replace(/^effectiveDate:.*$/m, `effectiveDate: "${date}"`) + validBody;
    const { status, stderr } = await runGate({ terms: source });
    assert.equal(status, 0, `${date}: ${stderr}`);
  }
});

test('quoted metadata values are accepted', async () => {
  const source = `---
publicationStatus: 'approved'
documentOwner: 'Pemilik Layanan (contoh uji)'
version: '1.0'
effectiveDate: '2026-10-19'
approvalReference: 'REF-TEST-001'
---${validBody}`;
  const { status, stderr } = await runGate({ terms: source });
  assert.equal(status, 0, stderr);
});

test('leftover draft instructions block even with approved metadata', async () => {
  for (const marker of ['Draf internal', 'Informasi yang wajib dilengkapi']) {
    const source = `${approvedFrontmatter}\n\n# Dokumen uji\n\n${marker} — sisa instruksi.\n`;
    const { status, stderr } = await runGate({ privacy: source });
    assert.equal(status, 1, `${marker}: expected the build to be blocked`);
    assert.match(stderr, /kebijakan-privasi\.md: remove draft instructions after legal and business review/);
  }
});
