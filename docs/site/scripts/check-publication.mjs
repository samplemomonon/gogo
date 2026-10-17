import { readFile } from 'node:fs/promises';

const documents = [
  'docs/syarat-ketentuan.md',
  'docs/kebijakan-privasi.md',
];

const failures = [];
for (const path of documents) {
  const source = await readFile(new URL(`../${path}`, import.meta.url), 'utf8');
  const frontmatter = source.match(/^---\s*\n([\s\S]*?)\n---\s*(?:\n|$)/)?.[1] ?? '';
  const metadata = Object.fromEntries(
    [...frontmatter.matchAll(/^([A-Za-z][A-Za-z0-9]*):\s*(.*?)\s*$/gm)].map(([, key, value]) => [key, value.replace(/^['"]|['"]$/g, '')]),
  );
  if (metadata.publicationStatus !== 'approved') failures.push(`${path}: publicationStatus must be approved`);
  for (const key of ['documentOwner', 'version', 'effectiveDate', 'approvalReference']) {
    if (!metadata[key]) failures.push(`${path}: ${key} is required`);
  }
  if (metadata.effectiveDate) {
    const parsedDate = new Date(`${metadata.effectiveDate}T00:00:00Z`);
    if (!/^\d{4}-\d{2}-\d{2}$/.test(metadata.effectiveDate) || Number.isNaN(parsedDate.valueOf()) || parsedDate.toISOString().slice(0, 10) !== metadata.effectiveDate) {
      failures.push(`${path}: effectiveDate must be a real date in YYYY-MM-DD format`);
    }
  }
  if (/Draf internal|Informasi yang wajib dilengkapi/.test(source)) {
    failures.push(`${path}: remove draft instructions after legal and business review`);
  }
}

if (failures.length) {
  console.error('Legal site build blocked:');
  for (const failure of failures) console.error(`- ${failure}`);
  process.exitCode = 1;
} else {
  console.log('Legal publication metadata is approved.');
}
