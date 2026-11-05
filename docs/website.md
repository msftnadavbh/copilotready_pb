# Website design and source

The React website uses a dark neutral palette, purple accents, and a short journey from the intact GitHub mark toward the Copilot UI icon. It is an independent setup kit, not an official GitHub product. The animation stops after 3.2 seconds and can be replayed; reduced-motion users see a still illustration.

Official design references: [GitHub color](https://brand.github.com/foundations/color), [Copilot identity](https://brand.github.com/brand-identity/copilot), [logo guidance](https://brand.github.com/foundations/logo), [motion principles](https://brand.github.com/motion/principles), and [Octicons](https://primer.style/octicons/).

React, React DOM, Scheduler, and GitHub Octicons are used under their MIT licenses, included in docs/licenses. Dependencies and exact versions are recorded in package-lock.json. Icon marks remain the property of their owners.

This repository includes packaging/demo source only, with no hosted instance or release downloads. The demo retains relative download links for artifacts produced by `scripts/build_release.py`; they do not work until a maintainer builds and serves those artifacts. Do not treat the demo as an available release.

With Node 24+ and npm, run `npm ci --registry=https://registry.npmjs.org` then `npm run build:web`. This rebuilds `site/app.js`, `site/style.css`, and `site/index.html` from `website` using the pinned dependencies. Temporary rendering output is written to ignored `build/`. The optional `server.cjs` serves a packaged site; it does not provision hosting. See [validation](validation.md) for current checks and [local log reporting](download-monitoring.md) for response-count semantics.
