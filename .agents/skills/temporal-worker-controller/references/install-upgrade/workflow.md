# Install and upgrade workflow

Use this area for TWC installation, upgrades, rollbacks, cert-manager setup or migration, webhook TLS, and failures during those operations.

## Route within this area

| Task | Read |
| --- | --- |
| Understand the chart and webhook relationships | [Concepts](concepts.md) |
| Install TWC on a cluster | [Install](install.md), then the relevant TLS path |
| Install or verify cert-manager for TWC | [cert-manager](cert-manager.md) |
| Upgrade or roll back TWC | [Upgrade](upgrade.md) |
| Upgrade from a release with `certmanager.install: true` | [Subchart migration](cert-manager-subchart-migration.md) before upgrading |
| Configure or change webhook TLS | [Webhook TLS](webhook-tls.md) |

## Source of truth

Follow the section links in the selected reference file. For a local Markdown link with a heading fragment, read from that heading to the next heading at the same or higher level; do not load the full source document by default. Check the target chart's values and templates for version-specific behavior.
