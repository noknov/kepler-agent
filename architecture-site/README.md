# Architecture site

This directory is a self-contained, dependency-free bilingual architecture
guide. It is deliberately separate from the Go services and the Markdown
reference documentation under `docs/`.

The page has independent language and architecture-version selectors:

- **v2** is the active architecture on `main`.
- **v1** is an archive of the final `v1-final` tag (`49380a51`), not a v2
  compatibility layer. Its source links deliberately target that tag.

## Preview locally

Open `index.html` directly, or serve this directory with any static file
server. No build step or package installation is required.

## Maintain the content

- `index.html` contains the Chinese and English v1/v2 articles. Keep matching
  language sections aligned, and update only the v2 article for changes on
  `main`. Keep v1 claims and links pinned to `v1-final`.
- `assets/architecture.css` owns the visual system and responsive layout.
- `assets/architecture.js` owns language/version selection and section
  highlighting. `?lang=zh|en&version=v1|v2` can select a view directly.

The GitHub Pages workflow publishes this directory only from the canonical
`noknov/kepler-agent` repository. Forks skip the deployment job even if
GitHub Actions is enabled.
