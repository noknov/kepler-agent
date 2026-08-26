# Architecture site

This directory is a self-contained, dependency-free bilingual architecture
guide. It is deliberately separate from the Go services and the Markdown
reference documentation under `docs/`.

## Preview locally

Open `index.html` directly, or serve this directory with any static file
server. No build step or package installation is required.

## Maintain the content

- `index.html` contains the Chinese and English articles. Keep matching
  sections aligned when architecture changes.
- `assets/architecture.css` owns the visual system and responsive layout.
- `assets/architecture.js` owns language selection and section highlighting.
- Source-code links target the `main` branch of this repository.

The GitHub Pages workflow publishes this directory only from the canonical
`noknov/kepler-agent` repository. Forks skip the deployment job even if
GitHub Actions is enabled.
