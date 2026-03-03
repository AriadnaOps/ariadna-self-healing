# Contributing to Ariadna Self-Healing Operator

Thank you for your interest in contributing. This document explains how to build, test, and submit changes.

## Prerequisites

- Go 1.25+ (see [go.mod](go.mod))
- Docker (optional, for image builds)
- `kubectl` and a Kubernetes cluster (optional, for deployment and e2e)
- [controller-gen](https://github.com/kubernetes-sigs/controller-tools) for CRD generation (`go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest`)

## Building and testing

```bash
# Build the operator binary
make build

# Run tests
make test

# Short tests only
make test-short

# Format, vet, lint, and test
make verify
```

## Code generation

If you change API types under `api/`:

```bash
make generate   # DeepCopy and generated code
make manifests  # CRD YAMLs in config/crd/bases
```

CRDs under `config/crd/bases/` are committed; CI checks that `make manifests` does not change them.

## Documentation

**Keep documentation in sync with code changes.** When you update code (APIs, behavior, config, CRDs), update the relevant docs:

- **README.md** – Quick start, features, deployment options
- **docs/** – MkDocs pages (installation, deployment, configuration, crds, architecture, glossary)
- **charts/selfhealing-operator/README.md** – Helm values and usage
- **Code comments** – Minimal and professional: document exported API and non-obvious logic only; avoid tutorial-style or long educational paragraphs.

Run `mkdocs build` (with `docs/requirements.txt` installed) to verify the docs build.

## Opening issues and pull requests

- **Bug reports and feature requests**: open an issue describing the problem or proposal.
- **Pull requests**: target the default branch (e.g. `main`). Ensure `make verify` passes and that CRDs are up to date if you changed APIs.

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/) for consistency and automated changelog generation (e.g. `feat:`, `fix:`, `docs:`, `chore:`).

## License

By contributing, you agree that your contributions will be licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).
