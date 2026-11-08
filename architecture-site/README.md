# Architecture documentation

This static Chinese site describes the shared execution core and the product
boundaries of Slack, Web, and the local CLI. Open `index.html` directly or serve
this directory with a static HTTP server. The site has no runtime package,
external font, remote renderer, or build-service dependency.

## Content ownership

The chapter order follows system boundaries rather than review history:

| Group | Page | Owns |
| --- | --- | --- |
| Foundations | `index.html` | Component responsibilities and product boundaries |
| Foundations | `reading-guide.html` | Concept definitions and identifier scope |
| Execution | `execution.html` | Admission, execution phases and termination |
| Execution | `context.html` | Prompt layers, projection, budgets, compaction and evidence |
| Execution | `models.html` | Provider protocols, network attempts and retry conditions |
| Execution | `tools.html` | Discovery, effects, authorization, scheduling and results |
| State | `reliability.html` | Canonical storage, ownership and recovery windows |
| State | `memory.html` | Explicit memory identity, versions, retrieval and deletion |
| Coordination | `delegation.html` | Child-task contracts, isolation and acceptance |
| Coordination | `operations.html` | Durable queue claims, admission and shutdown |
| Products | `slack.html`, `web.html`, `local.html` | Product-specific boundaries |
| Diagnostics | `observability.html` | Projection, health, metrics, traces and their limits |
| Diagnostics | `production.html` | Symptom-to-evidence diagnosis and recovery decisions |
| Reference | `reference.html` | Implementation and validation pointers |

The overview maps components; it does not redefine each chapter's workflow.
Context and memory have separate owners: history projection does not replace
cross-session memory, and memory deletion does not delete a transcript. Queue
claims and canonical event recovery also have separate ownership. Observation
reports state; diagnosis uses it without granting authority to derived views.

The Markdown guides under `docs/` own setup, configuration and operating commands.
The site explains architecture rather than maintaining a second deployment runbook.

Source citations pin application revision
`35995459c70ec3059e15fbbb7e68119f360af74d`. Later commits in the documentation branch
change the site rather than the application behavior described. When revising a
behavior claim, verify the owning source and tests, then update the affected
citations and this baseline. A current path existing is not proof that every
statement at that path is correct; the validator checks references, not semantics.

## Technical writing

Apply the useful principles of ASD-STE100 to Chinese technical prose: consistent
terms, explicit actors, short sentences, one subject per paragraph, and conditions
beside the affected action. This is an adaptation of writing principles, not a
claim of English STE conformity or certification.

Use a neutral site title. Do not add a product name, logo, monogram, branded
favicon or decorative identity mark to the published pages. Keep actual source
URLs and technical identifiers accurate.

Use functional headings and concrete descriptions. Exclude slogans, rhetorical
questions, reader coaching, review history, and prose about the writing process
from the architecture pages. Preserve code identifiers. Distinguish implemented
behavior, design requirements, verified outcomes, and unresolved limits.

The reading-layout reference is [御舆](https://lintsinghua.github.io/). Its public
source was inspected for chapter grouping and long-form navigation. The architecture
content is based on the repository, not copied from that site's product implementation.
Writing principles refer to [ASD-STE100](https://www.asd-ste100.org/).

## Site maintenance

`assets/guide.css` owns layout and both themes. `assets/architecture.js` owns
navigation, section anchors, the page contents, search, dialogs and theme choice.
Static HTML includes chapter links and all content so navigation and reading work
without JavaScript. Search uses a generated section index; it does not fetch pages
or call an external search service.

After changing HTML, run from the repository root:

```sh
python3 architecture-site/scripts/build-search-index.py
python3 architecture-site/scripts/validate-site.py
python3 -m unittest discover -s architecture-site/scripts -p 'test_*.py'
```

The Python tools use only the standard library. Validation checks document
structure, assets, cross-page anchors, pinned source paths and index freshness.
Regression tests ensure broken links, duplicate anchors, missing assets and
unpinned source citations fail the checker.

Optional browser regression checks use an externally installed Playwright module
and Chromium. Neither is a site runtime dependency:

```sh
PLAYWRIGHT_MODULE=/absolute/path/to/playwright-core \
CHROMIUM_PATH=/absolute/path/to/chromium \
node architecture-site/scripts/browser-check.cjs
```

The checker starts and stops its own loopback server. It covers every page at
five widths, both themes, search and focus, mobile navigation, storage failure,
JavaScript-disabled reading and print layout. `SITE_SCREENSHOTS=/existing/path`
optionally saves screenshots. `CHROMIUM_NO_SANDBOX=1` is only for test containers
whose Chromium requires it; do not assume it is needed on a developer machine.

## CI and publication

`architecture-pages` validates site changes on branch pushes and pull requests.
Feature branches validate only. The deployment job runs on `main`, after the
validation job succeeds, and retains the existing `github-pages` environment
protection. Pushing a feature branch or tag does not publish it.

After review and merge to `main`, relevant changes trigger validation and Pages
publication. Manual runs on `main` are also supported. If the environment rejects
`main`, an administrator must review the Pages environment rules; do not change
refs or permissions to bypass them. A successful validation is distinct from a
successful deployment. Check the deployment job before claiming the site is live.
