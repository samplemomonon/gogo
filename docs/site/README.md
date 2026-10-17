# Customer legal pages

This isolated Rspress site statically generates the customer terms and privacy pages. It is a separate publication surface from the internal engineering documentation and the headless editorial CMS.

Run `npm ci` once, then `npm run dev` for local authoring. `npm run build` first checks publication metadata and blocks while either document is a draft. Production publication requires content owners to replace the draft text, verify it against actual service practices, obtain legal and business approval, set `publicationStatus: approved`, and provide `documentOwner`, `version`, `effectiveDate`, and `approvalReference`. This repository does not identify the legal entity, actual data flows, or contractual policies, so it does not invent those facts.

Rspress build output is `doc_build/`. Deployment should publish those files at the agreed customer-facing path using the existing static delivery or gateway hosting configuration. The deployment path and host are intentionally not guessed here.
